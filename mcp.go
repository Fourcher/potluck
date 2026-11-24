package main

// Connectors: Potluck as an MCP client, so the MCP servers a person brought over from Claude
// Code, Claude Desktop, Codex or Cursor (continuity.go) work in Potluck's chat and agent.
//
// Local servers ("mcp-stdio") are started on this computer with the command, arguments and
// environment they were configured with, and spoken to over their standard input and output
// (JSON-RPC, one message per line). Remote ones ("mcp-http") are reached over HTTP with the
// streamable-HTTP transport (answers as JSON or as server-sent events). A server is started
// the first time its tools are needed and stopped after ten minutes unused.
//
// Like /potluck/run, only the chat page on this computer can use them, and the page asks the
// person before each use unless they chose to let the agent act freely.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const mcpProtocol = "2025-06-18"

type MCPTool struct {
	Server      string          `json:"server"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

type mcpClient struct {
	conn     ContConnector
	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	pending  map[int64]chan json.RawMessage
	nextID   atomic.Int64
	session  string // streamable HTTP: Mcp-Session-Id
	tools    []MCPTool
	lastUse  time.Time
	dead     error
	initDone bool
}

type mcpManager struct {
	mu      sync.Mutex
	clients map[string]*mcpClient // by connector name + scope
	data    string
}

var mcps = &mcpManager{clients: map[string]*mcpClient{}}

func (m *mcpManager) connectors(root string) []ContConnector {
	var all []ContConnector
	_ = readJSONFile(filepath.Join(contDir(m.data), "connectors.json"), &all)
	var out []ContConnector
	for _, c := range all {
		if c.Kind != "mcp-stdio" && c.Kind != "mcp-http" {
			continue
		}
		if c.Scope == "global" || c.Scope == "" || (root != "" && filepath.Clean(c.Scope) == filepath.Clean(root)) {
			out = append(out, c)
		}
	}
	return out
}

func (m *mcpManager) client(c ContConnector) *mcpClient {
	key := c.Name + "\x00" + c.Scope
	m.mu.Lock()
	defer m.mu.Unlock()
	if cl, ok := m.clients[key]; ok && cl.dead == nil {
		return cl
	}
	cl := &mcpClient{conn: c, pending: map[int64]chan json.RawMessage{}}
	m.clients[key] = cl
	return cl
}

// idleLoop stops servers nobody used for ten minutes.
func (m *mcpManager) idleLoop() {
	for {
		time.Sleep(time.Minute)
		m.mu.Lock()
		for k, cl := range m.clients {
			cl.mu.Lock()
			idle := time.Since(cl.lastUse) > 10*time.Minute
			cl.mu.Unlock()
			if idle {
				cl.close(errors.New("stopped after ten minutes unused"))
				delete(m.clients, k)
			}
		}
		m.mu.Unlock()
	}
}

func (cl *mcpClient) close(why error) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.dead == nil {
		cl.dead = why
	}
	if cl.stdin != nil {
		_ = cl.stdin.Close()
	}
	if cl.cmd != nil && cl.cmd.Process != nil {
		killTree(cl.cmd.Process.Pid)
	}
	for id, ch := range cl.pending {
		close(ch)
		delete(cl.pending, id)
	}
}

// start launches a local server and reads its answers.
func (cl *mcpClient) start() error {
	c := cl.conn
	if c.Command == "" {
		return fmt.Errorf("connector %s has no command to start", c.Name)
	}
	cmd := exec.Command(c.Command, c.Args...)
	cmd.Env = os.Environ()
	for k, v := range c.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	ownGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var errBuf bytes.Buffer
	cmd.Stderr = &limitWriter{w: &errBuf, n: 4000}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start %s (%s): %w", c.Name, c.Command, err)
	}
	cl.cmd, cl.stdin = cmd, stdin
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			cl.deliver(sc.Bytes())
		}
		_ = cmd.Wait()
		msg := strings.TrimSpace(errBuf.String())
		if len(msg) > 300 {
			msg = msg[len(msg)-300:]
		}
		cl.close(fmt.Errorf("%s stopped%s", c.Name, map[bool]string{true: ": " + msg, false: ""}[msg != ""]))
	}()
	return nil
}

type limitWriter struct {
	w io.Writer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(len(p), l.n)
		l.n -= k
		_, _ = l.w.Write(p[:k])
	}
	return len(p), nil
}

func (cl *mcpClient) deliver(line []byte) {
	var msg struct {
		ID     *json.RawMessage `json:"id"`
		Method string           `json:"method"`
	}
	if json.Unmarshal(line, &msg) != nil || msg.ID == nil || msg.Method != "" {
		return // notifications and requests from the server are not needed here
	}
	var id int64
	if json.Unmarshal(*msg.ID, &id) != nil {
		return
	}
	cl.mu.Lock()
	ch := cl.pending[id]
	delete(cl.pending, id)
	cl.mu.Unlock()
	if ch != nil {
		ch <- append(json.RawMessage(nil), line...)
	}
}

// call sends one request and waits for its answer.
func (cl *mcpClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := cl.nextID.Add(1)
	req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	var raw json.RawMessage
	if cl.conn.Kind == "mcp-http" {
		var err error
		if raw, err = cl.httpCall(ctx, req, id); err != nil {
			return nil, err
		}
	} else {
		ch := make(chan json.RawMessage, 1)
		cl.mu.Lock()
		if cl.dead != nil {
			cl.mu.Unlock()
			return nil, cl.dead
		}
		cl.pending[id] = ch
		_, err := cl.stdin.Write(append(req, '\n'))
		cl.mu.Unlock()
		if err != nil {
			return nil, err
		}
		select {
		case raw = <-ch:
			if raw == nil {
				cl.mu.Lock()
				err := cl.dead
				cl.mu.Unlock()
				return nil, err
			}
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	var res struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	if res.Error != nil {
		return nil, fmt.Errorf("%s: %s", cl.conn.Name, res.Error.Message)
	}
	return res.Result, nil
}

func (cl *mcpClient) notify(method string) {
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	if cl.conn.Kind == "mcp-http" {
		_, _ = cl.httpPost(context.Background(), b)
		return
	}
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if cl.stdin != nil {
		_, _ = cl.stdin.Write(append(b, '\n'))
	}
}

var mcpHTTP = &http.Client{Timeout: 5 * time.Minute}

func (cl *mcpClient) httpPost(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", cl.conn.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("MCP-Protocol-Version", mcpProtocol)
	if cl.session != "" {
		req.Header.Set("Mcp-Session-Id", cl.session)
	}
	for k, v := range cl.conn.Env { // remote connectors keep their headers here, e.g. Authorization
		if strings.HasPrefix(k, "header:") {
			req.Header.Set(strings.TrimPrefix(k, "header:"), v)
		}
	}
	return mcpHTTP.Do(req)
}

func (cl *mcpClient) httpCall(ctx context.Context, body []byte, id int64) (json.RawMessage, error) {
	res, err := cl.httpPost(ctx, body)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if s := res.Header.Get("Mcp-Session-Id"); s != "" {
		cl.session = s
	}
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 2000))
		return nil, fmt.Errorf("%s answered %s: %s", cl.conn.Name, res.Status, strings.TrimSpace(string(b)))
	}
	if strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
		sc := bufio.NewScanner(res.Body)
		sc.Buffer(make([]byte, 1<<20), 64<<20)
		for sc.Scan() {
			if data, ok := strings.CutPrefix(sc.Text(), "data:"); ok {
				var m struct {
					ID json.RawMessage `json:"id"`
				}
				if json.Unmarshal([]byte(data), &m) == nil && string(m.ID) == fmt.Sprint(id) {
					return json.RawMessage(strings.TrimSpace(data)), nil
				}
			}
		}
		return nil, fmt.Errorf("%s closed the stream without answering", cl.conn.Name)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, 64<<20))
	return b, err
}

// ensure starts the server and does the opening handshake, once.
func (cl *mcpClient) ensure(ctx context.Context) error {
	cl.mu.Lock()
	cl.lastUse = time.Now()
	ready, dead := cl.initDone, cl.dead
	cl.mu.Unlock()
	if dead != nil {
		return dead
	}
	if ready {
		return nil
	}
	if cl.conn.Kind == "mcp-stdio" && cl.cmd == nil {
		if err := cl.start(); err != nil {
			cl.close(err)
			return err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if _, err := cl.call(ctx, "initialize", map[string]any{
		"protocolVersion": mcpProtocol,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "potluck", "version": version},
	}); err != nil {
		cl.close(err)
		return fmt.Errorf("%s did not start up: %w", cl.conn.Name, err)
	}
	cl.notify("notifications/initialized")
	cl.mu.Lock()
	cl.initDone = true
	cl.mu.Unlock()
	return nil
}

func (cl *mcpClient) listTools(ctx context.Context) ([]MCPTool, error) {
	if err := cl.ensure(ctx); err != nil {
		return nil, err
	}
	cl.mu.Lock()
	cached := cl.tools
	cl.mu.Unlock()
	if cached != nil {
		return cached, nil
	}
	var out []MCPTool
	cursor := ""
	for page := 0; page < 20; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, err := cl.call(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var res struct {
			Tools []struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				InputSchema json.RawMessage `json:"inputSchema"`
			} `json:"tools"`
			Next string `json:"nextCursor"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, err
		}
		for _, t := range res.Tools {
			out = append(out, MCPTool{Server: cl.conn.Name, Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
		}
		if cursor = res.Next; cursor == "" {
			break
		}
	}
	cl.mu.Lock()
	cl.tools = out
	cl.mu.Unlock()
	return out, nil
}

