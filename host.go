package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	portUI    = 7716 // this computer only: the control panel
	portLink  = 7717 // helpers connect here (encrypted), and discovery (UDP)
	portJoin  = 7715 // "add a computer" web page for the local network
	portChat  = 7720 // this computer only: chat page and OpenAI-compatible API of the running model
	portPhone = 7721 // paired phones (encrypted, with a key per phone); only while one is paired
	portShare = 7722 // other devices and apps, with the share key; only while sharing is on
)

const engineTag = "b12608"

// For trying a build next to a Potluck that is already running: POTLUCK_PORT_OFFSET=100
// moves every port up by 100. Computers only find each other with the same offset.
func init() {
	if n, err := strconv.Atoi(os.Getenv("POTLUCK_PORT_OFFSET")); err == nil && n > 0 && n < 20000 {
		portUI, portLink, portJoin, portChat, portPhone, portShare = portUI+n, portLink+n, portJoin+n, portChat+n, portPhone+n, portShare+n
	}
}

// Node is one computer as the main computer sees it.
type Node struct {
	Info     NodeInfo  `json:"info"`
	Online   bool      `json:"online"`
	IsMain   bool      `json:"isMain"`
	Status   string    `json:"status"`
	Done     int64     `json:"done"`
	Total    int64     `json:"total"`
	Role     string    `json:"role"` // head | worker | "" while a model runs
	Held     int64     `json:"held"` // bytes of the running model on this computer
	Offer    int64     `json:"offer"`
	Active   bool      `json:"active"` // allowed to work right now (switch + schedule)
	NoGPU    bool      `json:"noGPU"`  // its engine found no graphics card it can use after all
	LastSeen time.Time `json:"lastSeen"`
	// LinkMs is the average round trip to this computer over the last couple of minutes,
	// jitter included. 0 until measured.
	LinkMs float64 `json:"linkMs"`
	rtts   []float64
	peer   *Peer
}

// Pending is a computer that asked to join and waits for a click.
type Pending struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	OS     string `json:"os"`
	Addr   string `json:"addr"`
	Code   string `json:"code"`
	decide chan bool
}

// Step is one line of the start-up checklist.
type Step struct {
	Text  string `json:"text"`
	State string `json:"state"` // todo | doing | done | failed
}

// ClusterState is the running (or starting) model.
type ClusterState struct {
	Phase   string    `json:"phase"` // idle | starting | running | stopping | error
	Model   *ModelRef `json:"model"`
	Steps   []Step    `json:"steps"`
	Plan    *Plan     `json:"plan"`
	Error   string    `json:"error"`
	Head    string    `json:"head"`
	Since   time.Time `json:"since"`
	TPS     float64   `json:"tps"`
	PTPS    float64   `json:"ptps"`
	Testing bool      `json:"testing"`
	Reply   string    `json:"reply"`
}

// Host is the main computer.
type Host struct {
	a   *Agent
	id  *Identity
	cfg *ConfigStore

	mu             sync.Mutex
	nodes          map[string]*Node
	memCeil        map[string]memCeiling // per computer, after its guard stepped in (hostguard.go)
	sessOnce       sync.Once
	sess           *sessionStore // agent sessions (sessions.go)
	pushErr        string        // the last problem sending a phone alert
	pushTestAnswer string        // what the phone answered to the last test alert
	vision         bool          // the running model can see images
	guardWhy       string        // why the guard stopped the model, shown instead of "stopped"
	pending        map[string]*Pending
	tunnels        map[string]chan net.Conn
	cluster        ClusterState
	cancel         context.CancelFunc
	release        map[string]Asset // engine release assets by name

	chatSrv  *http.Server
	lastCtx  int
	lastSolo string
	lastInfo *ModelInfo
	paused   *ModelRef // a model waiting for enough computers to be available again
	pauseWhy string
	stale    bool // limits changed on a computer that is part of the running model

	lastUse    time.Time // the last request to the model
	asleep     *ModelRef // let go after being unused for a while; the next request loads it
	asleepCtx  int
	asleepSolo string

	chatAPI    http.Handler // the chat address's handler, which phones reach through phoneHandler
	pairSecret string       // the one-time secret in the pairing code that is showing
	pairUntil  time.Time
	phoneSeen  map[string]time.Time
	phoneMu    sync.Mutex
	phoneSrvs  []*http.Server
	helperMu   sync.Mutex
	helper     helperState            // the Potluck helper (helper.go)
	helperGen  int                    // which setup run is current; bumped by stop and restart
	crew       map[string]*CrewMember // extra models on other computers (crew.go), under mu
	shareMu    sync.Mutex
	shareSrvs  []*http.Server
	shareOn    string // the addresses the share server listens on now
	phoneOn    string // the addresses the phone server listens on now
}

func newHost(a *Agent, id *Identity, cfg *ConfigStore) *Host {
	h := &Host{a: a, id: id, cfg: cfg, nodes: map[string]*Node{}, pending: map[string]*Pending{}, tunnels: map[string]chan net.Conn{}}
	h.cluster.Phase = "idle"
	cfg.Update(func(c *Config) {
		if c.JoinSecret == "" {
			c.JoinSecret = randomHex(8)
		}
		// The engine version follows the program: a new Potluck brings the engine it was
		// tested with (b12608 for Inkling 2 models), and every computer installs the same one.
		c.EngineTag = engineTag
	})
	self := &Node{Info: a.Info(), Online: true, IsMain: true}
	h.nodes[id.FP] = self
	a.dialTunnel = h.openTunnel
	a.introduce = func(to string, port int) ([]string, error) { return h.introduce(id.FP, to, port, localIPv4s()) }
	a.notify = func(method string, data any) {
		if p, ok := data.(ProgressNote); ok {
			h.setProgress(id.FP, p)
		}
	}
	return h
}

func (h *Host) setProgress(id string, p ProgressNote) {
	h.mu.Lock()
	if n := h.nodes[id]; n != nil {
		n.Status, n.Done, n.Total = p.Msg, p.Done, p.Total
	}
	h.mu.Unlock()
}

// Run starts the network side of the main computer.
func (h *Host) Run() error {
	ln, err := tls.Listen("tcp", ":"+strconv.Itoa(portLink), h.id.tlsConfig())
	if err != nil {
		return fmt.Errorf("could not open port %d: %w", portLink, err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go h.serveConn(c.(*tls.Conn))
		}
	}()
	go h.discoveryLoop()
	go h.inventoryLoop()
	go h.serveJoinPage()
	go h.watchLoop()
	go h.guardWatch()
	go h.idleLoop()
	h.applyShare()
	go h.phoneLoop()
	go h.shareLoop()
	go h.helperLoop()
	return nil
}

func (h *Host) trusted(fp string) bool {
	_, ok := h.cfg.Get().Trusted[fp]
	return ok
}

