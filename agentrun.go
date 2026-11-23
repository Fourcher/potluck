package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Agent mode: the chat page may ask this computer to run a command or a script for the
// model, after the user said yes to it in the page. Only the computer running Potluck can
// ask (never the network, whatever the sharing switch says), only from Potluck's own page,
// and nothing runs without the page asking first.

// RunReq is one command from the chat page.
type RunReq struct {
	Kind    string `json:"kind"`    // shell | applescript
	Command string `json:"command"` // the command line, or the script
	Dir     string `json:"dir"`     // folder to start in; "" = the agent folder
	Timeout int    `json:"timeout"` // seconds, default 120, at most 900
}

// RunRes is what came of it.
type RunRes struct {
	Exit     int     `json:"exit"`
	Output   string  `json:"output"` // what it printed (both streams, in order), shortened in the middle when long
	Dir      string  `json:"dir"`
	Seconds  float64 `json:"seconds"`
	TimedOut bool    `json:"timedOut,omitempty"`
}

// agentDir is where commands start unless told otherwise.
func (app *App) agentDir() string { return agentDirOf(app.cfg) }

func (h *Host) agentDir() string { return agentDirOf(h.cfg) }

func agentDirOf(cfg *ConfigStore) string {
	if cfg == nil {
		return homeDir()
	}
	if d := cfg.Get().AgentDir; d != "" {
		return d
	}
	return homeDir()
}

// limitedBuffer keeps the start and the end of a long output.
type limitedBuffer struct {
	mu         sync.Mutex
	head, tail []byte
	dropped    int
}

const outHead, outTail = 12 << 10, 20 << 10

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	if room := outHead - len(b.head); room > 0 {
		k := min(room, len(p))
		b.head = append(b.head, p[:k]...)
		p = p[k:]
	}
	b.tail = append(b.tail, p...)
	if over := len(b.tail) - outTail; over > 0 {
		b.dropped += over
		b.tail = append([]byte(nil), b.tail[over:]...)
	}
	return n, nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.dropped == 0 {
		return string(b.head) + string(b.tail)
	}
	return string(b.head) + "\n… (" + humanBytes(int64(b.dropped)) + " of output left out) …\n" + string(b.tail)
}

// runCommand runs one agent command and waits for it, or for ctx to end.
func runCommand(ctx context.Context, in RunReq, defDir string) (*RunRes, error) {
	if strings.TrimSpace(in.Command) == "" {
		return nil, errors.New("no command given")
	}
	dir := in.Dir
	if dir == "" {
		dir = defDir
	} else if !filepath.IsAbs(dir) {
		dir = filepath.Join(defDir, dir)
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil, errors.New("there is no folder " + dir)
	}
	t := in.Timeout
	if t <= 0 {
		t = 120
	}
	if t > 900 {
		t = 900
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(t)*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	switch in.Kind {
	case "", "shell":
		switch runtime.GOOS {
		case "windows":
			cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", in.Command)
		case "darwin":
			cmd = exec.CommandContext(ctx, "/bin/zsh", "-c", in.Command)
		default:
			sh := "/bin/sh"
			if _, err := os.Stat("/bin/bash"); err == nil {
				sh = "/bin/bash"
			}
			cmd = exec.CommandContext(ctx, sh, "-c", in.Command)
		}
	case "applescript":
		if runtime.GOOS != "darwin" {
			return nil, errors.New("AppleScript only works on a Mac")
		}
		cmd = exec.CommandContext(ctx, "osascript", "-e", in.Command)
	default:
		return nil, errors.New("unknown kind of command")
	}
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "POTLUCK_AGENT=1", "PAGER=cat", "GIT_PAGER=cat", "TERM=dumb")
	if runtime.GOOS == "darwin" && !strings.Contains(os.Getenv("PATH"), "/opt/homebrew/bin") {
		cmd.Env = append(cmd.Env, "PATH=/opt/homebrew/bin:/usr/local/bin:"+os.Getenv("PATH"))
	}
	ownGroup(cmd)
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			killTree(cmd.Process.Pid)
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = 3 * time.Second
	var out limitedBuffer
	cmd.Stdout, cmd.Stderr = &out, &out
	start := time.Now()
	err := cmd.Run()
	res := &RunRes{Output: out.String(), Dir: dir, Seconds: time.Since(start).Seconds()}
	if ctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
	}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.Exit = ee.ExitCode()
	case res.TimedOut:
		res.Exit = -1
	default:
		return nil, err
	}
	return res, nil
}

// sameOrigin: the request comes from Potluck's own chat page (or from a program, which
// sends no Origin), not from another website open in the browser.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return r.Header.Get("Sec-Fetch-Site") == "" || r.Header.Get("Sec-Fetch-Site") == "same-origin"
	}
	for _, ok := range []string{"http://127.0.0.1:", "http://localhost:", "http://[::1]:"} {
		if o == ok+itoa(portChat) {
			return true
		}
	}
	return false
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// handleRun serves /potluck/run for the chat page's agent mode.
func handleRun(w http.ResponseWriter, r *http.Request, defDir string) {
	if !chatOwner(r) || r.Header.Get("X-Potluck") != "1" || !sameOrigin(r) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "commands can only be run from the chat page on the computer running Potluck"})
		return
	}
	var in RunReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		fail(w, err)
		return
	}
	res, err := runCommand(r.Context(), in, defDir)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, res)
}
