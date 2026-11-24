package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Extra engines on a computer, separate from the main model's engine: the Potluck helper's
// small model ("helper") and crew members (crew.go), each in its own named slot with its own
// llama-server, so several models can run on several computers at once.
//
// The helper runs on the processor only (no graphics memory taken from the main model, and
// none of the Metal out-of-memory risk on Macs), at low priority, with few threads. A crew
// member may use the graphics card when the plan says so (NGL).

type StartAuxReq struct {
	Name    string `json:"name,omitempty"` // "" = "helper"
	File    string `json:"file"`
	Ctx     int    `json:"ctx"`
	NGL     int    `json:"ngl,omitempty"`     // layers on the graphics card; 0 = processor only
	Threads int    `json:"threads,omitempty"` // 0 = a few (helper) or most (crew)
}

type AuxStatus struct {
	Name    string `json:"name,omitempty"`
	Running bool   `json:"running"`
	Ready   bool   `json:"ready"`
	File    string `json:"file"`
	Log     string `json:"log,omitempty"`
}

type auxEngine struct {
	proc *proc
	port int
	file string
}

var auxNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,40}$`)

func auxName(n string) string {
	if n == "" {
		return "helper"
	}
	return n
}

func (a *Agent) startAux(in StartAuxReq) (any, error) {
	name := auxName(in.Name)
	if !auxNameRe.MatchString(name) {
		return nil, errors.New("a slot name is lowercase letters, digits and dashes")
	}
	a.mu.Lock()
	rt := a.rtDir
	old := a.aux[name]
	a.mu.Unlock()
	if rt == "" {
		return nil, errors.New("the AI engine is not installed yet")
	}
	if old != nil {
		old.proc.Stop()
	}
	model := a.modelPath(in.File)
	if _, err := os.Stat(model); err != nil {
		return nil, errors.New("that model is not on this computer")
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	if in.Ctx <= 0 {
		in.Ctx = 16384
	}
	cores := a.Info().Cores
	threads := in.Threads
	if threads <= 0 {
		if name == "helper" {
			threads = max(2, min(4, cores/2))
		} else {
			threads = max(2, cores-1)
		}
	}
	args := []string{"-m", model, "-c", strconv.Itoa(in.Ctx), "-np", "1", "-t", strconv.Itoa(threads),
		"--host", "127.0.0.1", "--port", strconv.Itoa(port), "-ngl", strconv.Itoa(in.NGL), "--jinja", "--no-webui",
		"--alias", name}
	if base := strings.ToLower(rt); in.NGL == 0 && (strings.Contains(base, "cuda") || strings.Contains(base, "vulkan")) {
		args = append(args, "-dev", "none")
	}
	p, err := a.startProc("aux-"+name, rt, findBin(rt, "llama-server"), args, true, true)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	if a.aux == nil {
		a.aux = map[string]*auxEngine{}
	}
	a.aux[name] = &auxEngine{proc: p, port: port, file: in.File}
	a.mu.Unlock()
	return map[string]int{"port": port}, nil
}

func (a *Agent) stopAux(name string) {
	name = auxName(name)
	a.mu.Lock()
	e := a.aux[name]
	delete(a.aux, name)
	a.mu.Unlock()
	if e != nil {
		e.proc.Stop()
	}
}

func (a *Agent) stopAllAux() {
	a.mu.Lock()
	all := a.aux
	a.aux = nil
	a.mu.Unlock()
	for _, e := range all {
		e.proc.Stop()
	}
}

func (a *Agent) auxStatus(name string) AuxStatus {
	name = auxName(name)
	a.mu.Lock()
	e := a.aux[name]
	a.mu.Unlock()
	st := AuxStatus{Name: name}
	if e == nil {
		return st
	}
	st.Running, st.File = e.proc.Running(), e.file
	if st.Running {
		res, err := (&http.Client{Timeout: 2 * time.Second}).Get("http://127.0.0.1:" + strconv.Itoa(e.port) + "/health")
		if err == nil {
			st.Ready = res.StatusCode == 200
			res.Body.Close()
		}
	} else {
		st.Log = lastLogLine(tailFile(e.proc.logPath, 4000))
	}
	return st
}

// auxPort is where a named slot's engine answers, for tunnels ("aux" is the helper).
func (a *Agent) auxPortFor(target string) (int, bool) {
	name := "helper"
	if rest, ok := strings.CutPrefix(target, "aux:"); ok {
		name = rest
	} else if target != "aux" {
		return 0, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if e := a.aux[name]; e != nil && e.proc.Running() {
		return e.port, true
	}
	return 0, false
}

// handleAux answers the main computer's requests about the extra engines.
func (a *Agent) handleAux(method string, raw json.RawMessage) (any, bool, error) {
	var in struct {
		Name string `json:"name"`
	}
	switch method {
	case "start_aux":
		var req StartAuxReq
		if err := json.Unmarshal(raw, &req); err != nil {
			return nil, true, err
		}
		r, err := a.startAux(req)
		return r, true, err
	case "stop_aux":
		_ = json.Unmarshal(raw, &in)
		a.stopAux(in.Name)
		return nil, true, nil
	case "aux_status":
		_ = json.Unmarshal(raw, &in)
		return a.auxStatus(in.Name), true, nil
	}
	return nil, false, nil
}
