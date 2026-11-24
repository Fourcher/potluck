package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// The loop that runs a session: ask the model, run the tools it calls (asking the person when
// the policy says so), feed the results back, and repeat until the model answers without
// calling a tool.

func (st *sessionStore) maxSteps(s *Session) int {
	if s.Project != nil {
		return 300 // a coding task takes many steps
	}
	return 80
}

func (st *sessionStore) run(ctx context.Context, s *Session) {
	s.setStatus("working", "")
	defer func() {
		s.mu.Lock()
		s.cancel = nil
		status := s.Status
		s.mu.Unlock()
		if ctx.Err() != nil && status != "error" {
			s.setStatus("stopped", "")
		} else if status == "working" {
			s.setStatus("idle", "")
		}
		st.save(s)
	}()
	for steps := 0; ; steps++ {
		if ctx.Err() != nil {
			return
		}
		if steps >= st.maxSteps(s) {
			st.addMsg(s, SMsg{Role: "assistant", Content: fmt.Sprintf("(Stopped after %d steps. Send a message to carry on.)", steps)})
			return
		}
		if err := st.compactIfNeeded(ctx, s); err != nil && ctx.Err() == nil {
			s.setStatus("error", err.Error())
			return
		}
		msg, err := st.complete(ctx, s)
		if err != nil {
			if ctx.Err() == nil {
				s.setStatus("error", err.Error())
			}
			return
		}
		st.addMsg(s, msg)
		if len(msg.Calls) == 0 {
			return
		}
		for _, call := range msg.Calls {
			if ctx.Err() != nil {
				return
			}
			res := st.runTool(ctx, s, call)
			st.addMsg(s, res)
		}
		s.mu.Lock()
		s.Steps++
		s.mu.Unlock()
		st.save(s)
	}
}

func (st *sessionStore) addMsg(s *Session, m SMsg) {
	m.At = time.Now()
	s.mu.Lock()
	s.Messages = append(s.Messages, m)
	s.Updated = time.Now()
	s.mu.Unlock()
	s.emit(SEvent{Type: "message", Msg: &m})
}

// ---------- what the model is told ----------

func (st *sessionStore) systemPrompt(s *Session) string {
	h := st.host
	data := h.a.dir
	ctxTok := h.lastCtx
	if ctxTok <= 0 {
		ctxTok = 8192
	}
	budget := max(2000, ctxTok) // characters of imported text: about a quarter of the context
	capText := func(t string, n int) string {
		n = min(n, budget/2)
		if len(t) > n {
			return t[:n] + "\n(… shortened)"
		}
		return t
	}
	osName := map[string]string{"darwin": "macOS", "windows": "Windows", "linux": "Linux"}[runtime.GOOS]
	p := []string{fmt.Sprintf("You are a capable assistant running privately on the user's own computers through Potluck, working on the user's %s computer. "+
		"You are working on a task the user may have set going and walked away from: keep going on your own until it is done, and do not stop to ask what you can decide yourself. "+
		"When you truly need the user (a choice only they can make, missing information), use ask_user; they can answer from their phone. "+
		"Work in small steps: do something, look at the result, then decide the next step. When you are done, say plainly what you did and what you found.", osName)}
	s.mu.Lock()
	pol, proj, first := s.Policy, s.Project, len(s.Messages) <= 1
	s.mu.Unlock()
	cx := loadContContext(data)
	if strings.TrimSpace(cx.Profile) != "" {
		p = append(p, "What the user brought over from their earlier AI setup: who they are and how they like to work. Treat it as their standing preferences.\n\n"+capText(cx.Profile, 16000))
	}
	if strings.TrimSpace(cx.Memory) != "" {
		p = append(p, "Things you remember about the user:\n\n"+capText(cx.Memory, 12000))
	}
	if len(cx.Skills) > 0 {
		var b strings.Builder
		for i, k := range cx.Skills {
			if i >= 60 {
				break
			}
			fmt.Fprintf(&b, "- %s: %s\n", k.Name, k.Description)
		}
		p = append(p, "Skills the user has set up. When a request matches one, load it with use_skill and follow it:\n"+b.String())
	}
	if proj != nil {
		pi, err := projectInfo(data, proj.Root)
		if err != nil {
			p = append(p, "The project folder "+proj.Root+" could not be opened: "+err.Error())
		} else {
			p = append(p, fmt.Sprintf("You are working in the user's project %q, the folder %s%s, %d files. "+
				"Use list_files, read_file, search_files, edit_file and write_file with paths relative to that folder, and run_command for builds, tests, git and anything else (it starts in the project folder). "+
				"Work like a careful senior engineer: read the files you change, make focused edits with edit_file, build or run the tests to check your work, and fix what fails.",
				pi.Name, pi.Root, map[bool]string{true: " (a git repository)", false: ""}[pi.Git], pi.Files))
			if strings.TrimSpace(pi.Instructions) != "" {
				p = append(p, "The project's own instructions ("+pi.InstrFile+"), which you follow:\n\n"+capText(pi.Instructions, 24000))
			}
			if strings.TrimSpace(pi.Memory) != "" {
				p = append(p, "What you remember about this project:\n\n"+capText(pi.Memory, 16000))
			}
		}
	} else {
		p = append(p, "Commands start in "+h.agentDir()+" unless you give a folder.")
	}
	if !pol.All && first {
		p = append(p, "Before you start, call request_permissions once with everything this task is likely to need (running commands, changing files, which connectors, which apps you will control). "+
			"The user approves it all at once now, so the work is not held up later while they are away. Ask for a little more rather than less.")
	}
	if pol.All {
		p = append(p, "The user has chosen to approve everything for this task: act without asking.")
	}
	return strings.Join(p, "\n\n")
}