func (h *Host) serveConn(c *tls.Conn) {
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	if err := c.Handshake(); err != nil {
		c.Close()
		return
	}
	fp, err := peerFP(c)
	if err != nil {
		c.Close()
		return
	}
	var hello Hello
	if err := readLine(c, &hello, 15*time.Second); err != nil {
		c.Close()
		return
	}
	_ = c.SetDeadline(time.Time{})
	switch hello.Kind {
	case "control":
		h.serveControl(c, fp, hello)
	case "tunnel":
		if !h.trusted(fp) {
			_ = writeLine(c, HelloReply{Err: "not paired"})
			c.Close()
			return
		}
		t, err := h.openTunnel(hello.To, hello.Target)
		if err != nil {
			_ = writeLine(c, HelloReply{Err: err.Error()})
			c.Close()
			return
		}
		if writeLine(c, HelloReply{OK: true}) != nil {
			c.Close()
			t.Close()
			return
		}
		pipe(c, t)
	case "accept":
		h.mu.Lock()
		ch := h.tunnels[hello.Session]
		delete(h.tunnels, hello.Session)
		h.mu.Unlock()
		if !h.trusted(fp) || ch == nil || writeLine(c, HelloReply{OK: true}) != nil {
			c.Close()
			return
		}
		ch <- c
	default:
		c.Close()
	}
}

func (h *Host) serveControl(c *tls.Conn, fp string, hello Hello) {
	cfg := h.cfg.Get()
	name := strings.TrimSpace(hello.Name)
	if name == "" {
		name = "Computer " + fp[:4]
	}
	ok := h.trusted(fp)
	if !ok && hello.Secret != "" && hello.Secret == cfg.JoinSecret {
		ok = true // came with a USB kit or join link made on this computer
	}
	if !ok {
		// Unknown computer on the network: show it in the control panel and wait for a click.
		p := &Pending{ID: fp, Name: name, OS: hello.OS, Addr: c.RemoteAddr().String(), Code: pairCode(h.id.FP, fp), decide: make(chan bool, 1)}
		h.mu.Lock()
		if old := h.pending[fp]; old != nil {
			old.decide <- false
		}
		h.pending[fp] = p
		h.mu.Unlock()
		if writeLine(c, HelloReply{OK: true, State: "pending", Code: p.Code, Name: cfg.Name}) != nil {
			c.Close()
			return
		}
		// Wait for the click. (No reading from the connection meanwhile: a read that times out
		// can leave an encrypted connection unusable.) If the other computer gives up and asks
		// again, its new request replaces this one.
		var yes bool
		select {
		case yes = <-p.decide:
		case <-time.After(20 * time.Minute):
		}
		h.mu.Lock()
		if h.pending[fp] == p {
			delete(h.pending, fp)
		}
		h.mu.Unlock()
		if !yes {
			_ = writeLine(c, HelloReply{Err: "declined"})
			c.Close()
			return
		}
	}
	h.cfg.Update(func(c *Config) { c.Trusted[fp] = name })
	var addrs []string
	for _, ip := range localIPs() {
		addrs = append(addrs, net.JoinHostPort(ip, strconv.Itoa(portLink)))
	}
	if writeLine(c, HelloReply{OK: true, State: "paired", Name: h.selfName(), Addrs: addrs}) != nil {
		c.Close()
		return
	}
	peer := newPeer(c)
	peer.OnEvt = func(method string, data json.RawMessage) {
		if method == "progress" {
			var p ProgressNote
			if json.Unmarshal(data, &p) == nil {
				h.setProgress(fp, p)
			}
		}
	}
	peer.OnReq = func(method string, data json.RawMessage) (any, error) {
		if method == "introduce" {
			var in struct {
				To    string   `json:"to"`
				Port  int      `json:"port"`
				Addrs []string `json:"addrs"`
			}
			if err := json.Unmarshal(data, &in); err != nil {
				return nil, err
			}
			addrs, err := h.introduce(fp, in.To, in.Port, in.Addrs)
			if err != nil {
				return nil, err
			}
			return map[string]any{"addr": addrs[0], "addrs": addrs[1:]}, nil
		}
		return nil, errors.New("unknown request")
	}
	h.mu.Lock()
	n := h.nodes[fp]
	if n == nil {
		n = &Node{}
		h.nodes[fp] = n
	}
	if n.peer != nil {
		n.peer.Close()
	}
	n.peer, n.Online, n.LastSeen = peer, true, time.Now()
	if n.Info.ID == "" {
		n.Info = NodeInfo{ID: fp, Name: name, OS: hello.OS}
	}
	h.mu.Unlock()
	go h.refreshNode(fp)
	peer.Run()
	h.mu.Lock()
	if n.peer == peer {
		n.peer, n.Online = nil, false
		n.Status = ""
	}
	h.mu.Unlock()
}

func (h *Host) selfName() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.nodes[h.id.FP].Info.Name
}

// call runs a request on a node: directly for this computer, over the link for helpers.
func (h *Host) call(id, method string, in, out any, timeout time.Duration) error {
	h.mu.Lock()
	n := h.nodes[id]
	var peer *Peer
	if n != nil {
		peer = n.peer
	}
	h.mu.Unlock()
	if n == nil {
		return errors.New("unknown computer")
	}
	if id == h.id.FP {
		var raw json.RawMessage
		if in != nil {
			raw, _ = json.Marshal(in)
		}
		res, err := h.a.Handle(method, raw)
		if err != nil {
			return err
		}
		if out != nil && res != nil {
			b, _ := json.Marshal(res)
			return json.Unmarshal(b, out)
		}
		return nil
	}
	if peer == nil {
		return fmt.Errorf("%s is not connected", n.Info.Name)
	}
	return peer.Call(method, in, out, timeout)
}

func (h *Host) refreshNode(id string) {
	if id != h.id.FP {
		// Time a request that costs the helper nothing: that is the connection alone.
		t := time.Now()
		if h.call(id, "ping", nil, nil, 5*time.Second) == nil {
			ms := float64(time.Since(t).Microseconds()) / 1000
			h.mu.Lock()
			if n := h.nodes[id]; n != nil {
				n.rtts = append(n.rtts, ms)
				if len(n.rtts) > 20 {
					n.rtts = n.rtts[len(n.rtts)-20:]
				}
				var sum float64
				for _, v := range n.rtts {
					sum += v
				}
				n.LinkMs = sum / float64(len(n.rtts))
			}
			h.mu.Unlock()
		}
	}
	var info NodeInfo
	if err := h.call(id, "inventory", nil, &info, 15*time.Second); err != nil {
		// This doubles as the heartbeat. A helper that has said nothing for a while is cut
		// loose so it can come back over another path.
		h.mu.Lock()
		var peer *Peer
		if n := h.nodes[id]; n != nil {
			peer = n.peer
		}
		h.mu.Unlock()
		if peer != nil && peer.Quiet() > 25*time.Second {
			peer.Close()
		}
		return
	}
	h.mu.Lock()
	if n := h.nodes[id]; n != nil {
		info.ID = id
		n.Info, n.LastSeen = info, time.Now()
	}
	h.mu.Unlock()
}

