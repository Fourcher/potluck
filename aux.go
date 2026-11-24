package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// The helper's engine: a second, small llama-server on a computer, separate from the main
// model's engine, so the Potluck helper can answer while a big model loads, runs or is
// swapped. It runs on the processor only (no graphics memory taken from the main model, and
// none of the Metal out-of-memory risk on Macs), at low priority, with few threads.

type StartAuxReq struct {
	File string `json:"file"`
	Ctx  int    `json:"ctx"`
}

type AuxStatus struct {
	Running bool   `json:"running"`
	Ready   bool   `json:"ready"`
	File    string `json:"file"`
	Log     string `json:"log,omitempty"`
}

func (a *Agent) startAux(in StartAuxReq) (any, error) {
	a.mu.Lock()
	rt, old := a.rtDir, a.aux
	a.mu.Unlock()
	if rt == "" {
		return nil, errors.New("the AI engine is not installed yet")
	}
	old.Stop()
	model := a.modelPath(in.File)
	if _, err := os.Stat(model); err != nil {
		return nil, errors.New("the helper's model is not on this computer")
	}
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	if in.Ctx <= 0 {
		in.Ctx = 16384
	}
	threads := max(2, min(4, a.Info().Cores/2))
	args := []string{"-m", model, "-c", strconv.Itoa(in.Ctx), "-np", "1", "-t", strconv.Itoa(threads),
		"--host", "127.0.0.1", "--port", strconv.Itoa(port), "-ngl", "0", "--jinja", "--no-webui",
		"--alias", "potluck-helper"}
	if base := strings.ToLower(rt); strings.Contains(base, "cuda") || strings.Contains(base, "vulkan") {
		args = append(args, "-dev", "none")
	}
	p, err := a.startProc("helper", rt, findBin(rt, "llama-server"), args, true, true)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.aux, a.auxPort, a.auxFile = p, port, in.File
	a.mu.Unlock()
	return map[string]int{"port": port}, nil
}

func (a *Agent) stopAux() {
	a.mu.Lock()
	p := a.aux
	a.aux, a.auxPort, a.auxFile = nil, 0, ""
	a.mu.Unlock()
	p.Stop()
}

func (a *Agent) auxStatus() AuxStatus {
	a.mu.Lock()
	p, port, file := a.aux, a.auxPort, a.auxFile
	a.mu.Unlock()
	st := AuxStatus{Running: p.Running(), File: file}
	if st.Running {
		res, err := (&http.Client{Timeout: 2 * time.Second}).Get("http://127.0.0.1:" + strconv.Itoa(port) + "/health")
		if err == nil {
			st.Ready = res.StatusCode == 200
			res.Body.Close()
		}
	} else if p != nil {
		st.Log = lastLogLine(tailFile(p.logPath, 4000))
	}
	return st
}

// handleAux answers the main computer's requests about the helper's engine.
func (a *Agent) handleAux(method string, raw json.RawMessage) (any, bool, error) {
	switch method {
	case "start_aux":
		var in StartAuxReq
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, true, err
		}
		r, err := a.startAux(in)
		return r, true, err
	case "stop_aux":
		a.stopAux()
		return nil, true, nil
	case "aux_status":
		return a.auxStatus(), true, nil
	}
	return nil, false, nil
}
