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
	Bytes int64     `json:"bytes"`
	Until time.Time `json:"until"`
}

// guardWatch asks the computers holding part of the model, every few seconds while it starts
// or runs, whether their guard had to step in.
func (h *Host) guardWatch() {
	for {
		time.Sleep(3 * time.Second)
		h.mu.Lock()
		phase, plan := h.cluster.Phase, h.cluster.Plan
		var ids []string
		if (phase == "running" || phase == "starting") && plan != nil {
			// The computers in this start: each clears its guard's event when it is asked to
			// start, so an event there now is about this start.
			for _, id := range planIDs(plan) {
				if n := h.nodes[id]; n != nil && n.Online {
					ids = append(ids, id)
				}
			}
		}
		h.mu.Unlock()
		for _, id := range ids {
			var ev *GuardEvent
			if err := h.call(id, "guard", nil, &ev, 5*time.Second); err != nil || ev == nil || !h.newGuardEvent(id, ev) {
				continue
			}
			h.guardTripped(id, ev, plan)
			break
		}
	}
}

// planIDs is every computer a plan uses: the head and each one holding a share.
func planIDs(p *Plan) []string {
	ids := []string{p.Head}
	for _, s := range p.Shares {
		if s.ID != p.Head {
			ids = append(ids, s.ID)
		}
	}
	return ids
}

// newGuardEvent says whether this event from a computer has not been acted on yet. Events
// are told apart by the time the computer wrote on them, only ever compared with that same
// computer's earlier events.
func (h *Host) newGuardEvent(id string, ev *GuardEvent) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.guardSeen == nil {
		h.guardSeen = map[string]time.Time{}
	}
	if h.guardSeen[id].Equal(ev.At) {
		return false
	}
	h.guardSeen[id] = ev.At
	return true
}

func (h *Host) guardTripped(id string, ev *GuardEvent, plan *Plan) {
	h.learnCeiling(id, ev, plan)
	h.Stop()
	h.mu.Lock()
	h.cluster.Phase, h.cluster.Error = "error", h.guardWhy
	h.mu.Unlock()
}

// learnCeiling remembers how much a computer held when its guard stopped it, and says so
// in guardWhy.
func (h *Host) learnCeiling(id string, ev *GuardEvent, plan *Plan) {
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
	var ceil *memCeiling
	if held > 0 && plan.LayerBytes > 0 {
		// One layer less than what it could not hold, for six hours. It ran out holding a
		// single layer: it gets none (otherwise the next start gives it the same and it runs
		// out again).
		ceil = &memCeiling{Bytes: max(0, held-plan.LayerBytes), Until: time.Now().Add(6 * time.Hour)}
		if ceil.Bytes > 0 {
			next = fmt.Sprintf(" Next time Potluck gives %s at most %s (it ran out holding %s); start the model again to try.", name, humanBytes(ceil.Bytes), humanBytes(held))
		} else {
			next = fmt.Sprintf(" For the next six hours Potluck leaves %s out of this model (it ran out holding a single part of it).", name)
		}
	}
	h.guardWhy = fmt.Sprintf("%s: %s. Potluck stopped the model so %s would not freeze.%s", name, ev.Why, name, next)
	h.mu.Unlock()
	if ceil != nil {
		// Kept in the settings only (one copy, and it survives a restart).
		h.cfg.Update(func(cf *Config) {
			if cf.Ceilings == nil {
				cf.Ceilings = map[string]memCeiling{}
			}
			cf.Ceilings[id] = *ceil
		})
	}
}

// capByCeiling trims a computer's offer to what it held without running out (see above).
func (h *Host) capByCeiling(id string, cpu, gpu int64) (int64, int64) {
	if h.cfg == nil {
		return cpu, gpu
	}
	c, ok := h.cfg.Get().Ceilings[id]
	if !ok || time.Now().After(c.Until) || cpu+gpu <= c.Bytes {
		return cpu, gpu
	}
	if gpu > c.Bytes {
		return 0, c.Bytes
	}
	return c.Bytes - gpu, gpu
}
