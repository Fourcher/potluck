package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"time"
)

// The Potluck helper: a small model of its own (on the processor, on one of the person's
// computers) that sets Potluck up and answers questions about it, using Potluck's own
// controls as tools (helpertools.go). It is separate from the main model, so it can help
// before any model is chosen, while one downloads, and while one runs.

// helperModel is the helper's brain: small enough to download in a minute or two and to run
// on any computer's processor alongside a big model, chosen by measuring its tool use.
// Measured on LLMOS's processor with 15 real setup requests: LFM3 1.7B 13/15 at 22 tokens/s,
// Qwen3.5 2B 10/15 at 18, Qwen3.5 4B 9/15 at 8.
var helperModel = ModelRef{Source: "hf", Repo: "LiquidAI/LFM3-1.7B-GGUF", File: "LFM3-1.7B-Q4_K_M.gguf"}

const helperCtx = 16384

type helperState struct {
	Phase    string `json:"phase"` // off | preparing | ready | error
	Note     string `json:"note,omitempty"`
	Node     string `json:"node,omitempty"`
	NodeName string `json:"nodeName,omitempty"`
	bytes    int64
}

// HelperStatus says whether the helper can answer, and where it runs.
func (h *Host) HelperStatus() helperState {
	// Lock order: never hold helperMu while taking h.mu (the planner holds h.mu, then helperMu).
	h.helperMu.Lock()
	st := h.helper
	h.helperMu.Unlock()
	if st.Phase == "" {
		st.Phase = "off"
	}
	if st.Phase == "ready" {
		h.mu.Lock()
		n := h.nodes[st.Node]
		online := n != nil && n.Online
		h.mu.Unlock()
		if !online {
			st.Phase, st.Note = "off", "The computer running the helper went away."
			h.helperMu.Lock()
			if h.helper.Node == st.Node {
				h.helper.Phase, h.helper.Note = st.Phase, st.Note
			}
			h.helperMu.Unlock()
		}
	}
	return st
}

// EnsureHelper gets the helper going if it isn't, in the background.
func (h *Host) EnsureHelper() {
	if p := h.HelperStatus().Phase; p == "preparing" || p == "ready" {
		return
	}
	h.helperMu.Lock()
	if h.helper.Phase == "preparing" {
		h.helperMu.Unlock()
		return
	}
	h.helper = helperState{Phase: "preparing", Note: "Choosing a computer for the helper"}
	h.helperGen++
	gen := h.helperGen
	h.helperMu.Unlock()
	go func() {
		err := h.startHelper(gen)
		h.helperMu.Lock()
		if err != nil && h.helperGen == gen {
			h.helper.Phase, h.helper.Note = "error", err.Error()
		}
		h.helperMu.Unlock()
	}()
}

func (h *Host) helperNote(s string) {
	h.helperMu.Lock()
	h.helper.Note = s
	h.helperMu.Unlock()
}

var errHelperCancelled = errors.New("cancelled")

// helperCurrent says whether a setup run is still the wanted one (not stopped or restarted).
func (h *Host) helperCurrent(gen int) bool {
	h.helperMu.Lock()
	defer h.helperMu.Unlock()
	return h.helperGen == gen
}

// pickHelperNode: a computer that is lending now and has room, preferring one that isn't
// running the main model, then the one with the most free memory.
func (h *Host) pickHelperNode(need int64) (string, string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	type cand struct {
		id, name string
		free     int64
		head     bool
	}
	headChoice := ""
	if h.cfg != nil {
		headChoice = h.cfg.Get().HeadChoice
	}
	var cs []cand
	for id, n := range h.nodes {
		if !n.Online || !n.Info.Limits.ActiveNow(now) {
			continue
		}
		free := int64(n.Info.RAMAvail)
		if free < need {
			continue
		}
		// "head" here: anything to do with the main model (running it, holding part of it, or
		// chosen to run it), so the helper never crowds it.
		busy := (id == h.cluster.Head && h.cluster.Phase != "idle") || n.Role != "" || n.Held > 0 || id == headChoice
		cs = append(cs, cand{id, n.Info.Name, free, busy})
	}
	if len(cs) == 0 {
		return "", "", errors.New("none of your computers that lend their memory has " + humanBytes(need) + " free for the helper right now")
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].head != cs[j].head {
			return !cs[i].head
		}
		return cs[i].free > cs[j].free
	})
	return cs[0].id, cs[0].name, nil
}