func (cl *mcpClient) callTool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if err := cl.ensure(ctx); err != nil {
		return "", err
	}
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	raw, err := cl.call(ctx, "tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return "", err
	}
	var res struct {
		Content []struct {
			Type     string `json:"type"`
			Text     string `json:"text"`
			MimeType string `json:"mimeType"`
			Resource *struct {
				URI  string `json:"uri"`
				Text string `json:"text"`
			} `json:"resource"`
		} `json:"content"`
		Structured json.RawMessage `json:"structuredContent"`
		IsError    bool            `json:"isError"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return string(raw), nil
	}
	var parts []string
	for _, c := range res.Content {
		switch {
		case c.Type == "text":
			parts = append(parts, c.Text)
		case c.Resource != nil:
			parts = append(parts, "["+c.Resource.URI+"]\n"+c.Resource.Text)
		default:
			parts = append(parts, fmt.Sprintf("(%s content%s, not shown)", c.Type, map[bool]string{true: " " + c.MimeType, false: ""}[c.MimeType != ""]))
		}
	}
	if len(parts) == 0 && len(res.Structured) > 0 {
		parts = append(parts, string(res.Structured))
	}
	out := strings.Join(parts, "\n")
	if res.IsError {
		return "", errors.New(out)
	}
	return out, nil
}

// MCPReq is one request from the chat page.
type MCPReq struct {
	Op      string          `json:"op"`      // tools | call
	Root    string          `json:"root"`    // the project folder of the chat, for project-scoped servers
	Servers []string        `json:"servers"` // tools: only these (the ones the person switched on); nothing else is started
	Server  string          `json:"server"`
	Tool    string          `json:"tool"`
	Args    json.RawMessage `json:"args"`
}

func handleMCP(w http.ResponseWriter, r *http.Request, data string) {
	if !chatOwner(r) || r.Header.Get("X-Potluck") != "1" || !sameOrigin(r) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "connectors can only be used from the chat page on the computer running Potluck"})
		return
	}
	mcps.mu.Lock()
	if mcps.data == "" {
		mcps.data = data
		go mcps.idleLoop()
	}
	mcps.mu.Unlock()
	var in MCPReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&in); err != nil {
		fail(w, err)
		return
	}
	switch in.Op {
	case "tools":
		// Every server in parallel; one that does not start does not hold up the others.
		want := map[string]bool{}
		for _, n := range in.Servers {
			want[n] = true
		}
		var conns []ContConnector
		for _, c := range mcps.connectors(in.Root) {
			if want[c.Name] {
				conns = append(conns, c)
			}
		}
		type res struct {
			tools []MCPTool
			err   string
		}
		results := make([]res, len(conns))
		var wg sync.WaitGroup
		for i, c := range conns {
			wg.Add(1)
			go func(i int, c ContConnector) {
				defer wg.Done()
				ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
				defer cancel()
				t, err := mcps.client(c).listTools(ctx)
				results[i].tools = t
				if err != nil {
					results[i].err = c.Name + ": " + err.Error()
				}
			}(i, c)
		}
		wg.Wait()
		var tools []MCPTool
		var problems []string
		for _, rr := range results {
			tools = append(tools, rr.tools...)
			if rr.err != "" {
				problems = append(problems, rr.err)
			}
		}
		writeJSON(w, map[string]any{"tools": tools, "problems": problems})
	case "call":
		for _, c := range mcps.connectors(in.Root) {
			if c.Name == in.Server {
				ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
				defer cancel()
				out, err := mcps.client(c).callTool(ctx, in.Tool, in.Args)
				if err != nil {
					fail(w, err)
					return
				}
				writeJSON(w, map[string]string{"text": out})
				return
			}
		}
		fail(w, fmt.Errorf("no connector called %s", in.Server))
	default:
		fail(w, errors.New("unknown connector operation"))
	}
}
