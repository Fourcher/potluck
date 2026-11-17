package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"testing"
	"time"
)

// Two computers on this machine: one waits for a direct link, the other makes it and talks
// to a program behind it. Also checks that a stranger is turned away.
func TestDirectLink(t *testing.T) {
	idA, err := loadOrCreateIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	idB, err := loadOrCreateIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	idC, _ := loadOrCreateIdentity(t.TempDir())

	// The "program" behind computer A: answers every line in capitals-free echo form.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						c.Close()
						return
					}
					fmt.Fprintf(c, "echo %s", line)
				}
			}()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	a := newDirect(idA, func(target string) (int, error) {
		if target != "rpc" {
			return 0, fmt.Errorf("no such program")
		}
		return port, nil
	})
	b := newDirect(idB, func(string) (int, error) { return 0, fmt.Errorf("none") })
	c := newDirect(idC, func(string) (int, error) { return 0, fmt.Errorf("none") })

	// What the main computer does: tell A to expect B, and give B the address of A.
	introduceTo := func(from *Direct) func(to string, p int) ([]string, error) {
		return func(to string, p int) ([]string, error) {
			// The first address is a dead end, as when two computers do not share that network.
			ap, err := a.Expect(from.id.FP, []string{fmt.Sprintf("127.0.0.1:%d", p)})
			return []string{"127.0.0.1:9", fmt.Sprintf("127.0.0.1:%d", ap)}, err
		}
	}
	conn, err := b.Connect(idA.FP, "rpc", introduceTo(b))
	if err != nil {
		t.Fatalf("direct link failed: %v", err)
	}
	defer conn.Close()
	big := make([]byte, 0, 3<<20)
	for len(big) < 3<<20 {
		big = append(big, "0123456789abcdef"...)
	}
	go func() {
		fmt.Fprintf(conn, "hello\n")
		conn.Write(append(big, '\n'))
	}()
	r := bufio.NewReaderSize(conn, 1<<16)
	_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	line, err := r.ReadString('\n')
	if err != nil || line != "echo hello\n" {
		t.Fatalf("got %q, %v", line, err)
	}
	got, err := io.ReadAll(io.LimitReader(r, int64(len(big))+6))
	if err != nil || len(got) != len(big)+6 {
		t.Fatalf("large message: got %d bytes, %v", len(got), err)
	}
	// A second stream reuses the same link.
	conn2, err := b.Connect(idA.FP, "rpc", func(string, int) ([]string, error) { return nil, fmt.Errorf("should not need a new introduction") })
	if err != nil {
		t.Fatalf("second stream: %v", err)
	}
	conn2.Close()

	// C was never introduced under its own fingerprint: A must refuse it.
	lie := func(to string, p int) ([]string, error) {
		ap, err := a.start()
		return []string{fmt.Sprintf("127.0.0.1:%d", ap)}, err
	}
	if sc, err := c.Connect(idA.FP, "rpc", lie); err == nil {
		_ = sc.SetReadDeadline(time.Now().Add(3 * time.Second))
		fmt.Fprintf(sc, "hi\n")
		buf := make([]byte, 16)
		if n, _ := sc.Read(buf); n > 0 {
			t.Fatalf("a computer that was not introduced got an answer: %q", buf[:n])
		}
	}
	// And B must refuse to talk to the wrong computer at the right address.
	if _, err := b.Connect(idC.FP, "rpc", lie); err == nil {
		t.Fatal("B accepted a computer with the wrong fingerprint")
	}
}

// The helper restarts (new process, new port) while the head still holds its old link. The
// next connection must notice and make a fresh link, not hand back a dead one.
func TestDirectLinkSurvivesHelperRestart(t *testing.T) {
	idA, _ := loadOrCreateIdentity(t.TempDir())
	idB, _ := loadOrCreateIdentity(t.TempDir())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	local := func(string) (int, error) { return port, nil }
	a := newDirect(idA, local)
	b := newDirect(idB, func(string) (int, error) { return 0, fmt.Errorf("none") })
	cur := a
	introduce := func(to string, p int) ([]string, error) {
		ap, err := cur.Expect(idB.FP, []string{fmt.Sprintf("127.0.0.1:%d", p)})
		return []string{fmt.Sprintf("127.0.0.1:%d", ap)}, err
	}
	roundTrip := func() error {
		c, err := b.Connect(idA.FP, "rpc", introduce)
		if err != nil {
			return err
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := c.Write([]byte("ping")); err != nil {
			return err
		}
		buf := make([]byte, 4)
		if _, err := io.ReadFull(c, buf); err != nil {
			return err
		}
		if string(buf) != "ping" {
			return fmt.Errorf("got %q", buf)
		}
		return nil
	}
	if err := roundTrip(); err != nil {
		t.Fatalf("first link: %v", err)
	}
	// The helper process dies without saying goodbye, and a new one starts.
	a.mu.Lock()
	a.tr.Conn.Close()
	a.mu.Unlock()
	cur = newDirect(idA, local)
	start := time.Now()
	if err := roundTrip(); err != nil {
		t.Fatalf("after the helper restarted: %v", err)
	}
	if time.Since(start) > 12*time.Second {
		t.Fatalf("recovering took %s", time.Since(start))
	}
}