func (h *Host) startHelper(gen int) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Hour)
	defer cancel()
	m := helperModel
	if err := resolveModel(ctx, &m); err != nil {
		return errors.New("could not look up the helper's model: " + err.Error())
	}
	need := m.Size + m.Size/2 + 1<<30 // the model, its working memory, and room to spare
	node, name, err := h.pickHelperNode(need)
	if err != nil {
		return err
	}
	h.helperMu.Lock()
	h.helper.Node, h.helper.NodeName = node, name
	h.helperMu.Unlock()
	h.helperNote("Getting the AI engine ready on " + name)
	h.mu.Lock()
	ni := h.nodes[node].Info
	h.mu.Unlock()
	spec, err := h.runtimeSpec(ni, true)
	if err != nil {
		return err
	}
	spec.Solo = true
	if err := h.call(node, "ensure_runtime", spec, nil, 2*time.Hour); err != nil {
		return err
	}
	h.helperNote("Downloading the helper (" + humanBytes(m.Size) + ") to " + name)
	if err := h.call(node, "fetch_model", FetchModelReq{URL: m.URL, File: m.File, Size: m.Size, SHA256: m.SHA256}, nil, 2*time.Hour); err != nil {
		return err
	}
	if !h.helperCurrent(gen) {
		return errHelperCancelled // stopped or moved while it downloaded: start nothing here
	}
	h.helperNote("Starting the helper on " + name)
	if err := h.call(node, "start_aux", StartAuxReq{Name: "helper", File: m.File, Ctx: helperCtx}, nil, time.Minute); err != nil {
		return err
	}
	for deadline := time.Now().Add(5 * time.Minute); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
		var st AuxStatus
		if err := h.call(node, "aux_status", nil, &st, 15*time.Second); err != nil {
			continue
		}
		if !h.helperCurrent(gen) {
			_ = h.call(node, "stop_aux", nil, nil, 20*time.Second)
			return errHelperCancelled
		}
		if st.Ready {
			h.helperMu.Lock()
			if h.helperGen != gen {
				h.helperMu.Unlock()
				_ = h.call(node, "stop_aux", nil, nil, 20*time.Second)
				return errHelperCancelled
			}
			h.helper.Phase, h.helper.Note, h.helper.bytes = "ready", "", need
			h.helperMu.Unlock()
			return nil
		}
		if !st.Running {
			return errors.New("the helper's engine stopped while loading: " + st.Log)
		}
	}
	return errors.New("the helper took too long to load")
}

// helperLoop gets the helper ready in the background once Potluck is set up as a main
// computer, so it can answer right away; and again if its computer goes away.
func (h *Host) helperLoop() {
	time.Sleep(20 * time.Second)
	for {
		if !h.cfg.Get().HelperOff {
			if p := h.HelperStatus().Phase; p == "off" {
				h.EnsureHelper()
			}
		}
		time.Sleep(2 * time.Minute)
	}
}

// StopHelper unloads the helper.
func (h *Host) StopHelper() {
	h.helperMu.Lock()
	node := h.helper.Node
	h.helper = helperState{Phase: "off"}
	h.helperGen++ // a setup still running stops at its next step
	h.helperMu.Unlock()
	if node != "" {
		_ = h.call(node, "stop_aux", nil, nil, 20*time.Second)
	}
}

// helperReserveLocked is the memory the helper holds on a computer, kept out of the main
// model's plans. Called with h.mu held; it takes only helperMu.
func (h *Host) helperReserveLocked(node string) int64 {
	h.helperMu.Lock()
	defer h.helperMu.Unlock()
	if h.helper.Node == node && (h.helper.Phase == "ready" || h.helper.Phase == "preparing") {
		if h.helper.bytes > 0 {
			return h.helper.bytes
		}
		return 3 << 30
	}
	return 0
}

// helperRoute passes a chat request for the "potluck-helper" model to the helper's engine.
func (h *Host) helperRoute(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
		return false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<20))
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if err != nil {
		return false
	}
	var peek struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(raw, &peek)
	if name, ok := strings.CutPrefix(peek.Model, "crew:"); ok {
		c := h.crewMember(name)
		if c == nil || c.Phase != "running" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "No running crew member called " + name}})
			return true
		}
		h.proxyAux(w, r, c.Node, "aux:"+c.Name, nil)
		return true
	}
	if peek.Model != "potluck-helper" {
		return false
	}
	st := h.HelperStatus()
	if st.Phase != "ready" {
		h.EnsureHelper()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		msg := "The Potluck helper is getting ready"
		if n := h.HelperStatus().Note; n != "" {
			msg += ": " + n
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": msg}})
		return true
	}
	h.proxyAux(w, r, st.Node, "aux", func() {
		h.helperMu.Lock()
		h.helper.Phase = "off"
		h.helperMu.Unlock()
	})
	return true
}

// proxyAux passes a request to an extra engine (the helper or a crew member) on a computer.
func (h *Host) proxyAux(w http.ResponseWriter, r *http.Request, node, target string, onFail func()) {
	u, _ := url.Parse("http://aux.invalid")
	rp := httputil.NewSingleHostReverseProxy(u)
	rp.Transport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return h.openTunnel(node, target)
		},
	}
	rp.FlushInterval = 50 * time.Millisecond
	rp.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		if onFail != nil {
			onFail()
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "Could not reach that model: " + err.Error()}})
	}
	rp.ServeHTTP(w, r)
}
