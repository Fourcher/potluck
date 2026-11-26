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
	if t, ok := strings.CutPrefix(reply, "length:"); ok { // stopped at the length limit
		chunk(map[string]any{"content": t})
		b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{}, "finish_reason": "length"}}})
		fmt.Fprintf(w, "data: %s\n\n", b)
		fmt.Fprint(w, "data: [DONE]\n\n")
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
	s.mcpByName = map[string]MCPTool{"mcp__files__write": {Server: "files", Name: "write"}} // offered at the last step
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

// A connector whose name has spaces is approved by its own name, and a tool name the model
// made up is refused.
func TestConnectorToolNames(t *testing.T) {
	_, st := withFakeModel(t)
	mt := MCPTool{Server: "Read and Write Apple Notes", Name: "add_note"}
	name := mcpToolName(mt)
	s := st.create(nil, Policy{Commands: "auto"})
	s.Grants.Connectors = map[string]bool{"Read and Write Apple Notes": true}
	s.mcpByName = map[string]MCPTool{name: mt}
	if no, _ := st.allowed(t.Context(), s, SToolCall{Name: name, Args: "{}"}, map[string]any{}); no != "" {
		t.Fatalf("an approved connector was not let through: %q", no)
	}
	if no, _ := st.allowed(t.Context(), s, SToolCall{Name: "mcp__other__thing", Args: "{}"}, map[string]any{}); no == "" {
		t.Fatal("a tool that was not offered ran")
	}
}

// An answer cut off at the length limit is carried on, not taken as the end of the task.
func TestSessionCarriesOnAfterLengthLimit(t *testing.T) {
	f, st := withFakeModel(t, "length:first half", "text:second half")
	s := st.create(nil, Policy{})
	_ = st.send(s, "write it")
	waitStatus(t, s, "idle")
	f.mu.Lock()
	n := len(f.seen)
	f.mu.Unlock()
	if n != 2 {
		t.Fatalf("the model was asked %d times; a cut answer should be carried on", n)
	}
	_, st = withFakeModel(t, "length:a", "length:b")
	s = st.create(nil, Policy{})
	_ = st.send(s, "write it")
	waitStatus(t, s, "error")
}

// A call left without a result (stopped partway) gets one when the model reads the task.
func TestOrphanCallsGetResults(t *testing.T) {
	_, st := withFakeModel(t)
	s := st.create(nil, Policy{})
	s.Messages = []SMsg{{Role: "user", Content: "go"}, {Role: "assistant", Calls: []SToolCall{{ID: "a", Name: "x"}, {ID: "b", Name: "y"}}},
		{Role: "tool", CallID: "a", Content: "ok"}, {Role: "user", Content: "carry on"}}
	var ids []string
	for _, m := range st.apiMessages(s) {
		if m["role"] == "tool" {
			ids = append(ids, m["tool_call_id"].(string))
		}
	}
	if strings.Join(ids, ",") != "a,b" {
		t.Fatalf("tool results %v, want a,b", ids)
	}
}

// A question nobody answers does not hold the task for ever: after the person's away time
// a permission is refused (and reported), a question to them is decided by the model.
func TestQuestionsAnsweredWhenAway(t *testing.T) {
	old := awayUnit
	awayUnit = 20 * time.Millisecond
	t.Cleanup(func() { awayUnit = old })
	f, st := withFakeModel(t, `call:run_command:{"command":"echo hi"}`, `call:ask_user:{"question":"Which colour?"}`, "text:done")
	s := st.create(nil, Policy{Commands: "ask", AwayMinutes: 2})
	_ = st.send(s, "go")
	waitStatus(t, s, "idle")
	s.mu.Lock()
	defer s.mu.Unlock()
	var results []string
	for _, m := range s.Messages {
		if m.Role == "tool" {
			results = append(results, m.State+": "+m.Content)
		}
	}
	if len(results) != 2 || !strings.HasPrefix(results[0], "denied") || !strings.Contains(results[0], "not approved") || !strings.Contains(results[1], "safest reasonable option") {
		t.Fatalf("results %q", results)
	}
	_ = f
	if (Policy{AwayMinutes: -1}).awayAfter() != 0 || (Policy{}).awayAfter() != 30*awayUnit {
		t.Fatal("away defaults")
	}
}

