package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Using Potluck on one computer: models other apps already downloaded, and letting go of
// the model's memory when it has not been used for a while.

// FoundView is a found model as the control panel shows it.
type FoundView struct {
	FoundModel
	Here     string     `json:"here"`     // fits | tight | no: on this computer alone
	HereNote string     `json:"hereNote"` // in words
	Verdict  string     `json:"verdict"`  // across all computers, like the store's
	Note     string     `json:"note"`
	Estimate []Estimate `json:"estimates,omitempty"`
}

// hereFit says whether a model of this size fits this computer's memory by itself.
func hereFit(info NodeInfo, held, size int64) (string, string) {
	info.Limits.Pedal, info.Limits.Enabled = 100, true
	cpu, gpu, unified := offer(info, held)
	room := cpu + gpu
	if unified {
		room = gpu
	}
	need := size + size/25 + perDeviceOverhead
	switch {
	case need <= room:
		return "fits", fmt.Sprintf("Fits this computer's memory: needs about %s of the %s it can make room for.", humanBytes(need), humanBytes(room))
	case size <= int64(info.RAMTotal)*9/10:
		return "tight", fmt.Sprintf("Needs about %s; this computer can spare %s right now. It can still run here, with part of it read from the disk (slower), or after closing other apps.", humanBytes(need), humanBytes(room))
	default:
		return "no", fmt.Sprintf("Too big for this computer alone (about %s, it has %s of memory). It may run pooled with your other computers.", humanBytes(size), humanBytes(int64(info.RAMTotal)))
	}
}

// Discover looks for models and engines on the main computer.
func (h *Host) Discover(deep bool) (map[string]any, error) {
	var res struct {
		Models  []FoundModel  `json:"models"`
		Engines []FoundEngine `json:"engines"`
		Dirs    []string      `json:"dirs"`
	}
	if err := h.call(h.id.FP, "discover", map[string]bool{"deep": deep}, &res, 4*time.Minute); err != nil {
		return nil, err
	}
	h.mu.Lock()
	self := h.nodes[h.id.FP]
	info, held := roomForOne(self.Info), self.Held
	if h.cluster.Phase != "running" {
		held = 0
	}
	h.mu.Unlock()
	files := make([]StoreFile, len(res.Models))
	for i, m := range res.Models {
		files[i] = StoreFile{File: m.Name, Size: m.Size}
	}
	h.judge(files)
	out := make([]FoundView, len(res.Models))
	for i, m := range res.Models {
		v := FoundView{FoundModel: m, Verdict: files[i].Verdict, Note: files[i].Note, Estimate: files[i].Estimates}
		if m.Error == "" {
			v.Here, v.HereNote = hereFit(info, held, m.Size)
		}
		out[i] = v
	}
	cfg := h.cfg.Get()
	engine := map[string]any{"pinned": engineTag, "choice": cfg.EnginePath}
	return map[string]any{"models": out, "engines": res.Engines, "dirs": res.Dirs, "engine": engine}, nil
}

// UseFound adds a found model to the list and, if asked, runs it on this computer.
func (h *Host) UseFound(path string, run bool, ctx int) (string, error) {
	var res struct {
		File string `json:"file"`
	}
	if err := h.call(h.id.FP, "use_found", map[string]string{"path": path}, &res, time.Minute); err != nil {
		return "", err
	}
	if run {
		solo := ""
		if h.cfg.Get().Single {
			solo = h.id.FP
		}
		if err := h.Start(ModelRef{Source: "local", Node: h.id.FP, File: res.File}, ctx, solo); err != nil {
			return res.File, err
		}
	}
	return res.File, nil
}

// touch records that the model was just used.
func (h *Host) touch() {
	h.mu.Lock()
	h.lastUse = time.Now()
	h.mu.Unlock()
}

// idleLoop lets go of the model after the chosen number of minutes without a request, so
// a computer used every day is not left with most of its memory taken. The next message
// loads it again (wake).
func (h *Host) idleLoop() {
	for {
		time.Sleep(30 * time.Second)
		mins := h.cfg.Get().IdleUnload
		if mins <= 0 {
			continue
		}
		h.mu.Lock()
		phase, model, last := h.cluster.Phase, h.cluster.Model, h.lastUse
		ctxSize, solo := h.lastCtx, h.lastSolo
		if last.IsZero() || h.cluster.Since.After(last) {
			last = h.cluster.Since
		}
		h.mu.Unlock()
		// Never while it is answering: a long answer or a long prompt can run past the idle
		// time, and no new request touches lastUse meanwhile.
		if phase != "running" || model == nil || h.inflight.Load() > 0 || time.Since(last) < time.Duration(mins)*time.Minute {
			continue
		}
		m := *model
		// Resting is set first, so a message that arrives while it stops waits and loads it
		// again instead of finding no model.
		h.mu.Lock()
		h.asleep, h.asleepCtx, h.asleepSolo = &m, ctxSize, solo
		h.mu.Unlock()
		h.Stop()
	}
}

// wake loads the model let go by idleLoop again and waits until it is ready. Of several
// requests arriving together, one starts it and the others wait for that start.
func (h *Host) wake(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		h.mu.Lock()
		m, ctxSize, solo, phase := h.asleep, h.asleepCtx, h.asleepSolo, h.cluster.Phase
		take := m != nil && (phase == "idle" || phase == "error")
		if take {
			h.asleep, h.waking = nil, true
		}
		waking := h.waking
		h.mu.Unlock()
		switch {
		case take:
			if err := h.Start(*m, ctxSize, solo); err != nil {
				h.mu.Lock()
				h.waking = false
				if h.asleep == nil {
					h.asleep = m // still resting: the next message tries again
				}
				h.mu.Unlock()
				return err
			}
		case m != nil && phase == "stopping":
			// It is being let go right now: wait, then load it again.
			if time.Now().After(deadline) {
				return errors.New("loading the model again took too long")
			}
			time.Sleep(500 * time.Millisecond)
			continue
		case !waking:
			return nil // awake (or nothing was resting)
		}
		break
	}
	defer func() {
		h.mu.Lock()
		h.waking = false
		h.mu.Unlock()
	}()
	for time.Now().Before(deadline) {
		h.mu.Lock()
		phase, msg := h.cluster.Phase, h.cluster.Error
		h.mu.Unlock()
		switch phase {
		case "running":
			return nil
		case "error":
			return errors.New(msg)
		case "idle":
			return errors.New("the model was stopped")
		}
		time.Sleep(time.Second)
	}
	return errors.New("loading the model again took too long")
}

// isModelRequest says whether a request to the chat address goes to the model itself.
func isModelRequest(r *http.Request) bool {
	p := r.URL.Path
	return strings.HasPrefix(p, "/v1/") || p == "/completion" || p == "/completions" || p == "/apply-template" || p == "/tokenize" || p == "/props" || p == "/chat/completions"
}
