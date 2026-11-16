package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config is everything a computer remembers between runs.
type Config struct {
	Role   string `json:"role"` // "" (not chosen yet) | main | helper
	Name   string `json:"name"`
	Limits Limits `json:"limits"`
	// helper: the main computer it belongs to
	MainFP   string   `json:"mainFP,omitempty"`
	MainAddr string   `json:"mainAddr,omitempty"`
	MainAlts []string `json:"mainAlts,omitempty"` // its other addresses, tried when the usual one goes quiet
	MainName string   `json:"mainName,omitempty"`
	// main: the helpers it trusts and the secret that USB kits / join links carry
	Trusted    map[string]string `json:"trusted,omitempty"` // fingerprint -> name
	JoinSecret string            `json:"joinSecret,omitempty"`
	EngineTag  string            `json:"engineTag,omitempty"`
	HeadChoice string            `json:"headChoice,omitempty"` // "" = automatic
	LastModel  *ModelRef         `json:"lastModel,omitempty"`
}

// ConfigStore saves Config to disk on every change.
type ConfigStore struct {
	mu   sync.Mutex
	path string
	c    Config
}

func loadConfig(dir string) *ConfigStore {
	s := &ConfigStore{path: filepath.Join(dir, "config.json")}
	if b, err := os.ReadFile(s.path); err == nil {
		_ = json.Unmarshal(b, &s.c)
	}
	if s.c.Trusted == nil {
		s.c.Trusted = map[string]string{}
	}
	return s
}

func (s *ConfigStore) Get() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.c
	c.Trusted = map[string]string{}
	for k, v := range s.c.Trusted {
		c.Trusted[k] = v
	}
	return c
}

func (s *ConfigStore) Update(fn func(c *Config)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.c)
	b, _ := json.MarshalIndent(s.c, "", "  ")
	tmp := s.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, s.path)
	}
}

// Agent does the actual work on one computer: installs the engine, downloads, runs it.
// The main computer drives it through Handle, locally or over the network.
type Agent struct {
	dir string
	id  *Identity
	cfg *ConfigStore

	mu        sync.Mutex
	info      NodeInfo
	rtDir     string
	rpc, head *proc
	rpcPort   int
	apiPort   int
	listeners []net.Listener
	jobCancel context.CancelFunc
	status    string
	done      int64
	total     int64

	// set by whoever owns the agent
	dialTunnel func(to, target string) (net.Conn, error)
	notify     func(method string, data any)
}

func newAgent(dir string, id *Identity, cfg *ConfigStore) *Agent {
	a := &Agent{dir: dir, id: id, cfg: cfg}
	a.info = staticInfo(dir)
	a.info.ID = id.FP
	return a
}

// Info returns a fresh snapshot of this computer.
func (a *Agent) Info() NodeInfo {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.info.refresh(a.dir)
	c := a.cfg.Get()
	if c.Name != "" {
		a.info.Name = c.Name
	}
	a.info.Limits = c.Limits
	return a.info
}

// ProgressNote is sent to the main computer while a long job runs.
type ProgressNote struct {
	Msg   string `json:"msg"`
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
}

func (a *Agent) note(msg string, done, total int64) {
	a.mu.Lock()
	a.status, a.done, a.total = msg, done, total
	n := a.notify
	a.mu.Unlock()
	if n != nil {
		n("progress", ProgressNote{msg, done, total})
	}
}

