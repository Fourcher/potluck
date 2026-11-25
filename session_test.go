package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// scriptModel answers like llama-server's streaming API from a script: each request gets the
// next reply; a reply is either text or one tool call.
type scriptModel struct {
	mu      sync.Mutex
	replies []string // "text:..." or "call:name:{json args}"
	seen    [][]map[string]any
}

func (f *scriptModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Messages []map[string]any `json:"messages"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.seen = append(f.seen, body.Messages)
	reply := "text:done"
	if len(f.replies) > 0 {
		reply, f.replies = f.replies[0], f.replies[1:]
	}
	f.mu.Unlock()
	w.Header().Set("Content-Type", "text/event-stream")
	chunk := func(delta any) {
		b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta}}})
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	if t, ok := strings.CutPrefix(reply, "error:"); ok { // fails partway
		chunk(map[string]any{"content": "half an ans"})
		b, _ := json.Marshal(map[string]any{"error": map[string]any{"message": t}})
		fmt.Fprintf(w, "data: %s\n\n", b)
		return
	}
	if t, ok := strings.CutPrefix(reply, "cut:"); ok { // the connection closes mid-answer
		chunk(map[string]any{"content": t})
		return
	}
	if t, ok := strings.CutPrefix(reply, "text:"); ok {
		chunk(map[string]any{"content": t})
	} else {
		p := strings.SplitN(strings.TrimPrefix(reply, "call:"), ":", 2)
		chunk(map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "c1", "function": map[string]any{"name": p[0], "arguments": p[1]}}}})
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func withFakeModel(t *testing.T, replies ...string) (*scriptModel, *sessionStore) {
	f := &scriptModel{replies: replies}
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	srv := httptest.NewUnstartedServer(f)
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)
	old := portChat
	portChat, _ = strconv.Atoi(strings.Split(ln.Addr().String(), ":")[1])
	t.Cleanup(func() { portChat = old })
	data := t.TempDir()
	h := &Host{a: &Agent{dir: data}, cfg: loadConfig(data), nodes: map[string]*Node{}}
	return f, h.sessionStore()
}

func waitStatus(t *testing.T, s *Session, want ...string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		st := s.Status
		s.mu.Unlock()
		for _, w := range want {
			if st == w {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("status never became %v (is %s: %s)", want, s.Status, s.Error)
}

func lastTool(s *Session) SMsg {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.Messages) - 1; i >= 0; i-- {
		if s.Messages[i].Role == "tool" {
			return s.Messages[i]
		}
	}
	return SMsg{}
}

func TestSessionAskAnswers(t *testing.T) {
	for _, tc := range []struct{ answer, wantState, wantIn string }{
		{"yes", "ok", "hello-from-session"},
		{"no", "denied", "said no"},
		{"use printf instead", "denied", "use printf instead"},
	} {
		_, st := withFakeModel(t, `call:run_command:{"command":"echo hello-from-session"}`, "text:all done")
		s := st.create(nil, Policy{Commands: "ask"})
		if err := st.send(s, "say hello"); err != nil {
			t.Fatal(err)
		}
		waitStatus(t, s, "waiting")
		qs := st.questions()
		if len(qs) != 1 || qs[0].Kind != "run" || !strings.Contains(qs[0].Detail, "echo hello") {
			t.Fatalf("question: %+v", qs)
		}
		if err := st.answer(s, qs[0].ID, tc.answer); err != nil {
			t.Fatal(err)
		}
		waitStatus(t, s, "idle")
		if m := lastTool(s); m.State != tc.wantState || !strings.Contains(m.Content, tc.wantIn) {
			t.Fatalf("answer %q: tool result %s %q", tc.answer, m.State, m.Content)
		}
		if err := st.answer(s, qs[0].ID, "yes"); err == nil {
			t.Fatal("a question can be answered twice")
		}
	}
}

func TestSessionApproveAll(t *testing.T) {
	_, st := withFakeModel(t, `call:run_command:{"command":"echo free"}`, "text:ok")
	s := st.create(nil, Policy{Commands: "ask", All: true})
	_ = st.send(s, "go")
	waitStatus(t, s, "idle")
	if m := lastTool(s); m.State != "ok" || !strings.Contains(m.Content, "free") {
		t.Fatalf("approve-all still asked or failed: %+v", m)
	}
}

func TestSessionUpfrontAndProjectGrants(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "a.txt"), []byte("old"), 0o644)
	_, st := withFakeModel(t,
		`call:request_permissions:{"reason":"fix a.txt","commands":true,"file_changes":true}`,
		`call:edit_file:{"path":"a.txt","old":"old","new":"new"}`,
		"text:fixed")
	s := st.create(&ProjectRef{Root: root, Name: "demo"}, Policy{})
	_ = st.send(s, "fix it")
	waitStatus(t, s, "waiting")
	q := st.questions()[0]
	if q.Kind != "plan" || !strings.Contains(q.Detail, "change and create files") {
		t.Fatalf("plan question: %+v", q)
	}
	_ = st.answer(s, q.ID, "yes")
	waitStatus(t, s, "idle") // the edit ran without a second question
	if b, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(b) != "new" {
		t.Fatalf("file: %q", b)
	}
	// The next task in the same project starts with those approvals.
	s2 := st.create(&ProjectRef{Root: root, Name: "demo"}, Policy{})
	if !s2.Grants.Write || !s2.Grants.Run {
		t.Fatalf("grants not kept for the project: %+v", s2.Grants)
	}
}

func TestSessionStopWhileWaiting(t *testing.T) {
	_, st := withFakeModel(t, `call:ask_user:{"question":"Which colour?"}`, "text:ok")
	s := st.create(nil, Policy{})
	_ = st.send(s, "paint")
	waitStatus(t, s, "waiting")
	if q := st.questions()[0]; q.Kind != "ask" || q.Title != "Which colour?" {
		t.Fatalf("%+v", q)
	}
	st.stop(s)
	waitStatus(t, s, "stopped")
	if len(st.questions()) != 0 {
		t.Fatal("a stopped task still has an open question")
	}
}

// Running commands freely does not approve the browser, the screen or connectors.
func TestPermissionScopes(t *testing.T) {
	_, st := withFakeModel(t)
	s := st.create(nil, Policy{Commands: "auto"})
	for _, name := range []string{"browser_click", "screen_click", "mcp__files__write"} {
		done := make(chan string, 1)
		go func() {
			no, _ := st.allowed(t.Context(), s, SToolCall{Name: name, Args: "{}"}, map[string]any{})
			done <- no
		}()
		waitStatus(t, s, "waiting")
		q := st.questions()[0]
		_ = st.answer(s, q.ID, "no")
		if no := <-done; no == "" {
			t.Fatalf("%s ran without asking under commands=auto", name)
		}
	}
	if no, _ := st.allowed(t.Context(), s, SToolCall{Name: "run_command", Args: "{}"}, map[string]any{"command": "ls"}); no != "" {
		t.Fatal("commands=auto should run commands without asking")
	}
}

// An engine failure partway through an answer stops the task with the reason, instead of
// ending it "done" with half an answer (found by the 2026-11-24 code review).
func TestSessionStreamFailures(t *testing.T) {
	for _, tc := range []struct{ reply, want string }{
		{"error:out of memory", "out of memory"},
		{"cut:half", "broke off"},
	} {
		_, st := withFakeModel(t, tc.reply)
		s := st.create(nil, Policy{})
		_ = st.send(s, "go")
		waitStatus(t, s, "error", "idle")
		s.mu.Lock()
		status, msg := s.Status, s.Error
		s.mu.Unlock()
		if status != "error" || !strings.Contains(msg, tc.want) {
			t.Fatalf("%s: status %s %q", tc.reply, status, msg)
		}
	}
}
