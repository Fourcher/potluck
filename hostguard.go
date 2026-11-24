package main

import (
	"fmt"
	"time"
)

// What the main computer does when a computer's guard (guard.go) stops its engine: stop the
// model everywhere, tell the person plainly what happened, and remember how much that
// computer managed to hold so the next start puts less on it. Nothing is limited before a
// computer has actually run out: the ceiling comes from what happened, not from a guess.

// memCeiling is the most a computer should be given for a while after it ran out of memory.
type memCeiling struct {
	Bytes int64
	Until time.Time
}

// guardWatch asks the computers holding part of the model, every few seconds while it starts
// or runs, whether their guard had to step in.
func (h *Host) guardWatch() {
	for {
		time.Sleep(3 * time.Second)
		h.mu.Lock()
		phase, since, plan := h.cluster.Phase, h.cluster.Since, h.cluster.Plan
		var ids []string
		if phase == "running" || phase == "starting" {
			for id, n := range h.nodes {
				if n.Online {
					ids = append(ids, id)
				}
			}
		}
		h.mu.Unlock()
		for _, id := range ids {
			var ev *GuardEvent
			if err := h.call(id, "guard", nil, &ev, 5*time.Second); err != nil || ev == nil || ev.At.Before(since) {
				continue
			}
			h.guardTripped(id, ev, plan)
			break
		}
	}
}

func (h *Host) guardTripped(id string, ev *GuardEvent, plan *Plan) {
	h.mu.Lock()
	name := "A computer"
	if n := h.nodes[id]; n != nil {
		name = n.Info.Name
	}
	var held int64
	if plan != nil {
		for _, s := range plan.Shares {
			if s.ID == id {
				held = s.Bytes
			}
		}
	}
	next := ""
	if held > 0 && plan.LayerBytes > 0 && held > plan.LayerBytes {
		if h.memCeil == nil {
			h.memCeil = map[string]memCeiling{}
		}
		ceil := held - plan.LayerBytes // one layer less than what it could not hold
		h.memCeil[id] = memCeiling{Bytes: ceil, Until: time.Now().Add(6 * time.Hour)}
		next = fmt.Sprintf(" Next time Potluck gives %s at most %s (it ran out holding %s); start the model again to try.", name, humanBytes(ceil), humanBytes(held))
	}
	h.guardWhy = fmt.Sprintf("%s: %s. Potluck stopped the model so %s would not freeze.%s", name, ev.Why, name, next)
	h.mu.Unlock()
	h.Stop()
	h.mu.Lock()
	h.cluster.Phase, h.cluster.Error = "error", h.guardWhy
	h.mu.Unlock()
}

// capByCeiling trims a computer's offer to what it held without running out (see above).
func (h *Host) capByCeiling(id string, cpu, gpu int64) (int64, int64) {
	c, ok := h.memCeil[id]
	if !ok || time.Now().After(c.Until) || cpu+gpu <= c.Bytes {
		return cpu, gpu
	}
	if gpu > c.Bytes {
		return 0, c.Bytes
	}
	return c.Bytes - gpu, gpu
}
