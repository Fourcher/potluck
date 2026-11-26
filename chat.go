package main

// The chat page. It is served on the same address as the model's API (port 7720), so
// whatever can reach the API can use the chat: this computer always, the local network
// when sharing is switched on.
//
// The page keeps chats in the visitor's own browser; this computer stores none of them.
// Besides the page itself there are three small endpoints under /potluck/: what is running,
// which models are on disk, and switching models. Switching is only for this computer.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// chatOwner says whether a request comes from this computer itself (not from the local
// network through the sharing switch, and not from a web page somewhere else that points
// its requests here).
func chatOwner(r *http.Request) bool {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !net.ParseIP(ip).IsLoopback() {
		return false
	}
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	return host == "localhost" || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
}

type chatNode struct {
	Name   string  `json:"name"`
	Role   string  `json:"role"`
	Status string  `json:"status"`
	Online bool    `json:"online"`
	Held   int64   `json:"held"`
	LinkMs float64 `json:"linkMs"`
	Done   int64   `json:"done"`
	Total  int64   `json:"total"`
}

type chatModel struct {
	Title    string `json:"title"`
	File     string `json:"file"`
	Size     int64  `json:"size"`
	Node     string `json:"node"`
	NodeName string `json:"nodeName"`
	Running  bool   `json:"running"`
}

