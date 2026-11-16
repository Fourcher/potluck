package main

import (
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// JoinToken is what a USB kit or join link carries: where the main computer is, how to
// recognise it, and a secret that lets this computer in without a click.
type JoinToken struct {
	Addrs  []string `json:"addrs"`
	Port   int      `json:"port"`
	FP     string   `json:"fp"`
	Secret string   `json:"secret"`
	Name   string   `json:"name"`
}

// Helper is a computer that lends its memory and processor to a main computer.
type Helper struct {
	a   *Agent
	id  *Identity
	cfg *ConfigStore

	mu     sync.Mutex
	phase  string // searching | pairing | connected | waiting
	code   string
	note   string
	token  *JoinToken
	manual string // address typed by hand
	peer   *Peer
	quiet  map[string]time.Time // addresses whose link went silent, and when
}

// HelperState is shown on the helper's own page.
type HelperState struct {
	Phase    string `json:"phase"`
	Code     string `json:"code"`
	Note     string `json:"note"`
	MainName string `json:"mainName"`
	MainAddr string `json:"mainAddr"`
}

func newHelper(a *Agent, id *Identity, cfg *ConfigStore, token *JoinToken) *Helper {
	return &Helper{a: a, id: id, cfg: cfg, token: token, phase: "searching"}
}

func (w *Helper) State() HelperState {
	w.mu.Lock()
	defer w.mu.Unlock()
	c := w.cfg.Get()
	return HelperState{Phase: w.phase, Code: w.code, Note: w.note, MainName: c.MainName, MainAddr: c.MainAddr}
}

func (w *Helper) set(phase, code, note string) {
	w.mu.Lock()
	w.phase, w.code, w.note = phase, code, note
	w.mu.Unlock()
}

// SetManual lets the person type the main computer's address when discovery cannot find it.
func (w *Helper) SetManual(addr string) {
	addr = strings.TrimSpace(addr)
	if addr != "" && !strings.Contains(addr, ":") {
		addr += ":" + strconv.Itoa(portLink)
	}
	w.mu.Lock()
	w.manual = addr
	w.mu.Unlock()
}

// Forget drops the pairing so this computer can join a different main computer.
func (w *Helper) Forget() {
	w.cfg.Update(func(c *Config) { c.MainFP, c.MainAddr, c.MainName = "", "", "" })
	w.mu.Lock()
	p := w.peer
	w.token = nil
	w.mu.Unlock()
	if p != nil {
		p.Close()
	}
}

// Run keeps this computer connected to its main computer, forever.
func (w *Helper) Run() {
	for round := 0; ; round++ {
		// First the places we already know about. Only when none of them answers do we go
		// looking on the network.
		known, wantFP, secret := w.candidates()
		connected := w.try(known, wantFP, secret)
		if !connected {
			found := discover(1500*time.Millisecond, wantFP)
			if len(found) == 0 && round%8 == 0 {
				// Firewalls often drop the answer to a broadcast. Knocking on each address of the
				// local network is an ordinary outgoing connection, which they let through.
				found = scanSubnets()
				time.Sleep(2 * time.Second) // let the network settle after the knocking
			}
			connected = w.try(found, wantFP, secret)
		}
		if !connected {
			if w.cfg.Get().MainFP != "" {
				w.set("waiting", "", "Looking for your main computer. Is Potluck open on it?")
			} else {
				w.set("searching", "", "Looking for a main computer on this network.")
			}
			time.Sleep(2 * time.Second)
		}
	}
}

// try connects to the first address that works and stays until that link ends.
func (w *Helper) try(addrs []string, wantFP, secret string) bool {
	for _, addr := range addrs {
		err := w.session(addr, wantFP, secret)
		if err == nil {
			return true
		}
		fmt.Printf("%s could not join %s: %v\n", time.Now().Format("15:04:05"), addr, err)
		if runtime.GOOS == "darwin" && strings.Contains(err.Error(), "no route to host") {
			// macOS answers "no route to host" when its Local Network privacy rule blocks an app.
			w.set("waiting", "", "macOS is keeping Potluck off your home network. Open System Settings, Privacy & Security, Local Network, and switch on Potluck (or Terminal, if you started it from there).")
			time.Sleep(4 * time.Second)
		}
		if errors.Is(err, errDeclined) {
			w.set("waiting", "", "The main computer declined this computer. Trying again in a moment.")
			time.Sleep(10 * time.Second)
		}
	}
	return false
}

var errDeclined = errors.New("declined")

// candidates lists where the main computer might be, best guess first.
func (w *Helper) candidates() (addrs []string, fp, secret string) {
	c := w.cfg.Get()
	w.mu.Lock()
	tok, manual := w.token, w.manual
	w.mu.Unlock()
	fp = c.MainFP
	if manual != "" {
		addrs = append(addrs, manual)
	}
	if tok != nil && (fp == "" || strings.HasPrefix(fp, tok.FP) || strings.HasPrefix(tok.FP, fp)) {
		if fp == "" {
			fp = tok.FP
		}
		secret = tok.Secret
		for _, a := range tok.Addrs {
			addrs = append(addrs, net.JoinHostPort(a, strconv.Itoa(tok.Port)))
		}
	}
	if c.MainAddr != "" {
		addrs = append(addrs, c.MainAddr)
	}
	addrs = append(addrs, c.MainAlts...)
	seen := map[string]bool{}
	var uniq []string
	for _, a := range addrs {
		if !seen[a] {
			seen[a] = true
			uniq = append(uniq, a)
		}
	}
	// A path that recently went silent goes to the back, so the next one gets a turn.
	w.mu.Lock()
	var good, bad []string
	for _, a := range uniq {
		if t, ok := w.quiet[a]; ok && time.Since(t) < 3*time.Minute {
			bad = append(bad, a)
		} else {
			good = append(good, a)
		}
	}
	w.mu.Unlock()
	return append(good, bad...), fp, secret
}

// discover shouts on the local network and collects main computers that answer.
func discover(wait time.Duration, wantFP string) []string {
	pc, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return nil
	}
	defer pc.Close()
	msg, _ := json.Marshal(beacon{Potluck: "find"})
	targets := []string{"255.255.255.255"}
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagBroadcast == 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if ip4 := ipn.IP.To4(); ip4 != nil && len(ipn.Mask) == 4 {
					b := make(net.IP, 4)
					for k := 0; k < 4; k++ {
						b[k] = ip4[k] | ^ipn.Mask[k]
					}
					targets = append(targets, b.String())
				}
			}
		}
	}
	for _, t := range targets {
		_, _ = pc.WriteTo(msg, &net.UDPAddr{IP: net.ParseIP(t), Port: portLink})
	}
	_ = pc.SetReadDeadline(time.Now().Add(wait))
	var found []string
	buf := make([]byte, 2048)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return found
		}
		var b beacon
		if json.Unmarshal(buf[:n], &b) != nil || b.Potluck != "here" {
			continue
		}
		if wantFP != "" && !strings.HasPrefix(b.FP, wantFP) && !strings.HasPrefix(wantFP, b.FP) {
			continue
		}
		found = append(found, net.JoinHostPort(addr.(*net.UDPAddr).IP.String(), strconv.Itoa(b.Port)))
	}
}