func (h *Host) inventoryLoop() {
	for {
		h.mu.Lock()
		var ids []string
		for id, n := range h.nodes {
			if n.Online {
				ids = append(ids, id)
			}
		}
		h.mu.Unlock()
		for _, id := range ids {
			go h.refreshNode(id)
		}
		time.Sleep(6 * time.Second)
	}
}

// openTunnel returns a byte stream to a program on another computer ("rpc" = its helper
// engine, "api" = the model's chat/API port on the head).
func (h *Host) openTunnel(to, target string) (net.Conn, error) {
	if to == h.id.FP {
		port, err := h.a.localPort(target)
		if err != nil {
			return nil, err
		}
		return net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 5*time.Second)
	}
	session := randomHex(12)
	ch := make(chan net.Conn, 1)
	h.mu.Lock()
	h.tunnels[session] = ch
	h.mu.Unlock()
	defer func() { h.mu.Lock(); delete(h.tunnels, session); h.mu.Unlock() }()
	if err := h.call(to, "open_tunnel", map[string]string{"session": session, "target": target}, nil, 15*time.Second); err != nil {
		return nil, err
	}
	select {
	case c := <-ch:
		return c, nil
	case <-time.After(15 * time.Second):
		return nil, errors.New("the other computer did not connect back")
	}
}

// introduce brings two paired computers together for a direct link. "from" will connect
// from the given UDP port; the answer lists the addresses "to" can be reached at. Each side
// learns the other's addresses and fingerprint only through its own trusted link to this
// computer. Both the address each one is seen at from here and the addresses it reports
// itself are passed on: two computers may reach this one over different networks.
func (h *Host) introduce(from, to string, port int, fromOwn []string) ([]string, error) {
	if from == to || port <= 0 || port > 65535 {
		return nil, errors.New("bad introduction")
	}
	h.mu.Lock()
	var fromPeer, toPeer *Peer
	if n := h.nodes[from]; n != nil {
		fromPeer = n.peer
	}
	if n := h.nodes[to]; n != nil {
		toPeer = n.peer
	}
	h.mu.Unlock()
	me := h.id.FP
	var fromIP, toIP string
	switch {
	case from == me && toPeer != nil:
		fromIP, toIP = addrHost(toPeer.c.LocalAddr()), addrHost(toPeer.c.RemoteAddr())
	case to == me && fromPeer != nil:
		fromIP, toIP = addrHost(fromPeer.c.RemoteAddr()), addrHost(fromPeer.c.LocalAddr())
	case fromPeer != nil && toPeer != nil:
		fromIP, toIP = addrHost(fromPeer.c.RemoteAddr()), addrHost(toPeer.c.RemoteAddr())
	default:
		return nil, errors.New("that computer is not connected")
	}
	withPort := func(first string, more []string, p int) []string {
		out := []string{net.JoinHostPort(first, strconv.Itoa(p))}
		for _, ip := range more {
			if len(out) < 12 && net.ParseIP(ip) != nil && ip != first {
				out = append(out, net.JoinHostPort(ip, strconv.Itoa(p)))
			}
		}
		return out
	}
	fromAddrs := withPort(fromIP, fromOwn, port)
	var res struct {
		Port  int      `json:"port"`
		Addrs []string `json:"addrs"`
	}
	in := map[string]any{"fp": from, "addr": fromAddrs[0], "addrs": fromAddrs[1:]}
	if err := h.call(to, "expect", in, &res, 10*time.Second); err != nil {
		return nil, err
	}
	if res.Port <= 0 {
		return nil, errors.New("no port")
	}
	return withPort(toIP, res.Addrs, res.Port), nil
}

// ---------- discovery ----------

type beacon struct {
	Potluck string `json:"potluck"` // find | here
	Name    string `json:"name,omitempty"`
	FP      string `json:"fp,omitempty"`
	Port    int    `json:"port,omitempty"`
}

// discoveryLoop answers helpers that shout "is there a main computer here?" on the local network.
func (h *Host) discoveryLoop() {
	pc, err := net.ListenPacket("udp4", ":"+strconv.Itoa(portLink))
	if err != nil {
		return
	}
	buf := make([]byte, 2048)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		var b beacon
		if json.Unmarshal(buf[:n], &b) != nil || b.Potluck != "find" {
			continue
		}
		out, _ := json.Marshal(beacon{Potluck: "here", Name: h.selfName(), FP: h.id.FP, Port: portLink})
		_, _ = pc.WriteTo(out, addr)
	}
}

func localIPs() []string {
	var out []string
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if ip4 := ipn.IP.To4(); ip4 != nil && !ip4.IsLinkLocalUnicast() {
					out = append(out, ip4.String())
				}
			}
		}
	}
	// Home network addresses first, VPN-style ones (100.x) last.
	sort.SliceStable(out, func(i, j int) bool { return rankIP(out[i]) < rankIP(out[j]) })
	return out
}

func rankIP(ip string) int {
	switch {
	case strings.HasPrefix(ip, "192.168."), strings.HasPrefix(ip, "10."):
		return 0
	case strings.HasPrefix(ip, "172."):
		return 1
	}
	return 2
}

// ---------- chat / API proxy ----------

// applyShare (re)starts the chat page and OpenAI-compatible API of the running model. It is
// reachable from this computer only, unless sharing with the local network is switched on.
// The API has no password, so sharing is a deliberate switch.
func (h *Host) applyShare() {
	target, _ := url.Parse("http://model.invalid")
	rp := httputil.NewSingleHostReverseProxy(target)
	rp.Transport = &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			h.mu.Lock()
			head, phase := h.cluster.Head, h.cluster.Phase
			h.mu.Unlock()
			if phase != "running" || head == "" {
				return nil, errors.New("no model is running")
			}
			return h.openTunnel(head, "api")
		},
		MaxIdleConns:    4,
		IdleConnTimeout: 30 * time.Second,
	}
	rp.FlushInterval = 50 * time.Millisecond
	rp.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "Potluck: " + err.Error() + ". Start a model first."}})
	}
	// The chat address is for this computer only; other devices use the share address
	// (netapi.go), on home-network and Tailscale addresses, with the key.
	addr := "127.0.0.1:" + strconv.Itoa(portChat)
	h.mu.Lock()
	old := h.chatSrv
	handler := h.chatHandler(rp)
	srv := &http.Server{Addr: addr, Handler: handler}
	h.chatSrv, h.chatAPI = srv, handler
	h.mu.Unlock()
	go h.applyNetShare()
	go func() {
		if old != nil {
			_ = old.Close()
			time.Sleep(300 * time.Millisecond)
		}
		_ = srv.ListenAndServe()
	}()
}

// ---------- keeping the running model in line with pedals and hours ----------