// A result too long for the model's memory is cut, saved whole, and readable in parts.
func TestLongResultsSavedAndReadable(t *testing.T) {
	_, st := withFakeModel(t)
	s := st.create(nil, Policy{})
	var b strings.Builder
	for i := 1; i <= 5000; i++ {
		fmt.Fprintf(&b, "line %d of the build log\n", i)
	}
	out := st.fitResult(s, SToolCall{ID: "c9", Name: "run_command"}, b.String())
	if len(out) > 8000 || !strings.Contains(out, "line 1 of") || !strings.Contains(out, "line 5000 of") || !strings.Contains(out, taskOutputPrefix) {
		t.Fatalf("cut result (%d chars) misses its head, tail or the saved path", len(out))
	}
	part, ok, err := st.readTaskOutput(s, "read", map[string]any{"path": taskOutputPrefix + safeName("run_command-c9") + ".txt", "start_line": float64(2500), "max_lines": float64(2)})
	if !ok || err != nil || !strings.Contains(part, "line 2500 of") {
		t.Fatalf("reading the saved result: %v %q", err, part)
	}
	if no, _ := st.allowed(t.Context(), s, SToolCall{Name: "read_file"}, map[string]any{"path": taskOutputPrefix + "x.txt"}); no != "" {
		t.Fatal("reading its own saved result asked for permission")
	}
	if short := st.fitResult(s, SToolCall{ID: "c1", Name: "x"}, "small"); short != "small" {
		t.Fatal("a short result changed")
	}
}

// The task's instructions read the same at every step, so the engine reuses what it read.
func TestTaskInstructionsStayTheSame(t *testing.T) {
	_, st := withFakeModel(t)
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a"), 0o644)
	s := st.create(&ProjectRef{Root: dir, Name: "p"}, Policy{})
	first := st.taskSystem(s)
	s.Messages = append(s.Messages, SMsg{Role: "user", Content: "go"}, SMsg{Role: "assistant", Content: "ok"})
	_ = os.WriteFile(filepath.Join(dir, "b.go"), []byte("package a"), 0o644) // the task made a file
	if again := st.taskSystem(s); again != first {
		t.Fatal("the instructions changed between steps")
	}
	s.Policy.All = true
	if st.taskSystem(s) == first {
		t.Fatal("approve-everything did not reach the instructions")
	}
}

// A task that worked a while tells the phones when it ends; a stopped one does not.
func TestTaskEndAlert(t *testing.T) {
	old := taskEndAlertAfter
	taskEndAlertAfter = 0
	t.Cleanup(func() { taskEndAlertAfter = old })
	_, st := withFakeModel(t, "text:all done")
	ended := make(chan string, 2)
	st.notifyEnd = func(s *Session) { ended <- s.Status }
	s := st.create(nil, Policy{})
	_ = st.send(s, "go")
	select {
	case got := <-ended:
		if got != "idle" {
			t.Fatalf("alert for status %s", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no alert when the task finished")
	}
}

// A task's remembered note is read by the next task on the same project, and not elsewhere.
func TestRememberReachesNextTask(t *testing.T) {
	_, st := withFakeModel(t)
	dir := t.TempDir()
	if _, err := remember(st.host.a.dir, dir, "project", "Run the tests with make check."); err != nil {
		t.Fatal(err)
	}
	if _, err := remember(st.host.a.dir, dir, "global", "The user likes short reports."); err != nil {
		t.Fatal(err)
	}
	s := st.create(&ProjectRef{Root: dir, Name: "p"}, Policy{})
	sys := st.taskSystem(s)
	if !strings.Contains(sys, "make check") || !strings.Contains(sys, "short reports") {
		t.Fatal("the next task did not read the notes")
	}
	other := st.create(&ProjectRef{Root: t.TempDir(), Name: "q"}, Policy{})
	if strings.Contains(st.taskSystem(other), "make check") {
		t.Fatal("a project's note reached another project")
	}
}

func TestTaskReport(t *testing.T) {
	msgs := []SMsg{{Role: "user", Content: "fix it"},
		{Role: "assistant", Calls: []SToolCall{{ID: "a", Name: "edit_file", Args: `{"path":"main.go"}`}, {ID: "b", Name: "run_command", Args: `{"command":"go test ./..."}`}}},
		{Role: "tool", CallID: "a", State: "ok", Content: "Changed."},
		{Role: "tool", CallID: "b", State: "ok", Content: "Exit code 1, 2.0 s, in /p\nFAIL"},
		{Role: "assistant", Calls: []SToolCall{{ID: "c", Name: "run_command", Args: `{"command":"git push"}`}, {ID: "d", Name: "ask_user", Args: `{"question":"Which branch?"}`}}},
		{Role: "tool", CallID: "c", State: "denied", Content: awayAnswer("run", 30*time.Minute)},
		{Role: "tool", CallID: "d", State: "ok", Content: awayAnswer("ask", 30*time.Minute)},
		{Role: "assistant", Content: "Done."}}
	r := taskReport(msgs, 90*time.Minute, "idle", "")
	for _, want := range []string{"Finished after 1.5 hours, 3 steps, 1 command run.", "Files changed (1):\n- main.go", "Commands that failed (1):\n- go test ./...", "Decided without you (nobody answered) (1):\n- Which branch?", "Waiting for your approval (not done) (1):\n- run_command git push"} {
		if !strings.Contains(r, want) {
			t.Fatalf("report misses %q:\n%s", want, r)
		}
	}
}
