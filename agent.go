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
	"regexp"
	"sort"
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
	ShareChat  bool              `json:"shareChat,omitempty"`  // chat page and API reachable from the local network
	HeadChoice string            `json:"headChoice,omitempty"` // "" = automatic
	LastModel  *ModelRef         `json:"lastModel,omitempty"`
	// Known remembers where each model that Potluck downloaded came from, by file name, so
	// it can be started again the same way from the chat page.
	Known map[string]ModelRef `json:"known,omitempty"`
	// Profiles: each computer's last benchmark, by computer. Measured: speeds seen on real
	// runs, newest first.
	Profiles map[string]*Profile `json:"profiles,omitempty"`
	Measured []Measured          `json:"measured,omitempty"`
	// Found: model files other apps keep on this computer that Potluck may use, by the name
	// Potluck knows them by. Potluck reads them where they are and never changes them.
	Found     map[string]string `json:"found,omitempty"`
	ModelDirs []string          `json:"modelDirs,omitempty"` // more folders to look in for models
	// EnginePath: "" = use an engine found on this computer when it is new enough and the
	// model runs here alone; "potluck" = always Potluck's own; else that llama-server.
	EnginePath string `json:"enginePath,omitempty"`
	Single     bool   `json:"single,omitempty"` // set up for one computer
	// IdleUnload: minutes without a request after which the model is let go to free the
	// memory (0 = keep it loaded). It loads again with the next message.
	IdleUnload int `json:"idleUnload,omitempty"`
	// AgentDir: the folder commands from the chat page's agent mode start in.
	AgentDir string `json:"agentDir,omitempty"`
	// Phones: paired iPhones, by id. Each holds a key; only its hash is kept here.
	Phones map[string]PhoneKey `json:"phones,omitempty"`
	Push   *PushConfig         `json:"push,omitempty"` // Apple push key for phone alerts (apns.go)
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
	gpuDev    string // the engine's name for this computer's graphics card, "" if none is used
	rpc, head *proc
	guard     *GuardEvent // the guard's last stop (guard.go)
	rpcPort   int
	apiPort   int
	listeners []net.Listener
	jobCancel context.CancelFunc
	status    string
	done      int64
	total     int64

	direct    *Direct // this computer's end of direct links to other computers
	streamDir string  // the installed InferSSD package, when there is one
	// streamNoGPU: InferSSD runs without the graphics card (too small for the model).
	streamNoGPU bool
	links       map[string]string // how each helper of the model running here is reached: direct | relay

	lastFound      []FoundModel // the last search for models on this computer
	engineChoice   string       // the engine found on this computer that a model alone uses
	engineChoiceAt time.Time
	localRT        bool // the engine in use is one found on this computer

	// set by whoever owns the agent
	dialTunnel func(to, target string) (net.Conn, error)
	notify     func(method string, data any)
	// introduce asks the main computer to bring this computer and another one together for
	// a direct link; it returns the addresses the other one may be reached at.
	introduce func(to string, port int) ([]string, error)
}

// dialPeer reaches a program on another computer: directly when the two can talk to each
// other, through the main computer otherwise.
func (a *Agent) dialPeer(to, target string) (net.Conn, error) {
	if a.introduce != nil && to != a.id.FP && os.Getenv("POTLUCK_NO_DIRECT") != "1" {
		if c, err := a.direct.Connect(to, target, a.introduce); err == nil {
			a.setLink(to, "direct")
			return c, nil
		}
	}
	c, err := a.dialTunnel(to, target)
	if err == nil {
		a.setLink(to, "relay")
	}
	return c, err
}

func (a *Agent) setLink(to, kind string) {
	a.mu.Lock()
	if a.links == nil {
		a.links = map[string]string{}
	}
	a.links[to] = kind
	a.mu.Unlock()
}

