package main

// Direct links between the computer that runs the model (the head) and its helpers.
//
// Without this, everything the head and a helper say to each other is relayed through the
// main computer: twice the airtime on Wi-Fi, plus whatever that third computer's own link
// adds. A direct link is the same encrypted, fingerprint-pinned conversation carried
// straight from one to the other.
//
// Neither side needs a firewall permission. The main computer introduces the two (each
// learns the other's address and fingerprint over the link it already trusts), both send a
// few packets to each other at the same time, and every common firewall then lets the
// answers through because each computer spoke first. The transport is QUIC over UDP, which
// also sidesteps a Windows setting that caps how fast a TCP connection may receive.
// When the direct link cannot be made, the relay is used as before.

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

const directALPN = "potluck-direct/1"

var directQUIC = &quic.Config{
	HandshakeIdleTimeout:           4 * time.Second,
	MaxIdleTimeout:                 45 * time.Second,
	KeepAlivePeriod:                5 * time.Second,
	InitialStreamReceiveWindow:     4 << 20,
	MaxStreamReceiveWindow:         16 << 20,
	InitialConnectionReceiveWindow: 8 << 20,
	MaxConnectionReceiveWindow:     48 << 20,
	MaxIncomingStreams:             64,
}

// Direct owns this computer's end of every direct link.
type Direct struct {
	id        *Identity
	localPort func(target string) (int, error)

	mu    sync.Mutex
	tr    *quic.Transport
	port  int
	allow map[string]time.Time // fingerprints the main computer has introduced, and until when
	conns map[string]*quic.Conn
	bad   map[string]time.Time // peers a direct link could not reach: when to try again
}

func newDirect(id *Identity, localPort func(string) (int, error)) *Direct {
	return &Direct{id: id, localPort: localPort, allow: map[string]time.Time{}, conns: map[string]*quic.Conn{}, bad: map[string]time.Time{}}
}

func (d *Direct) tlsConf() *tls.Config {
	c := d.id.tlsConfig()
	c.NextProtos = []string{directALPN}
	return c
}

// start opens the UDP port and begins accepting links. It is safe to call often.
func (d *Direct) start() (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.tr != nil {
		return d.port, nil
	}
	uc, err := net.ListenUDP("udp4", &net.UDPAddr{})
	if err != nil {
		return 0, err
	}
	tr := &quic.Transport{Conn: uc}
	ln, err := tr.Listen(d.tlsConf(), directQUIC)
	if err != nil {
		uc.Close()
		return 0, err
	}
	d.tr, d.port = tr, uc.LocalAddr().(*net.UDPAddr).Port
	go d.accept(ln)
	// The greeting packets are not QUIC; read them away so they do not pile up.
	go func() {
		buf := make([]byte, 64)
		for {
			if _, _, err := tr.ReadNonQUICPacket(context.Background(), buf); err != nil {
				return
			}
		}
	}()
	return d.port, nil
}

// greet sends a few small packets to each address the other side may be at. Their only job
// is to make this computer's firewall expect the answers.
func (d *Direct) greet(addrs []*net.UDPAddr, n int) {
	d.mu.Lock()
	tr := d.tr
	d.mu.Unlock()
	if tr == nil {
		return
	}
	for i := 0; i < n; i++ {
		for _, a := range addrs {
			_, _ = tr.WriteTo([]byte{0, 'p', 'o', 't', 'l', 'u', 'c', 'k'}, a)
		}
		time.Sleep(120 * time.Millisecond)
	}
}

// localIPv4s lists this computer's own addresses, so that two computers that reach the main
// computer over different networks (home network and a VPN, say) can still find a network
// they share.
func localIPv4s() []string {
	var out []string
	ifs, _ := net.Interfaces()
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok {
				if ip4 := ipn.IP.To4(); ip4 != nil && !ip4.IsLoopback() && !ip4.IsLinkLocalUnicast() {
					out = append(out, ip4.String())
				}
			}
		}
	}
	return out
}

// rankAddr orders candidate addresses: the home network first, then VPN-style addresses.
func rankAddr(ip net.IP) int {
	ip4 := ip.To4()
	switch {
	case ip4 == nil:
		return 3
	case ip4.IsPrivate():
		return 0
	case ip4[0] == 100 && ip4[1]&0xC0 == 64: // 100.64.0.0/10, used by Tailscale and similar
		return 1
	}
	return 2
}

