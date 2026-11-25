package main

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strconv"
	"time"
)

// Measuring a start before making it. Potluck's plan estimates what each device needs; the
// estimates missed the context cache and working memory of layers on a graphics card, which
// grow with the conversation's size and the prompt batch (measured on a 4 GB card, Inkling 2
// at 32k: 5.6 GB needed, of which 3.4 GB working memory, against 3.3 GB free). The engine's
// own dry run (llama-fit-params with the exact start arguments, nothing loaded, about a
// second) reports what it would really allocate on each device, so the plan is checked
// against that before the model loads.
//
// The engine's own fitting is not used to choose a layout: it does not know that a looped
// model (Inkling 2) runs some layers more than once, and names layers the file does not have.

// DevMem is one device in the engine's memory report, in MiB.
type DevMem struct {
	Name    string `json:"name"`
	Total   int64  `json:"total,omitempty"`
	Free    int64  `json:"free,omitempty"`
	Self    int64  `json:"self"`
	Model   int64  `json:"model"`
	Context int64  `json:"context"`
	Compute int64  `json:"compute"`
}

// MeasureRes is the engine's report for one set of start arguments.
type MeasureRes struct {
	Devices []DevMem `json:"devices"` // graphics devices, in the engine's order
	Host    DevMem   `json:"host"`    // ordinary memory
}

var (
	ansi = regexp.MustCompile("\x1b\\[[0-9;]*m")
	// |   - CUDA0 (GTX 1050 Ti) |  4095 = 3198 + ( 5634 =  1008 +    1267 +    3358) +   -4737 |
	memDevLine = regexp.MustCompile(`memory_breakdown_print: \|\s+- (.+?)\s+\|\s+(\d+) = (\d+) \+ \(\s*(\d+) =\s+(\d+) \+\s+(\d+) \+\s+(\d+)\)`)
	// |   - Host                |                 21559 = 21381 +       0 +     177                |
	memHostLine = regexp.MustCompile(`memory_breakdown_print: \|\s+- Host\s+\|\s+(\d+) =\s+(\d+) \+\s+(\d+) \+\s+(\d+)`)
)

// parseMemReport reads the first memory report in the engine's log (the one for the
// arguments as given; with fitting off there is only one).
func parseMemReport(log string) (MeasureRes, bool) {
	var r MeasureRes
	n := func(s string) int64 { v, _ := strconv.ParseInt(s, 10, 64); return v }
	seenHost := false
	for _, line := range regexp.MustCompile(`\r?\n`).Split(ansi.ReplaceAllString(log, ""), -1) {
		if m := memHostLine.FindStringSubmatch(line); m != nil {
			r.Host = DevMem{Name: "Host", Self: n(m[1]), Model: n(m[2]), Context: n(m[3]), Compute: n(m[4])}
			seenHost = true
			break // the report ends with the host line
		}
		if m := memDevLine.FindStringSubmatch(line); m != nil {
			r.Devices = append(r.Devices, DevMem{Name: m[1], Total: n(m[2]), Free: n(m[3]), Self: n(m[4]),
				Model: n(m[5]), Context: n(m[6]), Compute: n(m[7])})
		}
	}
	return r, seenHost
}

// measureHead runs the engine's dry run for a start on this computer alone.
func (a *Agent) measureHead(in StartHeadReq) (any, error) {
	if len(in.Workers) > 0 || in.Stream {
		return nil, errors.New("only a model on one computer can be measured")
	}
	a.mu.Lock()
	rt, gpuDev := a.rtDir, a.gpuDev
	a.mu.Unlock()
	if rt == "" {
		return nil, errors.New("the AI engine is not installed yet")
	}
	bin := findBin(rt, "llama-fit-params")
	if bin == "" {
		return nil, errors.New("this AI engine has no measuring tool")
	}
	model := a.modelPath(in.File)
	if _, err := os.Stat(model); err != nil {
		return nil, errors.New("the model file is not on this computer")
	}
	args := a.headArgs(in, model, rt, gpuDev, nil, threadsFor(a.Info(), in.AllThreads))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := a.engineCmd(bin, args...)
	cmd.Stdout = nil
	out := &capBuf{max: 4 << 20}
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() { <-ctx.Done(); _ = cmd.Process.Kill() }()
	_ = cmd.Wait() // the tool can end with an error after reporting (it cannot fit what was asked)
	res, ok := parseMemReport(out.String())
	if !ok {
		return nil, errors.New("the engine's dry run gave no memory report: " + lastLogLine(out.String()))
	}
	return res, nil
}

// capBuf keeps the first max bytes written to it.
type capBuf struct {
	b   []byte
	max int
}

func (c *capBuf) Write(p []byte) (int, error) {
	if room := c.max - len(c.b); room > 0 {
		c.b = append(c.b, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func (c *capBuf) String() string { return string(c.b) }