func newAgent(dir string, id *Identity, cfg *ConfigStore) *Agent {
	a := &Agent{dir: dir, id: id, cfg: cfg}
	a.direct = newDirect(id, a.localPort)
	a.info = staticInfo(dir)
	a.info.ID = id.FP
	go a.guardLoop()
	return a
}

// Info returns a fresh snapshot of this computer.
func (a *Agent) Info() NodeInfo {
	probeMetal(a.dir, a.cfg.Get().EnginePath)
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

// threadsFor turns the pedal into a number of CPU threads. The base is one thread per
// processor core; on processors that mix fast and slow cores it is the fast cores' threads
// (measured on a 4 fast + 8 slow core laptop: 8 threads beat 12 by 14% writing and 24%
// reading). all asks for every hardware thread instead.
func threadsFor(info NodeInfo, all bool) int {
	base := info.Cores
	if info.PerfThreads > 0 {
		base = info.PerfThreads
	} else if all && info.Threads > base {
		base = info.Threads
	}
	t := int(math.Round(float64(base) * float64(info.Limits.Pedal) / 100))
	if t < 1 {
		t = 1
	}
	return t
}

// Requests the main computer can make.
type (
	FetchModelReq struct {
		URL    string      `json:"url"`
		File   string      `json:"file"`
		Size   int64       `json:"size"`
		SHA256 string      `json:"sha256"`
		Parts  []ModelPart `json:"parts,omitempty"`
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
		// RPCDevices is how many devices the helpers offer in total (a helper with a
		// graphics card offers two).
		RPCDevices int `json:"rpcDevices"`
		// ExpertCPU lists layers whose expert weights stay in memory while the rest of the
		// layer sits on the graphics card.
		ExpertCPU []int `json:"expertCPU,omitempty"`
		// Stream: run the model with InferSSD, streaming experts from this computer's disk,
		// instead of llama.cpp.
		Stream bool `json:"stream,omitempty"`
		// DenseBytes is the size of the part of the model every word uses. InferSSD puts it on
		// the graphics card; when the card is too small for it, the card is left out.
		DenseBytes int64 `json:"denseBytes,omitempty"`
		// AllThreads: use every hardware thread instead of one per core. The main computer
		// asks for it only in the layout where that measured faster.
		AllThreads bool `json:"allThreads,omitempty"`
	}
	AgentStatus struct {
		RPC     bool   `json:"rpc"`
		Head    bool   `json:"head"`
		Ready   bool   `json:"ready"`
		Msg     string `json:"msg"`
		Done    int64  `json:"done"`
		Total   int64  `json:"total"`
		HeadLog string `json:"headLog,omitempty"`
		FullLog string `json:"fullLog,omitempty"` // the start of the engine's log: its load report
		// Links says how the model running here reaches each helper: "direct" or "relay".
		Links map[string]string `json:"links,omitempty"`
		// Guard: the last time the guard stopped an engine here to keep this computer from
		// freezing, and why (see guard.go).
		Guard *GuardEvent `json:"guard,omitempty"`
	}
)

// Handle runs one request.
func (a *Agent) Handle(method string, raw json.RawMessage) (any, error) {
	switch method {
	case "inventory":
		return a.Info(), nil

	case "ping":
		return nil, nil

	case "bench":
		// Measure this computer and keep the result beside its other data.
		if a.head.Running() || a.rpc.Running() {
			return nil, errors.New("this computer is lending to a running model; measure it when the model is stopped")
		}
		p := runBenchmark(a.Info(), a.dir)
		if b, err := json.MarshalIndent(p, "", "  "); err == nil {
			_ = os.WriteFile(filepath.Join(a.dir, "profile.json"), b, 0o644)
		}
		return p, nil

	case "expect":
		// The main computer introduces a computer that will link to this one directly.
		var in struct {
			FP    string   `json:"fp"`
			Addr  string   `json:"addr"`
			Addrs []string `json:"addrs"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		port, err := a.direct.Expect(in.FP, append([]string{in.Addr}, in.Addrs...))
		if err != nil {
			return nil, err
		}
		return map[string]any{"port": port, "addrs": localIPv4s()}, nil

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
		if spec.Solo {
			if le := a.localEngine(); le != "" {
				dir := filepath.Dir(le)
				dev := a.gpuDeviceName(dir)
				a.mu.Lock()
				a.rtDir, a.gpuDev, a.localRT = dir, dev, true
				a.mu.Unlock()
				a.note(fmt.Sprintf("Using the AI engine already on this computer (version %d)", engineBuild(le)), 0, 0)
				return map[string]string{"dir": dir, "gpuDevice": dev, "local": le}, nil
			}
		}
		dir, err := a.ensureRuntime(a.jobCtx(), spec, func(s string) { a.note(s, 0, 0) })
		if err != nil {
			return nil, err
		}
		dev := a.gpuDeviceName(dir)
		a.mu.Lock()
		a.rtDir, a.gpuDev, a.localRT = dir, dev, false
		a.mu.Unlock()
		a.pruneEngines(dir)
		a.note("AI engine ready", 0, 0)
		return map[string]string{"dir": dir, "gpuDevice": dev}, nil

	case "discover":
		// Look for models and engines that other apps put on this computer.
		var in struct {
			Deep bool `json:"deep"`
		}
		_ = json.Unmarshal(raw, &in)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		models := a.discoverModels(ctx, in.Deep)
		a.mu.Lock()
		a.lastFound, a.engineChoiceAt = models, time.Time{}
		a.mu.Unlock()
		return map[string]any{"models": models, "engines": a.discoverEngines(ctx), "dirs": a.cfg.Get().ModelDirs}, nil

	case "use_found":
		var in struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		name, err := a.useFound(in.Path)
		if err != nil {
			return nil, err
		}
		return map[string]string{"file": name}, nil

	case "ensure_stream":
		var p StreamPackage
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		dir, err := a.ensureStream(a.jobCtx(), p, func(s string) { a.note(s, 0, 0) })
		if err != nil {
			return nil, err
		}
		a.mu.Lock()
		a.streamDir = dir
		a.mu.Unlock()
		a.note("Disk-streaming engine ready", 0, 0)
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
		// One file, or every file of a model that comes in several pieces (the engine finds
		// the others by name once it is given the first).
		parts := in.Parts
		if len(parts) == 0 {
			parts = []ModelPart{{File: in.File, URL: in.URL, Size: in.Size, SHA256: in.SHA256}}
		}
		ctx := a.jobCtx()
		var before int64
		for _, part := range parts {
			err := downloadFile(ctx, part.URL, filepath.Join(a.dir, "models", filepath.Base(part.File)), part.Size, part.SHA256, 24, func(d, t int64, s string) {
				a.note("Model file: "+s, before+d, in.Size)
			})
			if err != nil {
				return nil, err
			}
			before += part.Size
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

	case "guard":
		a.mu.Lock()
		defer a.mu.Unlock()
		return a.guard, nil

	case "storage":
		return a.Storage(), nil

	case "read_header":
		// A model file that is already on this computer (copied here or downloaded with
		// another program): read its blueprint so it can be planned like any other.
		var in struct {
			File string `json:"file"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		return a.localHeader(filepath.Base(in.File))

	case "hash_model":
		var in struct {
			File string `json:"file"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		sum, err := sha256File(a.modelPath(in.File))
		if err != nil {
			return nil, err
		}
		return map[string]string{"sha256": sum}, nil

	case "delete_model":
		var in struct {
			File string `json:"file"`
		}
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, err
		}
		a.mu.Lock()
		busy := a.head.Running()
		a.mu.Unlock()
		if busy {
			return nil, errors.New("stop the running model first")
		}
		if a.isFound(in.File) {
			a.forgetFound(in.File) // another app's file: only take it off the list
			return a.Storage(), nil
		}
		base := filepath.Join(a.dir, "models", filepath.Base(in.File))
		for _, suffix := range []string{"", ".ok", ".part", ".progress"} {
			_ = os.Remove(base + suffix)
		}
		return a.Storage(), nil

	case "prune_cache":
		// This computer is not a helper for the model that is starting: drop any share left
		// from an earlier one.
		a.mu.Lock()
		busy := a.rpc.Running()
		a.mu.Unlock()
		if !busy {
			_ = os.RemoveAll(filepath.Join(a.dir, "cache", "rpc"))
		}
		return nil, nil

	case "clear_cache":
		a.mu.Lock()
		busy := a.rpc.Running() || a.head.Running()
		a.mu.Unlock()
		if busy {
			return nil, errors.New("stop the running model first")
		}
		_ = os.RemoveAll(filepath.Join(a.dir, "cache"))
		_ = os.RemoveAll(filepath.Join(a.dir, "tmp"))
		return a.Storage(), nil

	case "stop":
		a.Stop()
		return nil, nil
	}
	return nil, fmt.Errorf("unknown request %q", method)
}

func (a *Agent) startRPC() (any, error) {
	a.mu.Lock()
	rt, old, gpuDev := a.rtDir, a.rpc, a.gpuDev
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
	threads := threadsFor(info, false)
	_ = os.MkdirAll(filepath.Join(a.dir, "cache", "rpc"), 0o755)
	// Bound to this computer only. Other computers reach it through the encrypted tunnel,
	// because the worker protocol itself has no password.
	args := []string{"-H", "127.0.0.1", "-p", strconv.Itoa(port), "-c", "-t", strconv.Itoa(threads)}
	gpuBuild := strings.Contains(filepath.Base(rt), "cuda") || strings.Contains(filepath.Base(rt), "vulkan")
	switch {
	case gpuDev != "":
		// Offer the graphics card first and this computer's memory second. (By default the
		// engine would offer only the card.) The planner counts on exactly this order.
		args = append(args, "-d", gpuDev+",CPU")
	case gpuBuild:
		args = append(args, "-d", "CPU") // a graphics build, but no card worth using
	}
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
	if in.Stream {
		return a.startStream(in)
	}
	a.mu.Lock()
	rt, old, gpuDev := a.rtDir, a.head, a.gpuDev
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
	model := a.modelPath(in.File)
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
					t, err := a.dialPeer(w, "rpc")
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
	threads := threadsFor(info, in.AllThreads)
	if in.Ctx <= 0 {
		in.Ctx = 4096
	}
	args := []string{"-m", model, "-c", strconv.Itoa(in.Ctx), "-np", "1", "-t", strconv.Itoa(threads),
		"--host", "127.0.0.1", "--port", strconv.Itoa(api), "-fit", "off", "-ngl", strconv.Itoa(in.NGL), "-lv", "4",
		// Name the model in API answers by its file name, not the full path on this disk.
		"--alias", strings.TrimSuffix(filepath.Base(model), ".gguf")}
	if in.Split != "" && in.NGL > 0 {
		args = append(args, "-ts", in.Split)
	}
	if len(in.ExpertCPU) > 0 {
		// Keep these layers' expert weights in memory; the rest of each layer goes to the card.
		nums := make([]string, len(in.ExpertCPU))
		for i, l := range in.ExpertCPU {
			nums[i] = strconv.Itoa(l)
		}
		args = append(args, "-ot", `blk\.(`+strings.Join(nums, "|")+`)\.ffn_(up|down|gate)_(ch|)exps=CPU`)
	}
	// The helpers must be named before the device list: the engine only knows the names
	// RPC0, RPC1, ... once it has read --rpc.
	if len(rpcList) > 0 {
		args = append(args, "--rpc", strings.Join(rpcList, ","))
		// The engine normally saves its working state after every prompt so a conversation
		// can be rewound. With helpers that state has to come back over the network: measured
		// at 20 seconds per prompt on a direct Wi-Fi link and minutes on a poor one. Off, a
		// follow-up that cannot continue from where the last answer ended is read again
		// from the start, which is far cheaper here.
		args = append(args, "-ctxcp", "0", "-cram", "0")
	}
	if base := filepath.Base(rt); strings.Contains(base, "cuda") || strings.Contains(base, "vulkan") {
		// Name every device to use, in the plan's order: the helpers' devices, then the one
		// local card. Otherwise the engine would also pick up graphics built into the
		// processor or a second card the plan does not know about.
		var devs []string
		for i := 0; i < in.RPCDevices; i++ {
			devs = append(devs, "RPC"+strconv.Itoa(i))
		}
		if gpuDev != "" {
			devs = append(devs, gpuDev)
		}
		if len(devs) == 0 {
			devs = []string{"none"}
		}
		args = append(args, "-dev", strings.Join(devs, ","))
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

// startStream runs a model with InferSSD on this computer alone.
func (a *Agent) startStream(in StartHeadReq) (any, error) {
	a.mu.Lock()
	dir, old, oldL := a.streamDir, a.head, a.listeners
	a.listeners = nil
	a.mu.Unlock()
	bin := streamBin(dir)
	if dir == "" || bin == "" {
		return nil, errors.New("the disk-streaming engine is not installed yet")
	}
	old.Stop()
	for _, l := range oldL {
		l.Close()
	}
	model := a.modelPath(in.File)
	if _, err := os.Stat(model); err != nil {
		return nil, errors.New("the model file is not on this computer")
	}
	api, err := freePort()
	if err != nil {
		return nil, err
	}
	info := a.Info()
	// The pedal sets how much memory InferSSD's caches may take. The rest of the model stays
	// on the disk and is read as needed.
	cpu, gpu, _ := offer(info, 0)
	budget := cpu >> 20
	if budget < 1024 {
		budget = 1024
	}
	// InferSSD keeps the part of the model used for every word on the graphics card and
	// refuses to load when it does not fit (seen with a 4 GB card and a 4 GB part). Then run
	// without the card: processor and memory only.
	noGPU := gpu == 0 || (in.DenseBytes > 0 && gpu < in.DenseBytes+(512<<20))
	a.mu.Lock()
	a.streamNoGPU = noGPU
	a.mu.Unlock()
	a.pruneStreamRuns()
	p, err := a.startProc("head", dir, bin, streamArgs(model, api, in.Ctx, budget, noGPU), false, info.Limits.Pedal < 100)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.head, a.apiPort, a.links = p, api, nil
	a.mu.Unlock()
	a.note("Loading the model (the first start measures this computer's disk, one to three minutes)", 0, 0)
	return map[string]int{"port": api}, nil
}

// Status reports what is running here.
func (a *Agent) Status() AgentStatus {
	a.mu.Lock()
	s := AgentStatus{RPC: a.rpc.Running(), Head: a.head.Running(), Msg: a.status, Done: a.done, Total: a.total, Guard: a.guard}
	if len(a.links) > 0 {
		s.Links = map[string]string{}
		for k, v := range a.links {
			s.Links[k] = v
		}
	}
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
		if s.Ready {
			s.FullLog = headFile(head.logPath, 200000)
		}
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
	a.rpc, a.head, a.listeners, a.links = nil, nil, nil, nil
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

// StorageReport says what Potluck keeps on this computer's disk.
type StorageReport struct {
	Dir    string      `json:"dir"` // put model files here to use them
	Models []ModelFile `json:"models"`
	Cache  int64       `json:"cache"`  // this computer's share of models it helps with
	Engine int64       `json:"engine"` // the AI engine programs
	Free   int64       `json:"free"`
}

// ModelFile is one downloaded model, or one found on this computer (Found: where it is).
type ModelFile struct {
	File     string `json:"file"`
	Size     int64  `json:"size"`
	Complete bool   `json:"complete"`
	Found    string `json:"found,omitempty"`
}

func dirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, e := d.Info(); e == nil && info.Mode().IsRegular() {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// Storage lists what is on disk here.
func (a *Agent) Storage() StorageReport {
	r := StorageReport{Dir: filepath.Join(a.dir, "models"), Cache: dirSize(filepath.Join(a.dir, "cache")), Engine: dirSize(filepath.Join(a.dir, "runtime")), Free: int64(diskFree(a.dir))}
	_ = os.MkdirAll(r.Dir, 0o755)
	ents, _ := os.ReadDir(filepath.Join(a.dir, "models"))
	for _, e := range ents {
		name := e.Name()
		info, err := e.Info()
		if err != nil || e.IsDir() {
			continue
		}
		switch {
		case strings.HasSuffix(name, ".gguf"):
			r.Models = append(r.Models, ModelFile{File: name, Size: info.Size(), Complete: true})
		case strings.HasSuffix(name, ".gguf.part"):
			r.Models = append(r.Models, ModelFile{File: strings.TrimSuffix(name, ".part"), Size: info.Size()})
		}
	}
	// Models other apps keep: listed, not counted as Potluck's own disk use.
	found := a.cfg.Get().Found
	for name, path := range found {
		if otherPart.MatchString(name) && !firstPart.MatchString(name) {
			continue
		}
		var size int64
		if m := firstPart.FindStringSubmatch(name); m != nil {
			for n, p := range found {
				if otherPart.ReplaceAllString(n, "") == otherPart.ReplaceAllString(name, "") {
					if st, err := os.Stat(p); err == nil {
						size += st.Size()
					}
				}
			}
		} else if st, err := os.Stat(path); err == nil {
			size = st.Size()
		} else {
			continue // gone: the other app removed it
		}
		r.Models = append(r.Models, ModelFile{File: name, Size: size, Complete: true, Found: path})
	}
	sort.Slice(r.Models, func(i, j int) bool { return r.Models[i].File < r.Models[j].File })
	return r
}

// pruneEngines removes engine builds other than the one in use, so old versions do not pile up.
func (a *Agent) pruneEngines(keep string) {
	root := filepath.Join(a.dir, "runtime")
	ents, _ := os.ReadDir(root)
	for _, e := range ents {
		if p := filepath.Join(root, e.Name()); e.IsDir() && p != keep && e.Name() != "glibc" {
			_ = os.RemoveAll(p)
		}
	}
}

var localSplit = regexp.MustCompile(`-(\d{5})-of-(\d{5})\.gguf$`)

// localHeader reads the blueprint of a model file in this computer's models folder,
// following the other files when the model comes in several pieces.
func (a *Agent) localHeader(file string) (*ModelInfo, error) {
	dir := filepath.Join(a.dir, "models")
	names := []string{file}
	if m := localSplit.FindStringSubmatch(file); m != nil {
		n, _ := strconv.Atoi(m[2])
		names = nil
		for i := 1; i <= n; i++ {
			names = append(names, localSplit.ReplaceAllString(file, fmt.Sprintf("-%05d-of-%s.gguf", i, m[2])))
		}
	}
	var infos []*ModelInfo
	for i, name := range names {
		path := filepath.Join(dir, name)
		if a.isFound(name) {
			path = a.modelPath(name)
		}
		st, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("%s is missing from the models folder", name)
		}
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		hint := 0
		if i > 0 {
			hint = infos[0].Layers
		}
		var info *ModelInfo
		for size := int64(8 << 20); ; size *= 4 {
			if size > st.Size() {
				size = st.Size()
			}
			buf := make([]byte, size)
			if _, err = f.ReadAt(buf, 0); err != nil {
				break
			}
			if info, err = parseGGUFPart(buf, st.Size(), hint); err == nil || !errors.Is(err, errShort) || size == st.Size() {
				break
			}
		}
		f.Close()
		if err != nil {
			return nil, fmt.Errorf("%s does not look like a model file: %w", name, err)
		}
		infos = append(infos, info)
	}
	return mergeParts(infos), nil
}