// chatHandler wraps the model's API with the chat page and its endpoints.
func (h *Host) chatHandler(api http.Handler) http.Handler {
	sub, _ := fs.Sub(webFS, "web/chat")
	static := http.FileServer(http.FS(sub))
	// For working on the page: POTLUCK_DEV_WEB=<folder holding chat/> serves it from disk.
	if dir := os.Getenv("POTLUCK_DEV_WEB"); dir != "" {
		static = http.FileServer(http.Dir(filepath.Join(dir, "chat")))
	}
	pages := map[string]bool{"/": true, "/index.html": true, "/chat.js": true, "/chat.css": true, "/md.js": true, "/store.js": true, "/extract.js": true}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && pages[r.URL.Path]:
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("Referrer-Policy", "no-referrer")
			static.ServeHTTP(w, r)
		case r.URL.Path == "/potluck/state" && r.Method == http.MethodGet:
			writeJSON(w, h.chatState(chatOwner(r)))
		case r.URL.Path == "/potluck/models" && r.Method == http.MethodGet:
			writeJSON(w, map[string]any{"models": h.visibleModels(isShared(r)), "canSwitch": chatOwner(r)})
		case r.URL.Path == "/potluck/switch" && r.Method == http.MethodPost:
			if !chatOwner(r) || r.Header.Get("X-Potluck") != "1" {
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "only the computer running Potluck can switch models"})
				return
			}
			var in struct {
				File string `json:"file"`
				Node string `json:"node"`
				Ctx  int    `json:"ctx"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
				fail(w, err)
				return
			}
			if err := h.chatSwitch(in.File, in.Node, in.Ctx); err != nil {
				fail(w, err)
				return
			}
			writeJSON(w, map[string]bool{"ok": true})
		case r.URL.Path == "/potluck/context" && r.Method == http.MethodGet:
			// The imported setup (continuity.go), for this computer's own chat and agent only.
			if !chatOwner(r) {
				http.NotFound(w, r)
				return
			}
			writeJSON(w, loadContContext(h.a.dir))
		case r.URL.Path == "/potluck/skill" && r.Method == http.MethodGet:
			if !chatOwner(r) {
				http.NotFound(w, r)
				return
			}
			text, err := readSkill(h.a.dir, r.URL.Query().Get("name"))
			if err != nil {
				fail(w, err)
				return
			}
			writeJSON(w, map[string]string{"text": text})
		case r.URL.Path == "/potluck/import" && r.Method == http.MethodPost:
			// Another AI on this computer hands over a bundle (the panel's "bring your setup" prompt).
			// Programs (curl, an AI's tools) send no Origin; a web page elsewhere would.
			if !chatOwner(r) || r.Header.Get("X-Potluck") != "1" || !sameOrigin(r) {
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "only programs on this computer can import, with the header X-Potluck: 1"})
				return
			}
			raw, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20))
			c, err := bundleFound(raw)
			if err != nil {
				fail(w, err)
				return
			}
			src, err := saveContinuity(h.a.dir, []*contFound{c})
			if err != nil {
				fail(w, err)
				return
			}
			writeJSON(w, map[string]any{"ok": true, "imported": src[len(src)-1]})
		case r.URL.Path == "/potluck/ai" && r.Method == http.MethodGet:
			// How another AI can use Potluck: the same text the control panel copies.
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = io.WriteString(w, h.aiGuide(chatOwner(r), isShared(r)))
		case strings.HasPrefix(r.URL.Path, "/potluck/sessions") || r.URL.Path == "/potluck/questions":
			if !chatOwner(r) {
				http.NotFound(w, r)
				return
			}
			h.sessionStore().handle(w, r, true)
		case r.URL.Path == "/potluck/mcp" && r.Method == http.MethodPost:
			handleMCP(w, r, h.a.dir)
		case r.URL.Path == "/potluck/fs" && r.Method == http.MethodPost:
			handleFS(w, r, h.a.dir)
		case r.URL.Path == "/potluck/run" && r.Method == http.MethodPost:
			handleRun(w, r, h.agentDir())
		case strings.HasPrefix(r.URL.Path, "/potluck/"):
			http.NotFound(w, r)
		default:
			// The model API answers this computer's own programs (a loopback Host) and the share
			// and phone listeners, which mark their requests. Anything else reached 127.0.0.1 by a
			// renamed address (a web page whose DNS now points here): it gets nothing, not the
			// models and not the person's paid connections.
			if !isShared(r) && !chatOwner(r) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "Potluck: use http://127.0.0.1:" + strconv.Itoa(portChat) + " or http://localhost:" + strconv.Itoa(portChat)}})
				return
			}
			// Read a request's body once, with a limit, so every step below sees all of it
			// and one that is too big is refused as such (not cut short and sent on).
			if r.Body != nil && r.Method == http.MethodPost {
				raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxChatBody))
				if err != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusRequestEntityTooLarge)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "Potluck: the request is bigger than 64 MB"}})
					return
				}
				r.Body, r.ContentLength = io.NopCloser(bytes.NewReader(raw)), int64(len(raw))
			}
			// One thinking switch for every client (thinking.go).
			thinkingRequest(w, r)
			// Crew members (Potluck's own models, for shared devices too) and the Potluck
			// helper's small model (this computer only).
			if h.helperRoute(w, r, isShared(r)) {
				return
			}
			// A model from one of the person's other AI setups goes to that server.
			if h.connRoute(w, r) {
				return
			}
			if r.Method == http.MethodGet && r.URL.Path == "/v1/models" {
				h.mergedModels(w, r, api, isShared(r))
				return
			}
			if r.Method == http.MethodPost && isModelRequest(r) {
				h.inflight.Add(1)
				defer h.inflight.Add(-1)
				h.touch()
				// The model was let go after a while unused: load it again, then answer.
				if err := h.wake(15 * time.Minute); err != nil {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusServiceUnavailable)
					_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "Potluck: " + err.Error()}})
					return
				}
			}
			api.ServeHTTP(w, r)
		}
	})
}

func (h *Host) chatState(owner bool) map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.cluster
	out := map[string]any{"version": version, "phase": c.Phase, "error": c.Error, "steps": c.Steps, "owner": owner, "ctx": h.lastCtx, "pauseWhy": h.pauseWhy, "vision": h.vision,
		"busy": h.inflight.Load()} // requests the model is working on now (it answers one at a time)
	if c.Phase == "idle" && h.asleep != nil {
		// Let go after a while unused; the next message loads it again.
		out["phase"], c.Model = "resting", h.asleep
	}
	if c.Model != nil {
		out["model"] = map[string]any{"title": c.Model.Title, "file": c.Model.File, "size": c.Model.Size}
	}
	if owner {
		out["panel"] = "http://127.0.0.1:" + strconv.Itoa(portUI)
		out["os"], out["agentDir"] = runtime.GOOS, agentDirOf(h.cfg) // for agent mode
	}
	var nodes []chatNode
	for _, n := range h.nodes {
		if n.Role == "" && n.Total == 0 {
			continue
		}
		nodes = append(nodes, chatNode{Name: n.Info.Name, Role: n.Role, Status: n.Status, Online: n.Online, Held: n.Held, LinkMs: n.LinkMs, Done: n.Done, Total: n.Total})
	}
	sort.Slice(nodes, func(i, j int) bool {
		if (nodes[i].Role == "head") != (nodes[j].Role == "head") {
			return nodes[i].Role == "head"
		}
		return nodes[i].Name < nodes[j].Name
	})
	out["nodes"] = nodes
	return out
}

// chatModels lists the complete model files on every connected computer.
// visibleModels is the model list a request may see: a shared device sees Potluck's own
// models only, never the person's external AI connections (often paid, and theirs alone).
func (h *Host) visibleModels(shared bool) []chatModel {
	ms := h.chatModels()
	if !shared {
		return ms
	}
	own := ms[:0:0]
	for _, m := range ms {
		if !strings.HasPrefix(m.File, "ext:") {
			own = append(own, m)
		}
	}
	return own
}

func (h *Host) chatModels() []chatModel {
	h.mu.Lock()
	var running string
	if h.cluster.Model != nil && (h.cluster.Phase == "running" || h.cluster.Phase == "starting") {
		running = h.cluster.Model.File
	}
	names := map[string]string{}
	for id, n := range h.nodes {
		names[id] = n.Info.Name
	}
	h.mu.Unlock()
	known := h.cfg.Get().Known
	var out []chatModel
	seen := map[string]bool{}
	for id, rep := range h.Storage() {
		for _, f := range rep.Models {
			// A model that comes as several files is one entry, under its first file.
			if !f.Complete || f.AddOn || seen[f.File] || (splitRe.MatchString(f.File) && !strings.Contains(f.File, "-00001-of-")) {
				continue
			}
			seen[f.File] = true
			m := chatModel{Title: strings.TrimSuffix(f.File, ".gguf"), File: f.File, Size: f.Size, Node: id, NodeName: names[id], Running: f.File == running}
			if k, ok := known[f.File]; ok {
				if k.Title != "" {
					m.Title = k.Title
				}
				if k.Size > 0 {
					m.Size = k.Size
				}
			}
			out = append(out, m)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Running != out[j].Running {
			return out[i].Running
		}
		return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
	})
	return append(append(out, h.crewChatModels()...), h.connChatModels()...)
}

// chatSwitch starts a model that is already on one of the computers.
func (h *Host) chatSwitch(file, node string, ctx int) error {
	if file == "" || strings.ContainsAny(file, `/\`) {
		return errors.New("which model?")
	}
	switch ctx {
	case 4096, 8192, 16384, 32768, 65536, 131072:
	default:
		ctx = 0 // chosen to fit (autoCtx)
	}
	// A model Potluck downloaded itself is started the way it was first started, so that
	// helpers fetch their own share instead of receiving it over the local network.
	ref, ok := h.cfg.Get().Known[file]
	if !ok {
		if node == "" {
			return errors.New("that model is not on any connected computer")
		}
		ref = ModelRef{Source: "local", File: file, Node: node}
	}
	return h.Start(ref, ctx, "")
}

// maxChatBody: the largest chat request accepted (a long conversation with pictures).
const maxChatBody = 64 << 20