// resolveCandidates turns "ip:port" strings into addresses, best first, without repeats.
func resolveCandidates(addrs []string) []*net.UDPAddr {
	seen := map[string]bool{}
	var out []*net.UDPAddr
	for _, a := range addrs {
		ua, err := net.ResolveUDPAddr("udp4", a)
		if err != nil || ua.IP == nil || ua.Port == 0 || seen[ua.String()] {
			continue
		}
		seen[ua.String()] = true
		out = append(out, ua)
	}
	sort.SliceStable(out, func(i, j int) bool { return rankAddr(out[i].IP) < rankAddr(out[j].IP) })
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

// Expect prepares for a link from the computer with this fingerprint, which may be at any of
// these addresses, and returns the UDP port it should talk to. Only the main computer may
// ask for this.
func (d *Direct) Expect(fp string, addrs []string) (int, error) {
	port, err := d.start()
	if err != nil {
		return 0, err
	}
	cands := resolveCandidates(addrs)
	if len(cands) == 0 {
		return 0, errors.New("no usable address")
	}
	d.mu.Lock()
	d.allow[fp] = time.Now().Add(24 * time.Hour)
	d.mu.Unlock()
	go d.greet(cands, 40)
	return port, nil
}

func (d *Direct) accept(ln *quic.Listener) {
	for {
		c, err := ln.Accept(context.Background())
		if err != nil {
			return
		}
		go d.serve(c)
	}
}

func quicPeerFP(c *quic.Conn) string {
	certs := c.ConnectionState().TLS.PeerCertificates
	if len(certs) == 0 {
		return ""
	}
	return fingerprint(certs[0].Raw)
}

// serve answers one incoming link: every stream on it asks for one local program ("rpc").
func (d *Direct) serve(c *quic.Conn) {
	fp := quicPeerFP(c)
	d.mu.Lock()
	until, ok := d.allow[fp]
	d.mu.Unlock()
	if fp == "" || !ok || time.Now().After(until) {
		_ = c.CloseWithError(1, "not introduced")
		return
	}
	for {
		s, err := c.AcceptStream(context.Background())
		if err != nil {
			return
		}
		go func() {
			sc := &streamConn{Stream: s, c: c}
			_ = s.SetReadDeadline(time.Now().Add(10 * time.Second))
			target, err := readShortLine(s)
			_ = s.SetReadDeadline(time.Time{})
			if err != nil {
				sc.Close()
				return
			}
			port, err := d.localPort(target)
			if err != nil {
				sc.Close()
				return
			}
			local, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 5*time.Second)
			if err != nil {
				sc.Close()
				return
			}
			// Tell the other side the program is there. It waits for this before using the
			// stream, which is also how it notices a link whose far end has gone away.
			if _, err := s.Write([]byte{1}); err != nil {
				sc.Close()
				local.Close()
				return
			}
			pipe(sc, local)
		}()
	}
}

func readShortLine(s *quic.Stream) (string, error) {
	var b []byte
	one := make([]byte, 1)
	for len(b) < 64 {
		if _, err := s.Read(one); err != nil {
			return "", err
		}
		if one[0] == '\n' {
			return string(b), nil
		}
		b = append(b, one[0])
	}
	return "", errors.New("line too long")
}

// Connect opens a stream to a program on the computer with this fingerprint. introduce asks
// the main computer to bring the two together and returns the addresses the other side may
// be reached at.
func (d *Direct) Connect(fp, target string, introduce func(to string, port int) ([]string, error)) (net.Conn, error) {
	d.mu.Lock()
	c := d.conns[fp]
	retry, wasBad := d.bad[fp]
	d.mu.Unlock()
	if c != nil {
		if sc, err := d.open(c, target); err == nil {
			return sc, nil
		}
		// The kept link is dead (the other computer restarted, say): make a new one.
		_ = c.CloseWithError(3, "stale")
		d.mu.Lock()
		delete(d.conns, fp)
		d.mu.Unlock()
	}
	if wasBad && time.Now().Before(retry) {
		return nil, errors.New("direct link not available")
	}
	fail := func(err error) (net.Conn, error) {
		d.mu.Lock()
		d.bad[fp] = time.Now().Add(3 * time.Minute)
		d.mu.Unlock()
		return nil, err
	}
	port, err := d.start()
	if err != nil {
		return fail(err)
	}
	addrs, err := introduce(fp, port)
	if err != nil {
		return fail(err)
	}
	cands := resolveCandidates(addrs)
	if len(cands) == 0 {
		return fail(errors.New("no usable address"))
	}
	go d.greet(cands, 30)
	time.Sleep(300 * time.Millisecond) // let the greetings cross before the real handshake
	d.mu.Lock()
	tr := d.tr
	d.mu.Unlock()
	last := errors.New("no answer")
	for _, ua := range cands {
		ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
		c, err = tr.Dial(ctx, ua, d.tlsConf(), directQUIC)
		cancel()
		if err != nil {
			last = err
			continue
		}
		if quicPeerFP(c) != fp {
			_ = c.CloseWithError(2, "wrong computer")
			last = errors.New("the computer at that address is not the one expected")
			continue
		}
		sc, err := d.open(c, target)
		if err != nil {
			last = err
			continue
		}
		d.mu.Lock()
		d.conns[fp] = c
		delete(d.bad, fp)
		d.mu.Unlock()
		return sc, nil
	}
	return fail(last)
}

func (d *Direct) open(c *quic.Conn, target string) (net.Conn, error) {
	if c.Context().Err() != nil {
		return nil, errors.New("link closed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	s, err := c.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	sc := &streamConn{Stream: s, c: c}
	if _, err := s.Write([]byte(target + "\n")); err != nil {
		sc.Close()
		return nil, err
	}
	// Wait for the far end to say the program is there. A link kept from earlier may lead to
	// a computer or program that has since restarted; this is where that shows.
	ack := make([]byte, 1)
	_ = s.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, err = io.ReadFull(s, ack)
	_ = s.SetReadDeadline(time.Time{})
	if err != nil || ack[0] != 1 {
		sc.Close()
		return nil, errors.New("no answer on the direct link")
	}
	return sc, nil
}

// streamConn lets a QUIC stream stand in wherever a network connection is expected.
type streamConn struct {
	*quic.Stream
	c    *quic.Conn
	once sync.Once
}

func (s *streamConn) Close() error {
	var err error
	s.once.Do(func() {
		s.Stream.CancelRead(0)
		err = s.Stream.Close()
	})
	return err
}
func (s *streamConn) LocalAddr() net.Addr  { return s.c.LocalAddr() }
func (s *streamConn) RemoteAddr() net.Addr { return s.c.RemoteAddr() }

// addrHost returns the address part of a connection's end ("192.168.1.20:5000" -> "192.168.1.20").
func addrHost(a net.Addr) string {
	h, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return strings.TrimSpace(a.String())
	}
	return h
}
