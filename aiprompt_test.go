package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The prompt for the user's own AI carries the real address and current model, and is served
// as plain text at /potluck/ai.
func TestAIGuide(t *testing.T) {
	h := &Host{cfg: loadConfig(t.TempDir()), nodes: map[string]*Node{}}
	h.cluster.Phase, h.cluster.Model, h.lastCtx = "running", &ModelRef{Title: "Tiny Model", File: "tiny.gguf"}, 8192
	g := h.aiGuide()
	for _, want := range []string{"http://127.0.0.1:7720/v1", "Loaded now: Tiny Model.", "8192 tokens", "/potluck/switch", "Ask me before switching models", "only answers on this computer"} {
		if !strings.Contains(g, want) {
			t.Fatalf("guide is missing %q:\n%s", want, g)
		}
	}
	h.cluster.Phase, h.asleep = "idle", h.cluster.Model
	if g := h.aiGuide(); !strings.Contains(g, "resting") {
		t.Fatalf("a resting model is not described as resting:\n%s", g)
	}
	h.cfg.Update(func(c *Config) { c.ShareChat = true })
	if g := h.aiGuide(); len(localIPs()) > 0 && !strings.Contains(g, "home network") {
		t.Fatalf("sharing on, but the guide does not give the network address:\n%s", g)
	}
	m := aiMemory()
	for _, want := range []string{"save this to your long-term memory", "use Potluck", "run this on the local AI", "http://127.0.0.1:7720/potluck/ai", "never send the work to an online model"} {
		if !strings.Contains(m, want) {
			t.Fatalf("memory note is missing %q:\n%s", want, m)
		}
	}
	w := httptest.NewRecorder()
	h.chatHandler(http.NotFoundHandler()).ServeHTTP(w, chatReq("GET", "/potluck/ai", "127.0.0.1:5000", "127.0.0.1:7720", nil, ""))
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") || !strings.Contains(w.Body.String(), "/v1/chat/completions") {
		t.Fatalf("/potluck/ai: %d %q", w.Code, w.Body.String())
	}
}
