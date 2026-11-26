package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A phone pairs once with the code's secret, then reaches only chat, state, models and
// switching, with its own key; commands and the chat page stay out of reach.
func TestPhonePairing(t *testing.T) {
	dir := t.TempDir()
	id, err := loadOrCreateIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := &Host{id: id, cfg: loadConfig(dir), nodes: map[string]*Node{id.FP: {Info: NodeInfo{Name: "Desk"}}}}
	h.cluster.Phase = "idle"
	model := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h.chatAPI = h.chatHandler(model)
	srv := h.phoneHandler()
	do := func(method, path, key, body string) *httptest.ResponseRecorder {
		hdr := map[string]string{"Content-Type": "application/json"}
		if key != "" {
			hdr["Authorization"] = "Bearer " + key
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, chatReq(method, path, "100.101.1.2:5000", "100.108.208.116:7721", hdr, body))
		return w
	}

	// No code showing: pairing is refused.
	if w := do("POST", "/phone/pair", "", `{"secret":"x","name":"Phone"}`); w.Code != http.StatusForbidden {
		t.Fatalf("pairing without a code: %d", w.Code)
	}
	h.pairSecret, h.pairUntil = "s3cret", time.Now().Add(time.Minute)
	if w := do("POST", "/phone/pair", "", `{"secret":"wrong","name":"Phone"}`); w.Code != http.StatusForbidden {
		t.Fatalf("wrong secret: %d", w.Code)
	}
	w := do("POST", "/phone/pair", "", `{"secret":"s3cret","name":"Jo's iPhone"}`)
	var got struct{ Key, ID, Name string }
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if w.Code != 200 || len(got.Key) != 64 || got.Name != "Desk" {
		t.Fatalf("pairing: %d %s", w.Code, w.Body.String())
	}
	if p := h.cfg.Get().Phones[got.ID]; p.Name != "Jo's iPhone" || p.Hash == got.Key || p.Hash != hashKey(got.Key) {
		t.Fatalf("stored phone wrong: %+v", p)
	}
	// The code works once.
	if w := do("POST", "/phone/pair", "", `{"secret":"s3cret","name":"Other"}`); w.Code != http.StatusForbidden {
		t.Fatalf("code used twice: %d", w.Code)
	}

	// Without the key, or with a wrong one: nothing.
	for _, k := range []string{"", "nope", strings.Repeat("a", 64)} {
		if w := do("GET", "/potluck/state", k, ""); w.Code != http.StatusUnauthorized {
			t.Fatalf("key %q: %d", k, w.Code)
		}
		if w := do("POST", "/v1/chat/completions", k, "{}"); w.Code != http.StatusUnauthorized {
			t.Fatalf("chat with key %q: %d", k, w.Code)
		}
	}
	// With the key: hello, state and the model's API.
	if w := do("GET", "/phone/hello", got.Key, ""); w.Code != 200 || !strings.Contains(w.Body.String(), "Jo's iPhone") {
		t.Fatalf("hello: %d %s", w.Code, w.Body.String())
	}
	var st map[string]any
	w = do("GET", "/potluck/state", got.Key, "")
	_ = json.Unmarshal(w.Body.Bytes(), &st)
	if w.Code != 200 || st["phase"] != "idle" || st["owner"] != false || st["agentDir"] != nil {
		t.Fatalf("state: %d %v", w.Code, st)
	}
	if w := do("POST", "/v1/chat/completions", got.Key, "{}"); w.Code != http.StatusTeapot {
		t.Fatalf("chat did not reach the model: %d", w.Code)
	}
	// Never: commands, the chat page's own switch, the page itself, the control panel API.
	for _, p := range []string{"/potluck/run", "/potluck/switch", "/", "/chat.js", "/api/state", "/completion"} {
		for _, m := range []string{"GET", "POST"} {
			if w := do(m, p, got.Key, `{"command":"echo hi"}`); w.Code != http.StatusNotFound {
				t.Fatalf("%s %s with a key: %d", m, p, w.Code)
			}
		}
	}

	// Tasks: a phone can read them and tell one to carry on, but never reach one without its key.
	h.a = &Agent{dir: dir}
	ts := h.sessionStore().create(nil, Policy{})
	ts.Title, ts.Report = "Nightly tests", "Finished after 2 hours, 40 steps, 12 commands run.\nFiles changed (1):\n- a.go"
	ts.Messages = []SMsg{{Role: "user", Content: "run the tests"}, {Role: "assistant", Content: "All green.", Calls: []SToolCall{{ID: "x", Name: "run_command", Args: `{"command":"make test"}`}}}}
	if w := do("GET", "/phone/tasks", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("tasks without a key: %d", w.Code)
	}
	w = do("GET", "/phone/tasks", got.Key, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Nightly tests") || !strings.Contains(w.Body.String(), "Finished after 2 hours") || strings.Contains(w.Body.String(), "a.go") {
		t.Fatalf("task list: %d %s", w.Code, w.Body.String())
	}
	w = do("GET", "/phone/tasks/"+ts.ID, got.Key, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "All green.") || !strings.Contains(w.Body.String(), "run_command") {
		t.Fatalf("one task: %d %s", w.Code, w.Body.String())
	}
	if w := do("GET", "/phone/tasks/nope", got.Key, ""); w.Code != http.StatusNotFound {
		t.Fatalf("unknown task: %d", w.Code)
	}

	// Removed from the control panel: the key stops working at once.
	h.cfg.Update(func(c *Config) { delete(c.Phones, got.ID) })
	if w := do("GET", "/potluck/state", got.Key, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("removed phone still gets in: %d", w.Code)
	}
}

func TestPhoneAddrs(t *testing.T) {
	for ip, want := range map[string]bool{"100.108.208.116": true, "100.63.0.1": false, "8.8.8.8": false} {
		if got := isTailscale(net.ParseIP(ip)); got != want {
			t.Fatalf("%s: %v", ip, got)
		}
	}
}