// scanSubnets looks for a computer listening on the Potluck port on this computer's home
// networks (up to 1022 addresses per network).
func scanSubnets() []string {
	var targets []string
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipn.IP.To4()
			ones, bits := ipn.Mask.Size()
			if ip4 == nil || bits != 32 || ones < 22 || ones > 30 || !ip4.IsPrivate() {
				continue
			}
			base := uint32(ip4[0])<<24 | uint32(ip4[1])<<16 | uint32(ip4[2])<<8 | uint32(ip4[3])
			mask := uint32(0xffffffff) << (32 - ones)
			for h := uint32(1); h < ^mask; h++ {
				v := base&mask | h
				if v != base {
					targets = append(targets, net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v)).String())
				}
			}
		}
	}
	var mu sync.Mutex
	var found []string
	sem := make(chan struct{}, 24)
	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		sem <- struct{}{}
		go func(t string) {
			defer wg.Done()
			defer func() { <-sem }()
			addr := net.JoinHostPort(t, strconv.Itoa(portLink))
			c, err := net.DialTimeout("tcp", addr, 400*time.Millisecond)
			if err != nil {
				return
			}
			c.Close()
			mu.Lock()
			found = append(found, addr)
			mu.Unlock()
		}(t)
	}
	wg.Wait()
	return found
}

func (w *Helper) dial(addr, wantFP string) (*tls.Conn, string, error) {
	d := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 20 * time.Second}
	c, err := tls.DialWithDialer(d, "tcp", addr, w.id.tlsConfig())
	if err != nil {
		return nil, "", err
	}
	fp, err := peerFP(c)
	if err != nil {
		c.Close()
		return nil, "", err
	}
	if wantFP != "" && !strings.HasPrefix(fp, wantFP) {
		c.Close()
		return nil, "", fmt.Errorf("the computer at %s is not the main computer this one is paired with", addr)
	}
	return c, fp, nil
}

