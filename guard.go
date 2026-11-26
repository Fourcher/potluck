package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// The guard keeps a model that turned out too big from freezing a computer.
//
// It sets no limit on how much a model may use. It watches the operating system's own signs
// that memory has truly run out (Linux: almost nothing available and tasks stalled waiting for
// memory; macOS: the system's "critical" memory pressure level; Windows: the commit limit
// nearly reached) and the engine's log for out-of-memory errors. When memory stays exhausted
// for a few seconds, or the engine reports it could not allocate, the guard stops this
// computer's engine at once, before the system locks up. The main computer then stops the
// model everywhere, says why, and plans around what this computer managed to hold.
//
// A frozen computer cannot rescue itself, so the guard acts while it still can: it checks
// every second, its own memory use is tiny, and engines run at low priority so the guard,
// the desktop and remote logins keep getting time when memory gets tight.

// guardSustain is how many one-second checks in a row must show memory exhausted. Short
// spikes (a big allocation the system satisfies by reclaiming cache) are normal and ignored.
const guardSustain = 4

// oomInLog matches what the engines write when an allocation fails: Metal (macOS), CUDA,
// Vulkan, the processor backend and C++ itself.
var oomInLog = regexp.MustCompile(`(?i)insufficient memory|kIOGPUCommandBufferCallbackErrorOutOfMemory|cudaMalloc failed|CUDA error: out of memory|ErrorOutOfDeviceMemory|failed to allocate .*buffer|std::bad_alloc|out of memory`)

// GuardEvent is what the guard did, reported to the main computer with the agent's status.
type GuardEvent struct {
	At  time.Time `json:"at"`
	Why string    `json:"why"`
}

// guardScan reads the engines' logs for out-of-memory lines.
type guardScan struct {
	// How far each engine's log has been read, by engine (not by file name: a new engine
	// writes a new log at the same path, and must be read from its start).
	offsets map[*proc]int64
	// Engines whose log was read once more after they ended: an engine that runs out of
	// memory often exits before the next check, and its last lines are the reason.
	readAfterEnd map[*proc]bool
}

func newGuardScan() *guardScan {
	return &guardScan{offsets: map[*proc]int64{}, readAfterEnd: map[*proc]bool{}}
}

// oom returns a new out-of-memory line from p's log: while it runs, and once more after it
// has ended.
func (g *guardScan) oom(p *proc) string {
	if p == nil {
		return ""
	}
	if !p.Running() {
		if g.readAfterEnd[p] {
			return ""
		}
		g.readAfterEnd[p] = true
	} else {
		delete(g.readAfterEnd, p) // running again (a restart): read it again when it ends
	}
	return newOOMLine(p, g.offsets)
}

// forget drops engines that are gone.
func (g *guardScan) forget(live map[*proc]bool) {
	for p := range g.offsets {
		if !live[p] {
			delete(g.offsets, p)
		}
	}
	for p := range g.readAfterEnd {
		if !live[p] {
			delete(g.readAfterEnd, p)
		}
	}
}

func (a *Agent) guardLoop() {
	bad := 0
	g := newGuardScan()
	for {
		time.Sleep(time.Second)
		a.mu.Lock()
		rpc, head := a.rpc, a.head
		aux := map[string]*proc{}
		for name, e := range a.aux {
			aux[name] = e.proc
		}
		a.mu.Unlock()
		live := map[*proc]bool{rpc: true, head: true}
		for _, p := range aux {
			live[p] = true
		}
		g.forget(live)
		// 1. An engine says an allocation failed: stop it now. On a Mac a graphics chip that
		// runs out of memory can take the window server down with it if it keeps trying. One
		// that already ended of it is reported the same way, so the main computer learns
		// how much this computer could hold.
		for _, p := range []*proc{rpc, head} {
			if line := g.oom(p); line != "" {
				a.guardStop("the AI engine ran out of memory (" + line + ")")
				bad = 0
				break
			}
		}
		// The extra engines (the Potluck helper's model, crew members) are watched the same
		// way; one that runs out is stopped on its own, leaving the main model alone.
		for name, p := range aux {
			if line := g.oom(p); line != "" {
				a.guardStopAux(name, "it ran out of memory ("+line+")")
			}
		}
		auxUp := false
		for _, p := range aux {
			auxUp = auxUp || p.Running()
		}
		if !rpc.Running() && !head.Running() && !auxUp {
			bad = 0
			continue
		}
		// 2. The system as a whole has run out of memory and stays out: stop the main model's
		// engines, or the extra engines when they are all that is running.
		if critical, detail := memoryCritical(); critical {
			bad++
			if bad >= guardSustain {
				if rpc.Running() || head.Running() {
					a.guardStop("this computer ran out of memory (" + detail + ")")
				} else {
					for name, p := range aux {
						if p.Running() {
							a.guardStopAux(name, "this computer ran out of memory ("+detail+")")
						}
					}
				}
				bad = 0
			}
		} else {
			bad = 0
		}
	}
}

// guardStopAux stops one extra engine and notes why in its log and the guard's.
func (a *Agent) guardStopAux(name, why string) {
	a.mu.Lock()
	e := a.aux[name]
	if e != nil {
		e.guarded = true
	}
	a.mu.Unlock()
	if e != nil {
		if f, err := os.OpenFile(e.proc.logPath, os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintf(f, "\n[potluck] the guard stopped this engine: %s\n", why)
			f.Close()
		}
	}
	if e != nil {
		// Stopped but kept, so its status says it is down and why (its log's last line).
		e.proc.Stop()
	}
	_ = os.MkdirAll(filepath.Join(a.dir, "logs"), 0o755)
	if f, err := os.OpenFile(filepath.Join(a.dir, "logs", "guard.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		fmt.Fprintf(f, "%s stopped the %s engine: %s\n", time.Now().Format(time.RFC3339), name, why)
		f.Close()
	}
}

// newOOMLine returns the first out-of-memory line written to the log since the last look.
func newOOMLine(p *proc, offsets map[*proc]int64) string {
	f, err := os.Open(p.logPath)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	from := offsets[p]
	if st.Size() < from {
		from = 0 // a new log
	}
	if st.Size()-from > 1<<20 {
		from = st.Size() - 1<<20
	}
	buf := make([]byte, st.Size()-from)
	n, _ := f.ReadAt(buf, from)
	offsets[p] = from + int64(n)
	for _, line := range strings.Split(string(buf[:n]), "\n") {
		// Not the guard's own note of why it stopped the engine.
		if oomInLog.MatchString(line) && !strings.HasPrefix(strings.TrimSpace(line), "[potluck]") {
			line = strings.TrimSpace(line)
			if len(line) > 160 {
				line = line[:160]
			}
			return line
		}
	}
	return ""
}

// guardStop ends this computer's engines and remembers why.
func (a *Agent) guardStop(why string) {
	ev := &GuardEvent{At: time.Now(), Why: why}
	a.mu.Lock()
	a.guard = ev
	a.mu.Unlock()
	a.Stop()
	_ = os.MkdirAll(filepath.Join(a.dir, "logs"), 0o755)
	if f, err := os.OpenFile(filepath.Join(a.dir, "logs", "guard.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		fmt.Fprintf(f, "%s stopped the engine: %s\n", ev.At.Format(time.RFC3339), why)
		f.Close()
	}
}

// clearGuard forgets the guard's last event when a new start begins here, so what the main
// computer reads afterwards is about this start, never an earlier one. (The two computers'
// clocks are never compared: they may differ by minutes.)
func (a *Agent) clearGuard() {
	a.mu.Lock()
	a.guard = nil
	a.mu.Unlock()
}
