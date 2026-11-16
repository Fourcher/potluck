package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// Hello is the first line sent on every connection to the main computer.
type Hello struct {
	Kind    string `json:"kind"`              // control | tunnel | accept
	Name    string `json:"name,omitempty"`    // sender's display name
	OS      string `json:"os,omitempty"`      //
	Secret  string `json:"secret,omitempty"`  // join secret from a USB kit or join link
	To      string `json:"to,omitempty"`      // tunnel: node id to reach
	Target  string `json:"target,omitempty"`  // tunnel: "rpc" or "api"
	Session string `json:"session,omitempty"` // accept: which pending tunnel this answers
	Version string `json:"version,omitempty"`
}

// HelloReply answers a Hello.
type HelloReply struct {
	OK    bool     `json:"ok"`
	Err   string   `json:"err,omitempty"`
	State string   `json:"state,omitempty"` // paired | pending
	Code  string   `json:"code,omitempty"`
	Name  string   `json:"name,omitempty"`
	Addrs []string `json:"addrs,omitempty"` // every address the main computer can be reached at
}

func writeLine(c net.Conn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_ = c.SetWriteDeadline(time.Now().Add(30 * time.Second))
	_, err = c.Write(append(b, '\n'))
	_ = c.SetWriteDeadline(time.Time{})
	return err
}

// readLine reads one JSON line without buffering past it, so the connection can be handed
// over to raw byte copying afterwards.
func readLine(c net.Conn, v any, timeout time.Duration) error {
	if timeout > 0 {
		_ = c.SetReadDeadline(time.Now().Add(timeout))
		defer c.SetReadDeadline(time.Time{})
	}
	var buf []byte
	one := make([]byte, 1)
	for len(buf) < 1<<16 {
		if _, err := c.Read(one); err != nil {
			return err
		}
		if one[0] == '\n' {
			return json.Unmarshal(buf, v)
		}
		buf = append(buf, one[0])
	}
	return errors.New("line too long")
}

// Msg is one message on a control connection.
type Msg struct {
	T  string          `json:"t"` // req | res | evt
	ID uint64          `json:"id,omitempty"`
	M  string          `json:"m,omitempty"`
	D  json.RawMessage `json:"d,omitempty"`
	E  string          `json:"e,omitempty"`
}

// Peer is a control connection that both sides can send requests over.
type Peer struct {
	c      net.Conn
	r      *bufio.Reader
	wmu    sync.Mutex
	mu     sync.Mutex
	next   uint64
	pend   map[uint64]chan Msg
	OnReq  func(method string, data json.RawMessage) (any, error)
	OnEvt  func(method string, data json.RawMessage)
	closed chan struct{}
	once   sync.Once
	lastRx atomic.Int64 // unix time of the last message received
}

// Quiet reports how long it has been since the other side said anything.
func (p *Peer) Quiet() time.Duration { return time.Since(time.Unix(p.lastRx.Load(), 0)) }

func newPeer(c net.Conn) *Peer {
	p := &Peer{c: c, r: bufio.NewReaderSize(c, 64<<10), pend: map[uint64]chan Msg{}, closed: make(chan struct{})}
	p.lastRx.Store(time.Now().Unix())
	return p
}

func (p *Peer) send(m Msg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	_ = p.c.SetWriteDeadline(time.Now().Add(30 * time.Second))
	_, err = p.c.Write(append(b, '\n'))
	return err
}

// Call sends a request and waits for the answer.
func (p *Peer) Call(method string, in, out any, timeout time.Duration) error {
	var raw json.RawMessage
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		raw = b
	}
	ch := make(chan Msg, 1)
	p.mu.Lock()
	p.next++
	id := p.next
	p.pend[id] = ch
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.pend, id); p.mu.Unlock() }()
	if err := p.send(Msg{T: "req", ID: id, M: method, D: raw}); err != nil {
		return err
	}
	if timeout <= 0 {
		timeout = 24 * time.Hour
	}
	select {
	case m := <-ch:
		if m.E != "" {
			return errors.New(m.E)
		}
		if out != nil && len(m.D) > 0 {
			return json.Unmarshal(m.D, out)
		}
		return nil
	case <-p.closed:
		return errors.New("connection lost")
	case <-time.After(timeout):
		return fmt.Errorf("%s timed out", method)
	}
}

// Notify sends a one-way event.
func (p *Peer) Notify(method string, in any) {
	b, _ := json.Marshal(in)
	_ = p.send(Msg{T: "evt", M: method, D: b})
}

func (p *Peer) Close() {
	p.once.Do(func() { close(p.closed); _ = p.c.Close() })
}

// Run reads messages until the connection ends.
func (p *Peer) Run() {
	defer p.Close()
	for {
		line, err := p.r.ReadBytes('\n')
		if err != nil {
			return
		}
		p.lastRx.Store(time.Now().Unix())
		var m Msg
		if json.Unmarshal(line, &m) != nil {
			continue
		}
		switch m.T {
		case "req":
			go func(m Msg) {
				res := Msg{T: "res", ID: m.ID}
				if p.OnReq == nil {
					res.E = "not supported"
				} else if out, err := p.OnReq(m.M, m.D); err != nil {
					res.E = err.Error()
				} else if out != nil {
					res.D, _ = json.Marshal(out)
				}
				_ = p.send(res)
			}(m)
		case "res":
			p.mu.Lock()
			ch := p.pend[m.ID]
			p.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		case "evt":
			if p.OnEvt != nil {
				p.OnEvt(m.M, m.D)
			}
		}
	}
}

// pipe copies bytes both ways until either side closes.
func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		buf := make([]byte, 256<<10)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				if _, werr := dst.Write(buf[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		done <- struct{}{}
	}
	go cp(a, b)
	go cp(b, a)
	<-done
	_ = a.Close()
	_ = b.Close()
	<-done
}