type toolSpec struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

func spec(name, desc, params string) toolSpec {
	var t toolSpec
	t.Type = "function"
	t.Function.Name, t.Function.Description, t.Function.Parameters = name, desc, json.RawMessage(params)
	return t
}

func (st *sessionStore) tools(ctx context.Context, s *Session) []toolSpec {
	s.mu.Lock()
	proj, pol := s.Project, s.Policy
	s.mu.Unlock()
	shell := map[string]string{"windows": "PowerShell", "darwin": "the zsh shell"}[runtime.GOOS]
	if shell == "" {
		shell = "the shell"
	}
	t := []toolSpec{
		spec("run_command", "Run a command in "+shell+" on the user's computer and get what it printed and its exit code. It cannot answer questions a command asks, so use options that do not ask.",
			`{"type":"object","properties":{"command":{"type":"string"},"folder":{"type":"string","description":"Folder to run it in"},"timeout_seconds":{"type":"integer","description":"Default 120, at most 900"}},"required":["command"]}`),
		spec("ask_user", "Ask the user something only they can decide. They may be away; they can answer from their phone. Offer a clear yes/no question where you can.",
			`{"type":"object","properties":{"question":{"type":"string"}},"required":["question"]}`),
		spec("request_permissions", "Ask the user once, up front, to approve what this task will need, so it is not held up later.",
			`{"type":"object","properties":{"reason":{"type":"string","description":"One or two sentences: what you are about to do"},"commands":{"type":"boolean","description":"Run commands without asking"},"file_changes":{"type":"boolean","description":"Change and create files without asking"},"browser":{"type":"boolean","description":"Use the web browser without asking"},"connectors":{"type":"array","items":{"type":"string"},"description":"Connectors to use without asking"},"apps":{"type":"array","items":{"type":"string"},"description":"Apps you will control (macOS asks once per app; this asks now)"}},"required":["reason"]}`),
	}
	if runtime.GOOS == "darwin" {
		t = append(t, spec("run_applescript", "Run an AppleScript to control apps on the user's Mac (Finder, Safari, Mail, Notes, System Events for clicks and keystrokes).",
			`{"type":"object","properties":{"script":{"type":"string"},"timeout_seconds":{"type":"integer"}},"required":["script"]}`))
	}
	if proj != nil {
		t = append(t,
			spec("list_files", "List the project's files (dependency and build folders left out).", `{"type":"object","properties":{"path":{"type":"string"},"pattern":{"type":"string","description":"e.g. *.go or src/**/*.ts"}}}`),
			spec("read_file", "Read a file from the project, with line numbers.", `{"type":"object","properties":{"path":{"type":"string"},"start_line":{"type":"integer"},"max_lines":{"type":"integer"}},"required":["path"]}`),
			spec("search_files", "Find lines in the project's files containing some text, or matching /a regular expression/.", `{"type":"object","properties":{"query":{"type":"string"},"pattern":{"type":"string"}},"required":["query"]}`),
			spec("edit_file", "Replace exact text in a file. old must match the file exactly (copied from read_file, without the line numbers) and be unique unless all is true.", `{"type":"object","properties":{"path":{"type":"string"},"old":{"type":"string"},"new":{"type":"string"},"all":{"type":"boolean"}},"required":["path","old","new"]}`),
			spec("write_file", "Create a file in the project, or replace its whole content.", `{"type":"object","properties":{"path":{"type":"string"},"content":{"type":"string"}},"required":["path","content"]}`))
	}
	if findBrowser() != "" {
		t = append(t,
			spec("browser_open", "Open a web page in Potluck's browser window and get it back as text, with every link, button and field numbered.", `{"type":"object","properties":{"url":{"type":"string"}},"required":["url"]}`),
			spec("browser_read", "Read the page that is open now (after it changed), with the numbers of what you can use.", `{"type":"object","properties":{}}`),
			spec("browser_click", "Click a link, button or box on the page, by its number from the page text.", `{"type":"object","properties":{"ref":{"type":"integer"}},"required":["ref"]}`),
			spec("browser_type", "Type into a field on the page, by its number; submit presses Enter afterwards.", `{"type":"object","properties":{"ref":{"type":"integer"},"text":{"type":"string"},"submit":{"type":"boolean"}},"required":["ref","text"]}`),
			spec("browser_back", "Go back to the previous page.", `{"type":"object","properties":{}}`),
			spec("browser_screenshot", "Save a picture of the page (for the user to look at).", `{"type":"object","properties":{}}`))
	}
	if cx := loadContContext(st.host.a.dir); len(cx.Skills) > 0 {
		t = append(t, spec("use_skill", "Load one of the user's skills (listed in your instructions) and get its full instructions.", `{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`))
	}
	var servers []string
	for name, on := range pol.Connectors {
		if on {
			servers = append(servers, name)
		}
	}
	if len(servers) > 0 {
		root := ""
		if proj != nil {
			root = proj.Root
		}
		for _, mt := range mcpTools(ctx, st.host.a.dir, root, servers) {
			params := mt.InputSchema
			if len(params) == 0 || !bytes.Contains(params, []byte(`"object"`)) {
				params = json.RawMessage(`{"type":"object","properties":{}}`)
			}
			t = append(t, spec(mcpToolName(mt), trimTo("["+mt.Server+"] "+mt.Description, 1000), string(params)))
		}
	}
	return t
}