// watchLoop checks, a few times a minute, that every computer holding part of the running
// model is still allowed to. When one leaves (its hours ended, it was switched off, or it
// disconnected) the model is laid out again over the computers that remain; if it no longer
// fits it waits and comes back by itself when enough of them return.
func (h *Host) watchLoop() {
	for {
		time.Sleep(20 * time.Second)
		now := time.Now()
		h.mu.Lock()
		phase, model, ctxSize, info := h.cluster.Phase, h.cluster.Model, h.lastCtx, h.lastInfo
		paused := h.paused
		var gone []string
		if phase == "running" {
			for _, n := range h.nodes {
				if n.Role != "" && (!n.Online || !n.Info.Limits.ActiveNow(now)) {
					gone = append(gone, n.Info.Name)
				}
			}
		}
		h.mu.Unlock()
		switch {
		case len(gone) > 0 && model != nil:
			why := strings.Join(gone, " and ") + " stopped lending"
			h.Stop()
			if info != nil && h.plan(info, h.soloChoice()).Fits {
				_ = h.Start(*model, ctxSize, h.soloChoice())
			} else {
				h.mu.Lock()
				h.paused, h.pauseWhy = model, why+". The model no longer fits on the computers that are left; it will start again by itself when they are back."
				h.mu.Unlock()
			}
		case paused != nil && phase == "idle" && info != nil:
			if h.plan(info, h.soloChoice()).Fits {
				h.mu.Lock()
				h.paused, h.pauseWhy = nil, ""
				h.mu.Unlock()
				_ = h.Start(*paused, ctxSize, h.soloChoice())
			}
		}
	}
}

func (h *Host) soloChoice() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastSolo
}

// Storage asks every connected computer what Potluck keeps on its disk.
func (h *Host) Storage() map[string]StorageReport {
	h.mu.Lock()
	var ids []string
	for id, n := range h.nodes {
		if n.Online {
			ids = append(ids, id)
		}
	}
	h.mu.Unlock()
	out := map[string]StorageReport{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			var r StorageReport
			if h.call(id, "storage", nil, &r, 20*time.Second) == nil {
				mu.Lock()
				out[id] = r
				mu.Unlock()
			}
		}(id)
	}
	wg.Wait()
	return out
}

// ---------- engine release lookup ----------

func (h *Host) engineAssets(tag string) (map[string]Asset, error) {
	h.mu.Lock()
	if h.release != nil {
		defer h.mu.Unlock()
		return h.release, nil
	}
	h.mu.Unlock()
	cache := filepath.Join(h.a.dir, "engine-"+tag+".json")
	var rel struct {
		Assets []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			Digest string `json:"digest"`
			Size   int64  `json:"size"`
		} `json:"assets"`
	}
	if b, err := os.ReadFile(cache); err == nil {
		_ = json.Unmarshal(b, &rel)
	}
	if len(rel.Assets) == 0 {
		if err := httpGetJSON("https://api.github.com/repos/ggml-org/llama.cpp/releases/tags/"+tag, &rel); err != nil {
			return nil, fmt.Errorf("could not look up the AI engine release: %w", err)
		}
		if b, err := json.Marshal(rel); err == nil {
			_ = os.WriteFile(cache, b, 0o644)
		}
	}
	m := map[string]Asset{}
	for _, a := range rel.Assets {
		m[a.Name] = Asset{Name: a.Name, URL: a.URL, SHA256: strings.TrimPrefix(a.Digest, "sha256:"), Size: a.Size}
	}
	h.mu.Lock()
	h.release = m
	h.mu.Unlock()
	return m, nil
}

// gpuBackend says which engine build drives a computer's graphics card: "cuda" (NVIDIA on
// Windows), "vulkan" (AMD and Intel cards, and NVIDIA on Linux), "metal" (Apple Silicon) or ""
// for none. POTLUCK_FORCE_VULKAN=1 on the main computer swaps cuda for vulkan, for testing.
func gpuBackend(info NodeInfo) string {
	g := info.usableGPU()
	if g == nil {
		return ""
	}
	if g.Backend == "cuda" && os.Getenv("POTLUCK_FORCE_VULKAN") == "1" {
		return "vulkan"
	}
	return g.Backend
}

// twoDev says whether a computer, as a helper, offers its graphics card and its memory as
// two separate pools.
func twoDev(info NodeInfo) bool {
	b := gpuBackend(info)
	return b == "cuda" || b == "vulkan"
}

// runtimeSpec picks the engine build for a computer.
func (h *Host) runtimeSpec(info NodeInfo, isHead bool) (RuntimeSpec, error) {
	tag := h.cfg.Get().EngineTag
	assets, err := h.engineAssets(tag)
	if err != nil {
		return RuntimeSpec{}, err
	}
	var variant string
	var names []string
	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[info.Arch]
	switch info.OS {
	case "windows":
		switch backend := gpuBackend(info); {
		case backend == "cuda" && arch == "x64":
			variant = "win-cuda-12.4-x64"
			names = []string{"llama-" + tag + "-bin-win-cuda-12.4-x64.zip", "cudart-llama-bin-win-cuda-12.4-x64.zip"}
		case backend == "vulkan" && arch == "x64":
			variant = "win-vulkan-x64"
			names = []string{"llama-" + tag + "-bin-win-vulkan-x64.zip"}
		default:
			variant = "win-cpu-" + arch
			names = []string{"llama-" + tag + "-bin-win-cpu-" + arch + ".zip"}
		}
	case "darwin":
		variant = "macos-" + arch
		names = []string{"llama-" + tag + "-bin-macos-" + arch + ".tar.gz"}
	case "linux":
		variant = "ubuntu-" + arch
		if gpuBackend(info) == "vulkan" && arch == "x64" {
			variant = "ubuntu-vulkan-x64"
		}
		names = []string{"llama-" + tag + "-bin-" + variant + ".tar.gz"}
	}
	if arch == "" || variant == "" {
		return RuntimeSpec{}, fmt.Errorf("%s (%s/%s) is not supported yet", info.Name, info.OS, info.Arch)
	}
	spec := RuntimeSpec{Tag: tag, Variant: variant}
	for _, n := range names {
		a, ok := assets[n]
		if !ok {
			return RuntimeSpec{}, fmt.Errorf("the AI engine release has no file %s", n)
		}
		spec.Assets = append(spec.Assets, a)
	}
	return spec, nil
}

// ---------- planning ----------

// offer is how much memory a computer lends, after its pedal and a safety reserve.
func offer(info NodeInfo, held int64) (cpu, gpu int64, unified bool) {
	reserve := int64(info.RAMTotal / 8)
	if reserve < 1536<<20 {
		reserve = 1536 << 20
	}
	avail := int64(info.RAMAvail) + held
	if avail > int64(info.RAMTotal) {
		avail = int64(info.RAMTotal)
	}
	usable := avail - reserve
	if usable < 0 {
		usable = 0
	}
	pedal := int64(info.Limits.Pedal)
	cpu = usable * pedal / 100
	if g := info.usableGPU(); g != nil {
		if g.Unified {
			return 0, cpu, true
		}
		v := int64(g.VRAMFree) - (500 << 20)
		if v > 0 {
			gpu = v * pedal / 100
		}
	}
	return cpu, gpu, false
}

