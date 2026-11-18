package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func chatReq(method, path, remote, host string, hdr map[string]string, body string) *http.Request {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr, r.Host = remote, host
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	return r
}

// Only this computer itself counts as the owner: not the local network, and not a web page
// elsewhere that points a browser on this computer at the chat address under another name.
func TestChatOwner(t *testing.T) {
	for _, c := range []struct {
		remote, host string
		want         bool
	}{
		{"127.0.0.1:5000", "127.0.0.1:7720", true},
		{"127.0.0.1:5000", "localhost:7720", true},
		{"[::1]:5000", "[::1]:7720", true},
		{"192.168.1.30:5000", "192.168.1.20:7720", false},
		{"127.0.0.1:5000", "192.168.1.20:7720", false},
		{"127.0.0.1:5000", "evil.example:7720", false},
		{"192.168.1.30:5000", "127.0.0.1:7720", false},
	} {
		if got := chatOwner(chatReq("GET", "/", c.remote, c.host, nil, "")); got != c.want {
			t.Fatalf("remote %s host %s: owner = %v, want %v", c.remote, c.host, got, c.want)
		}
	}
}

func TestChatHandler(t *testing.T) {
	h := &Host{nodes: map[string]*Node{}}
	h.cluster.Phase = "idle"
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
	srv := h.chatHandler(api)
	do := func(r *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	local, lan := "127.0.0.1:5000", "192.168.1.30:5000"

	// The page and its scripts are built into the program.
	for _, p := range []string{"/", "/chat.js", "/chat.css", "/md.js", "/store.js", "/extract.js"} {
		if w := do(chatReq("GET", p, local, "127.0.0.1:7720", nil, "")); w.Code != 200 || w.Body.Len() < 500 {
			t.Fatalf("%s: status %d, %d bytes", p, w.Code, w.Body.Len())
		}
	}
	if w := do(chatReq("GET", "/", local, "127.0.0.1:7720", nil, "")); !strings.Contains(w.Body.String(), "Potluck chat") {
		t.Fatal("the chat page is not what is served at /")
	}
	// State: the owner learns where the control panel is, a visitor does not.
	var st map[string]any
	_ = json.Unmarshal(do(chatReq("GET", "/potluck/state", local, "127.0.0.1:7720", nil, "")).Body.Bytes(), &st)
	if st["owner"] != true || st["panel"] == nil || st["phase"] != "idle" {
		t.Fatalf("owner state wrong: %v", st)
	}
	st = nil
	_ = json.Unmarshal(do(chatReq("GET", "/potluck/state", lan, "192.168.1.20:7720", nil, "")).Body.Bytes(), &st)
	if st["owner"] != false || st["panel"] != nil {
		t.Fatalf("visitor state wrong: %v", st)
	}
	// Switching models: never for a visitor, never without the page's own header.
	body := `{"file":"model.gguf","ctx":8192}`
	hdr := map[string]string{"X-Potluck": "1", "Content-Type": "application/json"}
	if w := do(chatReq("POST", "/potluck/switch", lan, "192.168.1.20:7720", hdr, body)); w.Code != http.StatusForbidden {
		t.Fatalf("a visitor could switch models: %d", w.Code)
	}
	if w := do(chatReq("POST", "/potluck/switch", local, "127.0.0.1:7720", map[string]string{"Content-Type": "text/plain"}, body)); w.Code != http.StatusForbidden {
		t.Fatalf("a request without the page's header could switch models: %d", w.Code)
	}
	// Everything else is the model's own API.
	for _, p := range []string{"/v1/chat/completions", "/completion", "/props", "/health"} {
		if w := do(chatReq("POST", p, lan, "192.168.1.20:7720", nil, "{}")); w.Code != http.StatusTeapot {
			t.Fatalf("%s did not reach the model's API: %d", p, w.Code)
		}
	}
	if w := do(chatReq("GET", "/potluck/nothing", local, "127.0.0.1:7720", nil, "")); w.Code != http.StatusNotFound {
		t.Fatalf("unknown potluck path: %d", w.Code)
	}
}