func mcpToolName(t MCPTool) string {
	n := []byte("mcp__" + t.Server + "__" + t.Name)
	for i, c := range n {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			n[i] = '_'
		}
	}
	if len(n) > 64 {
		n = n[:64]
	}
	return string(n)
}

// mcpTools lists the tools of the given connectors (starting them if needed).
func mcpTools(ctx context.Context, data, root string, servers []string) []MCPTool {
	mcps.mu.Lock()
	if mcps.data == "" {
		mcps.data = data
		go mcps.idleLoop()
	}
	mcps.mu.Unlock()
	want := map[string]bool{}
	for _, n := range servers {
		want[n] = true
	}
	var out []MCPTool
	for _, c := range mcps.connectors(root) {
		if !want[c.Name] {
			continue
		}
		c2, cancel := context.WithTimeout(ctx, 60*time.Second)
		t, _ := mcps.client(c).listTools(c2)
		cancel()
		out = append(out, t...)
	}
	return out
}

// ---------- asking the model ----------

func (st *sessionStore) apiMessages(s *Session) []map[string]any {
	out := []map[string]any{{"role": "system", "content": st.systemPrompt(s)}}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, m := range s.Messages {
		switch m.Role {
		case "user":
			out = append(out, map[string]any{"role": "user", "content": m.Content})
		case "assistant":
			a := map[string]any{"role": "assistant", "content": m.Content}
			if len(m.Calls) > 0 {
				var calls []map[string]any
				for _, c := range m.Calls {
					calls = append(calls, map[string]any{"id": c.ID, "type": "function", "function": map[string]string{"name": c.Name, "arguments": c.Args}})
				}
				a["tool_calls"] = calls
			}
			if m.Content == "" && len(m.Calls) == 0 {
				continue
			}
			out = append(out, a)
		case "tool":
			out = append(out, map[string]any{"role": "tool", "tool_call_id": m.CallID, "content": m.Content})
		}
	}
	return out
}