// session connects to the main computer at addr and serves it until the link drops.
// It returns nil only after a link that was fully established has ended.
func (w *Helper) session(addr, wantFP, secret string) error {
	c, fp, err := w.dial(addr, wantFP)
	if err != nil {
		return err
	}
	info := w.a.Info()
	if err := writeLine(c, Hello{Kind: "control", Name: info.Name, OS: runtime.GOOS, Secret: secret, Version: version}); err != nil {
		c.Close()
		return err
	}
	var rep HelloReply
	if err := readLine(c, &rep, 20*time.Second); err != nil {
		c.Close()
		return err
	}
	if rep.OK && rep.State == "pending" {
		// Not known yet: both screens show the same code; someone clicks Approve on the main computer.
		w.set("pairing", pairCode(fp, w.id.FP), "Waiting for you to approve this computer on "+nonEmpty(rep.Name, "the main computer")+".")
		if err := readLine(c, &rep, 30*time.Minute); err != nil {
			c.Close()
			return err
		}
	}
	if !rep.OK {
		c.Close()
		if rep.Err == "declined" {
			return errDeclined
		}
		return errors.New(rep.Err)
	}
	w.cfg.Update(func(cf *Config) {
		cf.MainFP, cf.MainAddr, cf.MainName = fp, addr, rep.Name
		if len(rep.Addrs) > 0 {
			cf.MainAlts = rep.Addrs
		}
	})
	peer := newPeer(c)
	// The main computer checks in every few seconds. If it goes silent the path is broken
	// (seen between two Macs on the same Wi-Fi): hang up and try its next address.
	var silent atomic.Bool
	go func() {
		for {
			select {
			case <-peer.closed:
				return
			case <-time.After(5 * time.Second):
				if peer.Quiet() > 25*time.Second {
					silent.Store(true)
					fmt.Printf("%s the link to %s went silent, trying another path\n", time.Now().Format("15:04:05"), addr)
					peer.Close()
					return
				}
			}
		}
	}()
	w.a.dialTunnel = func(to, target string) (net.Conn, error) {
		t, _, err := w.dial(addr, fp)
		if err != nil {
			return nil, err
		}
		if err := writeLine(t, Hello{Kind: "tunnel", To: to, Target: target}); err != nil {
			t.Close()
			return nil, err
		}
		var r HelloReply
		if err := readLine(t, &r, 20*time.Second); err != nil || !r.OK {
			t.Close()
			return nil, errors.New(nonEmpty(r.Err, "tunnel refused"))
		}
		return t, nil
	}
	w.a.notify = peer.Notify
	peer.OnReq = func(method string, data json.RawMessage) (any, error) {
		if method == "open_tunnel" {
			var in struct{ Session, Target string }
			if err := json.Unmarshal(data, &in); err != nil {
				return nil, err
			}
			port, err := w.a.localPort(in.Target)
			if err != nil {
				return nil, err
			}
			go func() {
				t, _, err := w.dial(addr, fp)
				if err != nil {
					return
				}
				if writeLine(t, Hello{Kind: "accept", Session: in.Session}) != nil {
					t.Close()
					return
				}
				var r HelloReply
				if err := readLine(t, &r, 20*time.Second); err != nil || !r.OK {
					t.Close()
					return
				}
				local, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 5*time.Second)
				if err != nil {
					t.Close()
					return
				}
				pipe(t, local)
			}()
			return nil, nil
		}
		return w.a.Handle(method, data)
	}
	w.mu.Lock()
	w.peer = peer
	w.mu.Unlock()
	w.set("connected", "", "")
	peer.Run()
	w.mu.Lock()
	w.peer = nil
	w.mu.Unlock()
	w.a.notify = nil
	if silent.Load() {
		w.mu.Lock()
		if w.quiet == nil {
			w.quiet = map[string]time.Time{}
		}
		w.quiet[addr] = time.Now()
		w.mu.Unlock()
	}
	// The main computer is gone: stop lending memory so this computer is fully yours again.
	w.a.Stop()
	w.set("waiting", "", "Lost the connection to the main computer. Reconnecting.")
	return nil
}

func nonEmpty(s, alt string) string {
	if strings.TrimSpace(s) == "" {
		return alt
	}
	return s
}