func (a *Agent) jobCtx() context.Context {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.jobCancel != nil {
		a.jobCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.jobCancel = cancel
	return ctx
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// threadsFor turns the pedal into a number of CPU threads.
func threadsFor(cores, pedal int) int {
	t := int(math.Round(float64(cores) * float64(pedal) / 100))
	if t < 1 {
		t = 1
	}
	return t
}

// Requests the main computer can make.
type (
	FetchModelReq struct {
		URL    string `json:"url"`
		File   string `json:"file"`
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	}
	SeedReq struct {
		URL   string     `json:"url"`
		Items []SeedItem `json:"items"`
	}
	StartHeadReq struct {
		File    string   `json:"file"`
		Workers []string `json:"workers"` // node ids, in --rpc order
		NGL     int      `json:"ngl"`
		Split   string   `json:"split"`
		Ctx     int      `json:"ctx"`
	}
	AgentStatus struct {
		RPC     bool   `json:"rpc"`
		Head    bool   `json:"head"`
		Ready   bool   `json:"ready"`
		Msg     string `json:"msg"`
		Done    int64  `json:"done"`
		Total   int64  `json:"total"`
		HeadLog string `json:"headLog,omitempty"`
	}
)

// Handle runs one request.
func (a *Agent) Handle(method string, raw json.RawMessage) (any, error) {
	switch method {
	case "inventory":
		return a.Info(), nil

	case "set_limits":
		var in struct {
			Limits Limits `json:"limits"`
			Name   string `json:"name"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		if in.Limits.Pedal < 0 {
			in.Limits.Pedal = 0
		}
		if in.Limits.Pedal > 100 {
			in.Limits.Pedal = 100
		}
		a.cfg.Update(func(c *Config) {
			c.Limits = in.Limits
			if strings.TrimSpace(in.Name) != "" {
				c.Name = strings.TrimSpace(in.Name)
			}
		})
		return a.Info(), nil

	case "ensure_runtime":
		var spec RuntimeSpec
		if err := json.Unmarshal(raw, &spec); err != nil {
			return nil, err
		}
		dir, err := a.ensureRuntime(a.jobCtx(), spec, func(s string) { a.note(s, 0, 0) })
		if err != nil {
			return nil, err
		}
		a.mu.Lock()
		a.rtDir = dir
		a.mu.Unlock()
		a.note("AI engine ready", 0, 0)
		return map[string]string{"dir": dir}, nil

	case "fetch_model":
		var in FetchModelReq
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		dest := filepath.Join(a.dir, "models", filepath.Base(in.File))
		if free := diskFree(a.dir); free > 0 && int64(free) < in.Size+(2<<30) {
			if st, err := os.Stat(dest + ".part"); err != nil || st.Size() != in.Size {
				if _, err := os.Stat(dest + ".ok"); err != nil {
					return nil, fmt.Errorf("not enough free disk space: the model needs %s and %s is free", humanBytes(in.Size), humanBytes(int64(free)))
				}
			}
		}
		err := downloadFile(a.jobCtx(), in.URL, dest, in.Size, in.SHA256, 24, func(d, t int64, s string) {
			a.note("Model file: "+s, d, t)
		})
		if err != nil {
			return nil, err
		}
		a.note("Model file ready", in.Size, in.Size)
		return map[string]string{"path": dest}, nil

	case "seed":
		var in SeedReq
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		err := seedCache(a.jobCtx(), in.URL, filepath.Join(a.dir, "cache", "rpc"), in.Items, 8, func(d, t int64, s string) {
			a.note("Its share of the model: downloading", d, t)
		})
		if err != nil {
			return nil, err
		}
		a.note("Its share of the model is ready", 0, 0)
		return nil, nil

	case "start_rpc":
		return a.startRPC()

	case "start_head":
		var in StartHeadReq
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return a.startHead(in)

	case "status":
		return a.Status(), nil

	case "stop":
		a.Stop()
		return nil, nil
	}
	return nil, fmt.Errorf("unknown request %q", method)
}

func (a *Agent) startRPC() (any, error) {
	a.mu.Lock()
	rt, old := a.rtDir, a.rpc
	a.mu.Unlock()
	if rt == "" {
		return nil, errors.New("the AI engine is not installed yet")
	}
	old.Stop()
	bin := findBin(rt, "ggml-rpc-server", "rpc-server")
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	info := a.Info()
	threads := threadsFor(info.Cores, info.Limits.Pedal)
	_ = os.MkdirAll(filepath.Join(a.dir, "cache", "rpc"), 0o755)
	// Bound to this computer only. Other computers reach it through the encrypted tunnel,
	// because the worker protocol itself has no password.
	args := []string{"-H", "127.0.0.1", "-p", strconv.Itoa(port), "-c", "-t", strconv.Itoa(threads)}
	p, err := a.startProc("worker", rt, bin, args, true, info.Limits.Pedal < 100)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.rpc, a.rpcPort = p, port
	a.mu.Unlock()
	// Give it a moment to open its port.
	for i := 0; i < 50; i++ {
		if c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 300*time.Millisecond); err == nil {
			time.Sleep(300 * time.Millisecond) // hanging up at once makes the macOS worker quit
			c.Close()
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	a.note("Helper engine running", 0, 0)
	return map[string]int{"port": port, "threads": threads}, nil
}

func (a *Agent) startHead(in StartHeadReq) (any, error) {
	a.mu.Lock()
	rt, old := a.rtDir, a.head
	oldL := a.listeners
	a.listeners = nil
	a.mu.Unlock()
	if rt == "" {
		return nil, errors.New("the AI engine is not installed yet")
	}
	old.Stop()
	for _, l := range oldL {
		l.Close()
	}
	model := filepath.Join(a.dir, "models", filepath.Base(in.File))
	if _, err := os.Stat(model); err != nil {
		return nil, errors.New("the model file is not on this computer")
	}
	// One local port per helper. The engine connects to these; each connection is carried
	// to that helper through the main computer's encrypted link.
	var rpcList []string
	var listeners []net.Listener
	for _, w := range in.Workers {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		listeners = append(listeners, l)
		rpcList = append(rpcList, "127.0.0.1:"+strconv.Itoa(l.Addr().(*net.TCPAddr).Port))
		go func(l net.Listener, w string) {
			for {
				c, err := l.Accept()
				if err != nil {
					return
				}
				go func() {
					t, err := a.dialTunnel(w, "rpc")
					if err != nil {
						c.Close()
						return
					}
					pipe(c, t)
				}()
			}
		}(l, w)
	}
	api, err := freePort()
	if err != nil {
		return nil, err
	}
	info := a.Info()
	threads := threadsFor(info.Cores, info.Limits.Pedal)
	if in.Ctx <= 0 {
		in.Ctx = 4096
	}
	args := []string{"-m", model, "-c", strconv.Itoa(in.Ctx), "-np", "1", "-t", strconv.Itoa(threads),
		"--host", "127.0.0.1", "--port", strconv.Itoa(api), "-fit", "off", "-ngl", strconv.Itoa(in.NGL), "-lv", "4"}
	if in.Split != "" && in.NGL > 0 {
		args = append(args, "-ts", in.Split)
	}
	if len(rpcList) > 0 {
		args = append(args, "--rpc", strings.Join(rpcList, ","))
	}
	p, err := a.startProc("head", rt, findBin(rt, "llama-server"), args, false, info.Limits.Pedal < 100)
	if err != nil {
		for _, l := range listeners {
			l.Close()
		}
		return nil, err
	}
	a.mu.Lock()
	a.head, a.apiPort, a.listeners = p, api, listeners
	a.mu.Unlock()
	a.note("Loading the model", 0, 0)
	return map[string]int{"port": api, "threads": threads}, nil
}

// Status reports what is running here.
func (a *Agent) Status() AgentStatus {
	a.mu.Lock()
	s := AgentStatus{RPC: a.rpc.Running(), Head: a.head.Running(), Msg: a.status, Done: a.done, Total: a.total}
	api, head := a.apiPort, a.head
	a.mu.Unlock()
	if s.Head {
		c := http.Client{Timeout: 2 * time.Second}
		if res, err := c.Get("http://127.0.0.1:" + strconv.Itoa(api) + "/health"); err == nil {
			s.Ready = res.StatusCode == 200
			res.Body.Close()
		}
	}
	if head != nil {
		s.HeadLog = tailFile(head.logPath, 6000)
	}
	return s
}

// Stop ends every engine process and any download in progress.
func (a *Agent) Stop() {
	a.mu.Lock()
	if a.jobCancel != nil {
		a.jobCancel()
		a.jobCancel = nil
	}
	rpc, head, ls := a.rpc, a.head, a.listeners
	a.rpc, a.head, a.listeners = nil, nil, nil
	a.status, a.done, a.total = "", 0, 0
	a.mu.Unlock()
	head.Stop()
	rpc.Stop()
	for _, l := range ls {
		l.Close()
	}
}

// localPort is where a tunnel for the given target ends on this computer.
func (a *Agent) localPort(target string) (int, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch target {
	case "rpc":
		if a.rpc.Running() {
			return a.rpcPort, nil
		}
	case "api":
		if a.head.Running() {
			return a.apiPort, nil
		}
	}
	return 0, fmt.Errorf("nothing is running for %q here", target)
}