var sessionHTTP = &http.Client{Timeout: 0} // a long answer from a slow model can take an hour

// complete asks the model for its next message, streaming it to the session's watchers.
func (st *sessionStore) complete(ctx context.Context, s *Session) (SMsg, error) {
	s.mu.Lock()
	thinking := s.Policy.Thinking
	s.mu.Unlock()
	body := map[string]any{
		"model": "potluck", "stream": true, "messages": st.apiMessages(s), "tools": st.tools(ctx, s),
		"stream_options":       map[string]bool{"include_usage": true},
		"chat_template_kwargs": map[string]any{"enable_thinking": thinking, "reasoning_effort": map[bool]string{true: "high", false: "none"}[thinking]},
	}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, "POST", "http://127.0.0.1:"+strconv.Itoa(portChat)+"/v1/chat/completions", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	res, err := sessionHTTP.Do(req)
	if err != nil {
		return SMsg{}, fmt.Errorf("could not reach the model: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.NewDecoder(res.Body).Decode(&e)
		if e.Error.Message == "" {
			e.Error.Message = res.Status
		}
		return SMsg{}, errors.New(strings.TrimPrefix(e.Error.Message, "Potluck: "))
	}
	msg := SMsg{Role: "assistant"}
	calls := map[int]*SToolCall{}
	var order []int
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 1<<20), 64<<20)
	for sc.Scan() {
		data, ok := strings.CutPrefix(sc.Text(), "data: ")
		if !ok || data == "[DONE]" {
			continue
		}
		var ch struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					Reasoning string `json:"reasoning_content"`
					ToolCalls []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				Prompt int `json:"prompt_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(data), &ch) != nil {
			continue
		}
		if ch.Usage != nil {
			s.mu.Lock()
			s.lastPrompt = ch.Usage.Prompt
			s.mu.Unlock()
		}
		if len(ch.Choices) == 0 {
			continue
		}
		d := ch.Choices[0].Delta
		if d.Content != "" || d.Reasoning != "" {
			msg.Content += d.Content
			msg.Reasoning += d.Reasoning
			s.emit(SEvent{Type: "delta", Text: d.Content, Reasoning: d.Reasoning})
		}
		for _, tc := range d.ToolCalls {
			c := calls[tc.Index]
			if c == nil {
				c = &SToolCall{}
				calls[tc.Index] = c
				order = append(order, tc.Index)
			}
			if tc.ID != "" {
				c.ID = tc.ID
			}
			c.Name += tc.Function.Name
			c.Args += tc.Function.Arguments
		}
	}
	if err := sc.Err(); err != nil && ctx.Err() == nil {
		return SMsg{}, fmt.Errorf("the answer broke off: %w", err)
	}
	for _, i := range order {
		c := calls[i]
		if c.ID == "" {
			c.ID = "call_" + randomHex(4)
		}
		msg.Calls = append(msg.Calls, *c)
	}
	return msg, ctx.Err()
}

// compactIfNeeded summarises the older part of a long session so it fits the context again.
func (st *sessionStore) compactIfNeeded(ctx context.Context, s *Session) error {
	ctxTok := st.host.lastCtx
	s.mu.Lock()
	used, n := s.lastPrompt, len(s.Messages)
	s.mu.Unlock()
	if ctxTok <= 0 || used < ctxTok*3/4 || n < 8 {
		return nil
	}
	s.emit(SEvent{Type: "status", Status: "working", Error: "Summarising the earlier part of this task to make room"})
	s.mu.Lock()
	keep := 6 // the latest steps stay as they are
	old := append([]SMsg(nil), s.Messages[:n-keep]...)
	s.mu.Unlock()
	var b strings.Builder
	for _, m := range old {
		fmt.Fprintf(&b, "[%s%s] %s\n", m.Role, map[bool]string{true: " " + m.Name, false: ""}[m.Name != ""], trimTo(m.Content, 4000))
		for _, c := range m.Calls {
			fmt.Fprintf(&b, "  (called %s %s)\n", c.Name, trimTo(c.Args, 500))
		}
	}
	body, _ := json.Marshal(map[string]any{"model": "potluck", "messages": []map[string]string{
		{"role": "system", "content": "Summarise this work log for the assistant that continues it: the task, what was done, what was found, files changed, decisions, and what is left. Be complete and specific; it replaces the log."},
		{"role": "user", "content": trimTo(b.String(), ctxTok*2)},
	}, "chat_template_kwargs": map[string]any{"enable_thinking": false, "reasoning_effort": "none"}})
	req, _ := http.NewRequestWithContext(ctx, "POST", "http://127.0.0.1:"+strconv.Itoa(portChat)+"/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := sessionHTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil || len(out.Choices) == 0 {
		return fmt.Errorf("could not summarise the task to make room: %v", err)
	}
	s.mu.Lock()
	rest := s.Messages[n-keep:]
	// A tool result must follow the call that asked for it: start the kept part at a user or
	// assistant message.
	for len(rest) > 0 && rest[0].Role == "tool" {
		rest = rest[1:]
	}
	s.Messages = append([]SMsg{{Role: "user", Content: "Summary of the work so far (it replaces the earlier log):\n\n" + out.Choices[0].Message.Content, At: time.Now()}}, rest...)
	s.lastPrompt = 0
	s.mu.Unlock()
	return nil
}

// ---------- running tools ----------

func (st *sessionStore) runTool(ctx context.Context, s *Session, call SToolCall) SMsg {
	res := SMsg{Role: "tool", CallID: call.ID, Name: call.Name}
	var args map[string]any
	if call.Args != "" && json.Unmarshal([]byte(call.Args), &args) != nil {
		res.State, res.Content = "error", "The tool request was not valid JSON."
		return res
	}
	if args == nil {
		args = map[string]any{}
	}
	str := func(k string) string { v, _ := args[k].(string); return v }
	num := func(k string) int { v, _ := args[k].(float64); return int(v) }
	no, err := st.allowed(ctx, s, call, args)
	if err != nil {
		res.State, res.Content = "denied", "Stopped."
		return res
	}
	if no != "" {
		res.State, res.Content = "denied", no
		return res
	}
	s.mu.Lock()
	proj := s.Project
	s.mu.Unlock()
	root := ""
	if proj != nil {
		root = proj.Root
	}
	out, err := func() (string, error) {
		switch call.Name {
		case "run_command", "run_applescript":
			kind, cmd := "shell", str("command")
			if call.Name == "run_applescript" {
				kind, cmd = "applescript", str("script")
			}
			dir := str("folder")
			if dir == "" {
				dir = root
			}
			r, err := runCommand(ctx, RunReq{Kind: kind, Command: cmd, Dir: dir, Timeout: num("timeout_seconds")}, st.host.agentDir())
			if err != nil {
				return "", err
			}
			t := fmt.Sprintf("Exit code %d, %.1f s, in %s", r.Exit, r.Seconds, r.Dir)
			if r.TimedOut {
				t += " (stopped: it ran past its time limit)"
			}
			return t + "\n" + r.Output, nil
		case "list_files", "read_file", "search_files", "edit_file", "write_file":
			if root == "" {
				return "", errors.New("this task has no project folder")
			}
			op := strings.TrimSuffix(strings.TrimSuffix(call.Name, "_files"), "_file")
			b, _ := args["all"].(bool)
			return projectFS(FSReq{Op: op, Root: root, Path: str("path"), Glob: str("pattern"), Query: str("query"), Start: num("start_line"), Max: num("max_lines"), Content: str("content"), Old: str("old"), New: str("new"), All: b})
		case "use_skill":
			return readSkill(st.host.a.dir, str("name"))
		case "browser_open", "browser_read", "browser_click", "browser_type", "browser_back", "browser_screenshot":
			return browserTool(ctx, st.host.a.dir, call.Name, args)
		case "ask_user":
			ans, err := st.ask(ctx, s, "ask", str("question"), "")
			if err != nil {
				return "", err
			}
			return "The user answered: " + ans, nil
		case "request_permissions":
			return st.requestPermissions(ctx, s, args)
		}
		if strings.HasPrefix(call.Name, "mcp__") {
			s.mu.Lock()
			var servers []string
			for n, on := range s.Policy.Connectors {
				if on {
					servers = append(servers, n)
				}
			}
			s.mu.Unlock()
			for _, mt := range mcpTools(ctx, st.host.a.dir, root, servers) {
				if mcpToolName(mt) == call.Name {
					for _, c := range mcps.connectors(root) {
						if c.Name == mt.Server {
							raw, _ := json.Marshal(args)
							return mcps.client(c).callTool(ctx, mt.Name, raw)
						}
					}
				}
			}
			return "", errors.New("that connector tool is not available now")
		}
		return "", fmt.Errorf("there is no tool called %s", call.Name)
	}()
	if err != nil {
		res.State, res.Content = "error", "Could not do that: "+err.Error()
	} else {
		res.State, res.Content = "ok", out
	}
	return res
}

// requestPermissions puts the model's up-front request to the person as one question.
func (st *sessionStore) requestPermissions(ctx context.Context, s *Session, args map[string]any) (string, error) {
	var items []string
	cmd, _ := args["commands"].(bool)
	files, _ := args["file_changes"].(bool)
	web, _ := args["browser"].(bool)
	if web {
		items = append(items, "• use the web browser without asking (open pages, click, type)")
	}
	if cmd {
		items = append(items, "• run commands without asking")
	}
	if files {
		items = append(items, "• change and create files without asking")
	}
	var conns, apps []string
	if v, ok := args["connectors"].([]any); ok {
		for _, x := range v {
			if n, ok := x.(string); ok && n != "" {
				conns = append(conns, n)
				items = append(items, "• use the connector "+n+" without asking")
			}
		}
	}
	if v, ok := args["apps"].([]any); ok {
		for _, x := range v {
			if n, ok := x.(string); ok && n != "" {
				apps = append(apps, n)
			}
		}
		if len(apps) > 0 {
			items = append(items, "• control these apps: "+strings.Join(apps, ", ")+" (macOS will ask you about each one now)")
		}
	}
	if len(items) == 0 {
		return "Nothing to approve.", nil
	}
	reason, _ := args["reason"].(string)
	ans, err := st.ask(ctx, s, "plan", "Approve what this task needs, up front?", strings.TrimSpace(reason)+"\n\n"+strings.Join(items, "\n")+"\n\nYes approves it for this task and keeps it for this project's next tasks. No: it asks each time instead.")
	if err != nil {
		return "", err
	}
	a := strings.ToLower(strings.TrimSpace(ans))
	if a != "yes" && a != "always" && a != "y" && a != "ok" {
		if a == "no" || a == "n" {
			return "The user did not approve these up front; each action will be asked about when it comes up.", nil
		}
		return "The user did not approve these up front, and said: " + ans, nil
	}
	s.mu.Lock()
	s.Grants.Run = s.Grants.Run || cmd
	s.Grants.Browser = s.Grants.Browser || web
	s.Grants.Write = s.Grants.Write || files
	s.Grants.Read = true
	for _, n := range conns {
		if s.Grants.Connectors == nil {
			s.Grants.Connectors = map[string]bool{}
		}
		s.Grants.Connectors[n] = true
		if s.Policy.Connectors == nil {
			s.Policy.Connectors = map[string]bool{}
		}
		s.Policy.Connectors[n] = true
	}
	g, proj := s.Grants, s.Project
	s.mu.Unlock()
	if proj != nil {
		st.keepGrants(proj.Root, &g)
	}
	// Ask macOS's own questions now, while the person is here, instead of in the middle of the night.
	var primed []string
	if runtime.GOOS == "darwin" {
		for _, app := range apps {
			c, cancel := context.WithTimeout(ctx, 90*time.Second)
			err := exec.CommandContext(c, "osascript", "-e", `tell application "`+strings.ReplaceAll(app, `"`, ``)+`" to get name`).Run()
			cancel()
			if err == nil {
				primed = append(primed, app)
			}
		}
	}
	out := "Approved: " + strings.Join(items, "; ")
	if len(primed) > 0 {
		out += ". macOS allowed control of: " + strings.Join(primed, ", ")
	}
	if len(apps) > len(primed) && runtime.GOOS == "darwin" {
		out += ". Some apps were not allowed or did not answer; ask_user if you need them"
	}
	return out + ".", nil
}