// roomForOne: on a computer used by itself, count the memory other apps can give up, not
// only what is idle this second. A Mac in use shows little free memory, yet makes room by
// compressing what other apps hold: measured on a 16 GB Mac mini with about 6% free, the
// 8.7 GB Inkling 2 Mini loaded and ran at 15 words/s.
func roomForOne(info NodeInfo) NodeInfo {
	if room := info.RAMTotal * 80 / 100; info.RAMAvail < room {
		info.RAMAvail = room
	}
	return info
}

// planNodes lists the computers that may work right now, with what each offers.
func (h *Host) planNodes() []PlanNode {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	loaded := h.cluster.Phase == "running"
	single := h.cfg != nil && h.cfg.Get().Single
	var out []PlanNode
	for id, n := range h.nodes {
		n.Active = n.Online && n.Info.Limits.ActiveNow(now)
		held := n.Held
		if !loaded {
			held = 0 // nothing is in memory yet while a model is still starting
		}
		info := n.Info
		if single && id == h.id.FP {
			info = roomForOne(info)
		}
		cpu, gpu, uni := offer(info, held)
		cpu, gpu = h.capByCeiling(id, cpu, gpu)
		// The helper's and crew members' memory is not on offer to the main model.
		if r := h.helperReserveLocked(id) + h.crewReserveLocked(id); r > 0 {
			if r > cpu {
				gpu = max(0, gpu-(r-cpu))
			}
			cpu = max(0, cpu-r)
		}
		n.Offer = cpu + gpu
		if !n.Active {
			n.Offer = 0
			continue
		}
		pn := PlanNode{ID: id, Name: n.Info.Name, CPUBytes: cpu, GPUBytes: gpu, Unified: uni, Cores: n.Info.Cores,
			TwoDev: twoDev(n.Info), HasGPU: n.Info.usableGPU() != nil, RAMTotal: int64(n.Info.RAMTotal), LinkMs: n.LinkMs,
			DiskFree: int64(n.Info.DiskFree)}
		pn.StreamVariant, pn.StreamWhy = streamVariant(n.Info)
		if n.NoGPU && !uni {
			pn.GPUBytes, pn.TwoDev, pn.HasGPU = 0, false, false
		}
		out = append(out, pn)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// modelInfo reads a model's blueprint: from its download address, or from the computer that
// already has the file.
func (h *Host) modelInfo(ctx context.Context, m *ModelRef) (*ModelInfo, error) {
	if m.Source != "local" {
		return fetchModelHeaders(ctx, m)
	}
	var info ModelInfo
	if err := h.call(m.Node, "read_header", map[string]string{"file": m.File}, &info, 2*time.Minute); err != nil {
		return nil, err
	}
	m.Size = info.FileSize
	return &info, nil
}

// plan lays a model out: pooled across the computers, or on one computer alone (solo) with
// its disk standing in for the memory it lacks.
func (h *Host) plan(info *ModelInfo, solo string) *Plan { return h.planFor(info, solo, "") }

// planFor is plan with the head fixed: a model file that is already on a computer has to
// run from that computer.
// autoCtx picks the conversation memory when the person has not chosen one: 32k tokens
// (room for an agent task's tools, files and history) when that fits without the disk, less
// only when it does not, and more when the plan stays exactly as it is (same placement on the
// same computers, so no speed is lost), up to what the model was trained for.
func (h *Host) autoCtx(info *ModelInfo, file, solo, head string) int {
	const floor = 32768
	try := func(c int) *Plan {
		m := *info
		m.PlanCtx = c
		m.WorkSeen = h.cfg.Get().WorkSeen[workKey(file, c)]
		return h.planFor(&m, solo, head)
	}
	good := func(p *Plan) bool { return p.Fits && p.Overflow == 0 }
	limit := info.CtxTrain
	if limit <= 0 {
		limit = floor
	}
	best, bestPlan := 0, (*Plan)(nil)
	for _, c := range []int{floor, 16384, 8192} {
		if c > limit {
			continue
		}
		if p := try(c); good(p) {
			best, bestPlan = c, p
			break
		}
	}
	if best == 0 {
		return min(8192, limit) // nothing fits well; the plan at the usual size says why
	}
	if best < floor {
		return best
	}
	for c := floor * 2; c <= limit && c <= 1<<18; c *= 2 {
		p := try(c)
		if !good(p) || !samePlacement(p, bestPlan) {
			break
		}
		best = c
	}
	return best
}

// samePlacement: the two plans put the same layers on the same devices in the same way.
func samePlacement(a, b *Plan) bool {
	return a.Head == b.Head && a.NGL == b.NGL && a.KVKeyQ8 == b.KVKeyQ8 && a.CardTrunk == b.CardTrunk &&
		slices.Equal(a.WorkerOrder, b.WorkerOrder) && slices.Equal(a.DevLayers, b.DevLayers) && slices.Equal(a.ExpertCPU, b.ExpertCPU)
}

func (h *Host) planFor(info *ModelInfo, solo, head string) *Plan {
	p := h.planForKV(info, solo, head)
	// Too big as it is: try again with the conversation memory's keys stored in 8 bits
	// (about a quarter less memory, close to no loss in quality, and it works on every
	// device, unlike 8-bit values, which need flash attention).
	if !p.Fits && !info.KVKeyQ8 && !p.Stream && info.ContextTotal(max(info.PlanCtx, 1)) > 0 {
		sq := *info
		sq.KVKeyQ8 = true
		if q := h.planForKV(&sq, solo, head); q.Fits {
			q.KVKeyQ8 = true
			return q
		}
	}
	return p
}

func (h *Host) planForKV(info *ModelInfo, solo, head string) *Plan {
	nodes := h.planNodes()
	if solo == "" {
		if head == "" {
			head = h.cfg.Get().HeadChoice
		} else {
			found := false
			for _, n := range nodes {
				found = found || n.ID == head
			}
			if !found {
				return &Plan{Reason: "The computer that holds this model file is not available right now."}
			}
		}
		return planLayout(info, nodes, head)
	}
	if id, ok := streamChoice(solo); ok {
		if head != "" && head != id {
			return &Plan{Reason: "The model file is on another computer; it can only be streamed from the computer that holds it."}
		}
		for _, n := range nodes {
			if n.ID == id {
				return planStream(info, n, head == id)
			}
		}
		return &Plan{Reason: "That computer is not available right now."}
	}
	for _, n := range nodes {
		if n.ID == solo {
			return planSolo(info, n)
		}
	}
	return &Plan{Reason: "That computer is not available right now."}
}

// bestSolo finds the computer that could run a model alone with the least help from its
// disk. only, when set, limits the search to that computer.
func (h *Host) bestSolo(info *ModelInfo, only string) *Plan {
	var best *Plan
	for _, n := range h.planNodes() {
		if only != "" && n.ID != only {
			continue
		}
		if p := planSolo(info, n); p.Fits && (best == nil || p.Overflow < best.Overflow) {
			best = p
		}
	}
	return best
}

// ---------- starting and stopping a model ----------

func (h *Host) step(text string) func(err error) {
	h.mu.Lock()
	h.cluster.Steps = append(h.cluster.Steps, Step{Text: text, State: "doing"})
	i := len(h.cluster.Steps) - 1
	h.mu.Unlock()
	return func(err error) {
		h.mu.Lock()
		if i < len(h.cluster.Steps) {
			if err != nil {
				h.cluster.Steps[i].State = "failed"
			} else {
				h.cluster.Steps[i].State = "done"
			}
		}
		h.mu.Unlock()
	}
}

// Start plans and launches a model across the computers. It returns at once; progress is in
// the cluster state.
func (h *Host) Start(model ModelRef, ctxSize int, solo string) error {
	h.mu.Lock()
	if h.cluster.Phase == "starting" || h.cluster.Phase == "stopping" {
		h.mu.Unlock()
		return errors.New("busy: wait for the current start or stop to finish")
	}
	// Claim the start before letting go of the lock, so a second start (a click, the
	// watcher, a phone) is turned away instead of running alongside this one.
	running := h.cluster.Phase == "running"
	if h.cancel != nil {
		h.cancel()
		h.cancel = nil
	}
	h.cluster = ClusterState{Phase: "starting", Model: &model, Since: time.Now()}
	h.mu.Unlock()
	if running {
		// Stop the old model and wait until its engines have exited, then read every
		// computer's free memory afresh: planning on the last report would count the old
		// model as still there (seen: a 23.5 GB model planned to read 34 GB from disk on a
		// computer with 27 GB free).
		h.stopAll()
		h.mu.Lock()
		h.vision = false
		h.mu.Unlock()
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.mu.Lock()
	h.cancel = cancel
	h.cluster = ClusterState{Phase: "starting", Model: &model, Since: time.Now()}
	h.guardWhy = ""
	h.lastCtx, h.lastSolo, h.stale = ctxSize, solo, false
	h.asleep, h.lastUse = nil, time.Now()
	h.mu.Unlock()
	h.cfg.Update(func(c *Config) { c.LastModel = &model })
	go func() {
		err := h.start(ctx, model, ctxSize, solo)
		h.mu.Lock()
		if err != nil {
			h.cluster.Phase, h.cluster.Error = "error", err.Error()
			if h.guardWhy != "" {
				h.cluster.Error = h.guardWhy // the guard stopped it: say that, not "stopped"
			}
			for i := range h.cluster.Steps {
				if h.cluster.Steps[i].State == "doing" {
					h.cluster.Steps[i].State = "failed"
				}
			}
		} else {
			h.cluster.Phase = "running"
		}
		h.mu.Unlock()
		if err != nil {
			h.stopAll()
		}
	}()
	return nil
}

func (h *Host) start(ctx context.Context, model ModelRef, ctxSize int, solo string) error {
	local := model.Source == "local"
	done := h.step("Reading the model's blueprint")
	if err := resolveModel(ctx, &model); err != nil {
		done(err)
		return err
	}
	h.mu.Lock()
	h.cluster.Model = &model // now with its title, size and checksum filled in
	h.mu.Unlock()
	h.cfg.Update(func(c *Config) {
		c.LastModel = &model
		if !local && model.File != "" {
			if c.Known == nil {
				c.Known = map[string]ModelRef{}
			}
			c.Known[model.File] = model
		}
	})
	info, err := h.modelInfo(ctx, &model)
	done(err)
	if err != nil {
		return fmt.Errorf("could not read the model: %w", err)
	}
	if ctxSize <= 0 {
		ctxSize = h.autoCtx(info, model.File, solo, model.Node)
	}
	h.mu.Lock()
	h.lastCtx = ctxSize
	info.PlanCtx = ctxSize // plans count the context cache for this size
	if model.Vision != nil {
		info.OtherBytes += model.Vision.Size // the image add-on is held by the computer running the model
	}
	info.WorkSeen = h.cfg.Get().WorkSeen[workKey(model.File, ctxSize)]
	h.lastInfo = info
	h.mu.Unlock()

	type part struct {
		id     string
		isHead bool
	}
	var plan *Plan
	var parts []part
	// Plan, install the engine, and check what each engine can really use. If a computer's
	// graphics card turns out not to be usable, plan once more without it.
	for attempt := 0; ; attempt++ {
		done = h.step("Working out which computer holds which part")
		plan = h.planFor(info, solo, model.Node)
		h.mu.Lock()
		h.cluster.Plan, h.cluster.Head = plan, plan.Head
		h.mu.Unlock()
		if !plan.Fits {
			done(errors.New(plan.Reason))
			return errors.New(plan.Reason)
		}
		done(nil)

		// Everyone that takes part, head first.
		parts = []part{{id: plan.Head, isHead: true}}
		for _, w := range plan.WorkerOrder {
			parts = append(parts, part{id: w})
		}
		helper := map[string]bool{}
		for _, w := range plan.WorkerOrder {
			helper[w] = true
		}
		h.mu.Lock()
		var idle []string
		for id, n := range h.nodes {
			n.Role, n.Held = "", 0
			for _, s := range plan.Shares {
				if s.ID == id && (s.CPULayers+s.GPULayers > 0 || s.Role == "head") {
					n.Role, n.Held = s.Role, s.Bytes
				}
			}
			if n.Online && !helper[id] {
				idle = append(idle, id)
			}
		}
		h.mu.Unlock()
		// Computers that are not helpers this time do not need a share from an earlier model.
		for _, id := range idle {
			go func(id string) { _ = h.call(id, "prune_cache", nil, nil, 30*time.Second) }(id)
		}

		alone := len(parts) == 1
		if alone {
			done = h.step("Getting the AI engine ready")
		} else {
			done = h.step("Installing the AI engine on each computer")
		}
		var replan sync.Map
		if err := parallelDo(parts, func(p part) error {
			h.mu.Lock()
			ni, noGPU := h.nodes[p.id].Info, h.nodes[p.id].NoGPU
			h.mu.Unlock()
			if noGPU {
				for i := range ni.GPUs {
					ni.GPUs[i].Usable = false // install the plain build
				}
			}
			if plan.Stream {
				variant, why := streamVariant(ni)
				if variant == "" {
					return errors.New(ni.Name + ": streaming from disk is " + why)
				}
				pkg, err := h.streamPackage(variant)
				if err != nil {
					return err
				}
				return h.call(p.id, "ensure_stream", pkg, nil, 2*time.Hour)
			}
			spec, err := h.runtimeSpec(ni, p.isHead)
			if err != nil {
				return err
			}
			spec.Solo = p.isHead && len(plan.WorkerOrder) == 0
			var res struct {
				GPUDevice string `json:"gpuDevice"`
			}
			if err := h.call(p.id, "ensure_runtime", spec, &res, 2*time.Hour); err != nil {
				return err
			}
			if twoDev(ni) && res.GPUDevice == "" {
				replan.Store(p.id, true) // the engine found no card it can use
			}
			return nil
		}); err != nil {
			done(err)
			return err
		}
		done(nil)
		changed := false
		replan.Range(func(k, _ any) bool {
			h.mu.Lock()
			if n := h.nodes[k.(string)]; n != nil && !n.NoGPU {
				n.NoGPU, changed = true, true
			}
			h.mu.Unlock()
			return true
		})
		if !changed || attempt >= 2 {
			break
		}
	}

	text := "Downloading the model (each computer fetches its own share)"
	switch {
	case local && len(plan.WorkerOrder) == 0:
		text = "Finding the model file"
	case local:
		text = "Getting the helpers ready (the head sends them their share)"
	case len(parts) == 1:
		text = "Downloading the model"
	}
	done = h.step(text)
	split := parseSplit(plan.TensorSplit)
	if err := parallelDo(parts, func(p part) error {
		if p.isHead {
			if local {
				return nil // the file is already there
			}
			return h.call(p.id, "fetch_model", FetchModelReq{URL: model.URL, File: model.File, Size: model.Size, SHA256: model.SHA256, Parts: model.Parts, Vision: model.Vision}, nil, 24*time.Hour)
		}
		var res struct {
			Port int `json:"port"`
		}
		if err := h.call(p.id, "start_rpc", nil, &res, time.Minute); err != nil {
			return err
		}
		if local {
			return nil // nothing to download from: the head pushes this share over the network
		}
		var items []SeedItem
		for _, t := range info.Tensors {
			layer := t.Layer
			if layer < 0 {
				if t.Name != "output.weight" {
					continue
				}
				layer = info.EngineLayers()
			}
			if !t.OK || t.Size <= rpcHashThreshold {
				continue
			}
			if dev := deviceOfLayer(layer, info.EngineLayers(), plan.NGL, split); dev >= 0 && dev < len(plan.DevOwner) && plan.DevOwner[dev] == p.id {
				it := SeedItem{Off: t.Off, Size: t.Size}
				if t.Part < len(model.Parts) {
					it.URL = model.Parts[t.Part].URL
				}
				items = append(items, it)
			}
		}
		if err := h.call(p.id, "seed", SeedReq{URL: model.URL, Items: items}, nil, 24*time.Hour); err != nil {
			// Not fatal: the head can still push this share over the local network.
			h.setProgress(p.id, ProgressNote{Msg: "Will receive its share from the head instead (" + err.Error() + ")"})
		}
		return nil
	}); err != nil {
		done(err)
		return err
	}
	done(nil)
	if ctx.Err() != nil {
		return errors.New("stopped")
	}

	if len(plan.WorkerOrder) == 0 {
		done = h.step("Loading the model")
	} else {
		done = h.step("Loading the model across the computers")
	}
	// Devices the helpers offer, in order; the head's own card (if any) is the last entry
	// of the plan's device list and is not one of them.
	rpcDevs, helperLayers := 0, 0
	for i, owner := range plan.DevOwner {
		if owner != plan.Head {
			rpcDevs++
			helperLayers += plan.DevLayers[i]
		}
	}
	ngl := plan.NGL
	if ngl > info.EngineLayers() {
		// Every layer on graphics chips: say "all" rather than a count. Some models run part
		// of their layers more than once (Inkling 2 repeats 12 of its 24 three times), and the
		// engine counts each pass as a layer, so a count from the file left half of Inkling
		// on the processor (2 words per second instead of 18.5, measured on the Mac mini).
		ngl = 999
	}
	// The largest graphics memory among the computers in the plan (see the working-memory
	// note after loading).
	var cardMax int64
	h.mu.Lock()
	for _, id := range append([]string{plan.Head}, plan.WorkerOrder...) {
		if n := h.nodes[id]; n != nil {
			for _, g := range n.Info.GPUs {
				if !g.Unified && int64(g.VRAMTotal) > cardMax {
					cardMax = int64(g.VRAMTotal)
				}
			}
		}
	}
	h.mu.Unlock()
	req := StartHeadReq{File: model.File, Vision: model.Vision, Workers: plan.WorkerOrder, NGL: ngl, Split: plan.TensorSplit, Ctx: ctxSize, RPCDevices: rpcDevs, Stream: plan.Stream, DenseBytes: info.denseBytes(), KVKeyQ8: plan.KVKeyQ8,
		ExpertCPU: plan.ExpertCPU, Sampling: samplingArgs(info),
		// Every hardware thread except where it measured slower: the disk in play, or pooled with
		// whole layers on the head's processor (see threadsFor).
		AllThreads: plan.Overflow == 0 && !(!plan.Solo && len(plan.Shares) > 0 && plan.Shares[0].CPULayers > 0)}
	if err := h.call(plan.Head, "start_head", req, nil, time.Minute); err != nil {
		done(err)
		return err
	}
	deadline := time.Now().Add(3 * time.Hour)
	for {
		if ctx.Err() != nil {
			return errors.New("stopped")
		}
		var st AgentStatus
		if err := h.call(plan.Head, "status", nil, &st, 20*time.Second); err == nil {
			if st.Ready {
				// Trust, but verify: the engine's own report must show the helpers holding
				// something when the plan gave them layers.
				h.mu.Lock()
				h.vision = st.Vision
				h.mu.Unlock()
				// Learn the engine's working memory, but only from a load that used graphics
				// chips (the only kind the figure is charged to) and only a figure that fits on
				// the largest of them: a bigger one means the system spilled it into ordinary
				// memory, a sign of a wrong plan rather than something to plan around.
				if w := workReported(st.FullLog); w > 0 && ngl > 0 && (cardMax == 0 || w <= cardMax) {
					h.cfg.Update(func(c *Config) {
						if c.WorkSeen == nil {
							c.WorkSeen = map[string]int64{}
						}
						c.WorkSeen[workKey(model.File, ctxSize)] = w
					})
				}
				if helperLayers > 0 && st.HeadLog != "" && !engineUsedHelpers(st.FullLog) {
					err := errors.New("the AI engine loaded the model without the helpers, which does not match the plan. Please report this; the technical details below show what it did")
					done(err)
					return err
				}
				break
			}
			if !st.Head {
				err := errors.New("the AI engine stopped while loading: " + lastLogLine(st.HeadLog))
				done(err)
				return err
			}
		}
		if time.Now().After(deadline) {
			err := errors.New("loading took too long")
			done(err)
			return err
		}
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
		}
	}
	done(nil)
	var st AgentStatus
	_ = h.call(plan.Head, "status", nil, &st, 20*time.Second)
	for _, p := range parts {
		msg := "Holding its part of the model"
		switch {
		case p.isHead:
			msg = "Running the model"
		case st.Links[p.id] == "direct":
			msg += " (linked straight to " + h.nodeName(plan.Head) + ")"
		case st.Links[p.id] == "relay" && plan.Head != h.id.FP:
			msg += " (reached through this computer: a direct link could not be made)"
		}
		h.setProgress(p.id, ProgressNote{Msg: msg})
	}
	return nil
}

func (h *Host) nodeName(id string) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	if n := h.nodes[id]; n != nil {
		return n.Info.Name
	}
	return "the head"
}

var rpcBuffer = regexp.MustCompile(`RPC\d+\[[^\]]*\] model buffer size =\s+([\d.]+) MiB`)

// engineUsedHelpers reads the engine's load report and says whether any helper device holds
// part of the model.
func engineUsedHelpers(log string) bool {
	for _, m := range rpcBuffer.FindAllStringSubmatch(log, -1) {
		if v, err := strconv.ParseFloat(m[1], 64); err == nil && v > 1 {
			return true
		}
	}
	return !strings.Contains(log, "model buffer size") // no report at all: cannot tell, do not block
}

var logNoise = regexp.MustCompile(`^\d+\.\d+\.\d+\.\d+ \w `)

func lastLogLine(log string) string {
	lines := strings.Split(strings.TrimSpace(log), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		l := strings.TrimSpace(lines[i])
		if l != "" && (strings.Contains(strings.ToLower(l), "error") || strings.Contains(l, " E ") || i == len(lines)-1) {
			return logNoise.ReplaceAllString(l, "")
		}
	}
	return "no details in the log"
}

// parallelDo runs fn for every item at once and returns the first error.
func parallelDo[T any](items []T, fn func(T) error) error {
	errs := make(chan error, len(items))
	for _, it := range items {
		go func(it T) { errs <- fn(it) }(it)
	}
	var first error
	for range items {
		if err := <-errs; err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Stop ends the running model everywhere.
func (h *Host) Stop() {
	h.mu.Lock()
	if h.cancel != nil {
		h.cancel()
		h.cancel = nil
	}
	if h.cluster.Phase == "idle" {
		h.mu.Unlock()
		return
	}
	h.cluster.Phase = "stopping"
	h.mu.Unlock()
	h.stopAll()
	h.mu.Lock()
	h.cluster = ClusterState{Phase: "idle"}
	h.vision = false
	h.mu.Unlock()
}

func (h *Host) stopAll() {
	h.mu.Lock()
	var ids []string
	for id, n := range h.nodes {
		if n.Online {
			ids = append(ids, id)
		}
		n.Role, n.Held, n.Status, n.Done, n.Total = "", 0, "", 0, 0
	}
	h.mu.Unlock()
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			// "stop" answers once the engines have exited; the inventory after it is fresh.
			if h.call(id, "stop", nil, nil, 60*time.Second) == nil {
				h.refreshNode(id)
			}
		}(id)
	}
	wg.Wait()
}

// Test sends one short question to the running model and records the speed.
// layoutName describes how a model ran, for the list of measured speeds.
func layoutName(p *Plan) string {
	var names []string
	for _, s := range p.Shares {
		if s.CPULayers+s.GPULayers > 0 || s.Role == "head" {
			names = append(names, s.Name)
		}
	}
	switch {
	case p.Stream:
		return names[0] + ", streaming from disk"
	case len(names) == 1:
		return names[0] + " alone"
	}
	return "pooled: " + strings.Join(names, ", ")
}

// Benchmark measures one computer and saves its profile.
func (h *Host) Benchmark(id string) (*Profile, error) {
	h.mu.Lock()
	n := h.nodes[id]
	busy := n != nil && n.Role != "" && h.cluster.Phase != "idle"
	h.mu.Unlock()
	if n == nil {
		return nil, errors.New("unknown computer")
	}
	if busy {
		return nil, errors.New(n.Info.Name + " is lending to the running model; stop it first, so the measurement is fair")
	}
	var p Profile
	if err := h.call(id, "bench", nil, &p, 2*time.Minute); err != nil {
		return nil, err
	}
	h.cfg.Update(func(c *Config) {
		if c.Profiles == nil {
			c.Profiles = map[string]*Profile{}
		}
		c.Profiles[id] = &p
	})
	return &p, nil
}

func (h *Host) Test() error {
	h.mu.Lock()
	if h.cluster.Phase != "running" {
		h.mu.Unlock()
		return errors.New("no model is running")
	}
	if h.cluster.Testing {
		h.mu.Unlock()
		return errors.New("a speed test is already running")
	}
	h.cluster.Testing = true
	h.mu.Unlock()
	go func() {
		body := `{"messages":[{"role":"user","content":"In two sentences, what is distributed inference?"}],"max_tokens":96}`
		c := http.Client{Timeout: 30 * time.Minute}
		res, err := c.Post("http://127.0.0.1:"+strconv.Itoa(portChat)+"/v1/chat/completions", "application/json", strings.NewReader(body))
		var out struct {
			Choices []struct {
				Message struct {
					Content   string `json:"content"`
					Reasoning string `json:"reasoning_content"`
				} `json:"message"`
			} `json:"choices"`
			Timings struct {
				Predicted float64 `json:"predicted_per_second"`
				Prompt    float64 `json:"prompt_per_second"`
			} `json:"timings"`
		}
		if err == nil {
			err = json.NewDecoder(res.Body).Decode(&out)
			res.Body.Close()
		}
		h.mu.Lock()
		h.cluster.Testing = false
		if err == nil {
			h.cluster.TPS, h.cluster.PTPS = out.Timings.Predicted, out.Timings.Prompt
			if m := h.cluster.Model; m != nil && h.cluster.Plan != nil && out.Timings.Predicted > 0 {
				rec := Measured{File: m.File, Layout: layoutName(h.cluster.Plan), Gen: round1(out.Timings.Predicted), Prompt: round1(out.Timings.Prompt), When: time.Now()}
				go h.cfg.Update(func(c *Config) {
					c.Measured = append([]Measured{rec}, c.Measured...)
					if len(c.Measured) > 50 {
						c.Measured = c.Measured[:50]
					}
				})
			}
			if len(out.Choices) > 0 {
				h.cluster.Reply = strings.TrimSpace(out.Choices[0].Message.Content)
				if h.cluster.Reply == "" {
					h.cluster.Reply = strings.TrimSpace(out.Choices[0].Message.Reasoning)
				}
			}
		} else {
			h.cluster.Reply = "Speed test failed: " + err.Error()
		}
		h.mu.Unlock()
	}()
	return nil
}

// workKey names a remembered working-memory figure: per model file and context size, for a
// load that uses graphics chips. (Figures from before 1.12 had no "@card" and were dropped:
// some came from loads with no layers on the card, which reserve far more.)
func workKey(file string, ctx int) string {
	return file + "@" + strconv.Itoa(ctx) + "@card"
}

var workLine = regexp.MustCompile(`(\S+) compute buffer size =\s+([\d.]+) MiB`)

// workReported is the largest working memory the engine reserved on one device (graphics cards
// and helpers; the processor's own is small), from its load report.
func workReported(log string) int64 {
	var most float64
	for _, m := range workLine.FindAllStringSubmatch(log, -1) {
		if m[1] == "CPU" {
			continue
		}
		if v, err := strconv.ParseFloat(m[2], 64); err == nil && v > most {
			most = v
		}
	}
	return int64(most * (1 << 20))
}
