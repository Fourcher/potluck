package main

// Browser control: the agent drives a real web browser on this computer, so tasks can use web
// sites the way a person does: open pages, read them, click, type, submit. It works with any
// model, because pages are given to the model as text, with every link, button and field
// numbered ("[12] button: Sign in"), and the model acts on those numbers.
//
// Potluck starts its own window of Chrome (or Chromium, Edge, Brave) with a profile of its own
// in the data folder, so the person's everyday browser is untouched; they can sign in to sites
// in that window once and the agent keeps the session. It talks to it over the Chrome DevTools
// Protocol on this computer only (127.0.0.1).

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type browserCtl struct {
	mu      sync.Mutex
	dataDir string
	cmd     *exec.Cmd
	port    int
	ws      *wsConn
	nextID  atomic.Int64
	pending map[int64]chan json.RawMessage
	// One task uses the browser at a time: tasks share one page, so another task's reads and
	// clicks would land on this one's page and its numbered elements. The hold lapses when
	// the task ends or has not used the browser for holdFor.
	holder string
	heldAt time.Time
}

const holdFor = 10 * time.Minute

// hold gives the browser to the task who, or says which task has it.
func (b *browserCtl) hold(who string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.holder != "" && b.holder != who && time.Since(b.heldAt) < holdFor {
		return fmt.Errorf("the browser is in use by another task; try again when it has finished (or in %d minutes)", int(time.Until(b.heldAt.Add(holdFor)).Minutes())+1)
	}
	b.holder, b.heldAt = who, time.Now()
	return nil
}

// release lets another task use the browser.
func (b *browserCtl) release(who string) {
	b.mu.Lock()
	if b.holder == who {
		b.holder = ""
	}
	b.mu.Unlock()
}

var refRe = regexp.MustCompile(`^[0-9]{1,6}$`)

var browsers = &browserCtl{pending: map[int64]chan json.RawMessage{}}

// findBrowser returns a Chromium-family browser installed on this computer.
func findBrowser() string {
	var c []string
	switch runtime.GOOS {
	case "darwin":
		for _, a := range []string{"Google Chrome", "Chromium", "Microsoft Edge", "Brave Browser"} {
			c = append(c, "/Applications/"+a+".app/Contents/MacOS/"+a)
		}
	case "windows":
		for _, base := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LocalAppData")} {
			c = append(c, filepath.Join(base, `Google\Chrome\Application\chrome.exe`), filepath.Join(base, `Microsoft\Edge\Application\msedge.exe`),
				filepath.Join(base, `BraveSoftware\Brave-Browser\Application\brave.exe`), filepath.Join(base, `Chromium\Application\chrome.exe`))
		}
	default:
		for _, n := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "microsoft-edge", "brave-browser"} {
			if p, err := exec.LookPath(n); err == nil {
				c = append(c, p)
			}
		}
	}
	for _, p := range c {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return ""
}

// ensure starts Potluck's browser window if it is not running, and connects to its page.
func (b *browserCtl) ensure(ctx context.Context) error {
	b.mu.Lock()
	if b.ws != nil && b.cmd != nil && b.cmd.ProcessState == nil {
		b.mu.Unlock()
		return nil
	}
	b.mu.Unlock()
	bin := findBrowser()
	if bin == "" {
		return errors.New("no Chrome, Chromium, Edge or Brave is installed on this computer; install one to let the agent use web sites")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	profile := filepath.Join(b.dataDir, "browser-profile")
	_ = os.MkdirAll(profile, 0o700)
	cmd := exec.Command(bin, "--remote-debugging-port="+fmt.Sprint(port), "--remote-debugging-address=127.0.0.1",
		"--user-data-dir="+profile, "--no-first-run", "--no-default-browser-check", "--new-window", "about:blank")
	ownGroup(cmd) // so closing it ends its helper processes too
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start the browser: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	// Wait for it to answer, then attach to its first page.
	var target string
	for i := 0; i < 60 && target == ""; i++ {
		time.Sleep(250 * time.Millisecond)
		var list []struct {
			Type string `json:"type"`
			WS   string `json:"webSocketDebuggerUrl"`
		}
		res, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/json/list", port))
		if err != nil {
			continue
		}
		_ = json.NewDecoder(res.Body).Decode(&list)
		res.Body.Close()
		for _, t := range list {
			if t.Type == "page" && t.WS != "" {
				target = t.WS
				break
			}
		}
	}
	if target == "" {
		killTree(cmd.Process.Pid)
		return errors.New("the browser started but did not open a page to control")
	}
	ws, err := dialWS(target)
	if err != nil {
		killTree(cmd.Process.Pid)
		return err
	}
	b.mu.Lock()
	b.cmd, b.port, b.ws = cmd, port, ws
	b.mu.Unlock()
	go b.readLoop(ws)
	for _, m := range []string{"Page.enable", "Runtime.enable"} {
		if _, err := b.call(ctx, m, nil); err != nil {
			return err
		}
	}
	return nil
}

func (b *browserCtl) readLoop(ws *wsConn) {
	for {
		msg, err := ws.read()
		if err != nil {
			b.mu.Lock()
			if b.ws == ws {
				b.ws = nil
			}
			for id, ch := range b.pending {
				close(ch)
				delete(b.pending, id)
			}
			b.mu.Unlock()
			return
		}
		var m struct {
			ID int64 `json:"id"`
		}
		if json.Unmarshal(msg, &m) == nil && m.ID != 0 {
			b.mu.Lock()
			ch := b.pending[m.ID]
			delete(b.pending, m.ID)
			b.mu.Unlock()
			if ch != nil {
				ch <- msg
			}
		}
	}
}

func (b *browserCtl) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	b.mu.Lock()
	ws := b.ws
	if ws == nil {
		b.mu.Unlock()
		return nil, errors.New("the browser window was closed")
	}
	id := b.nextID.Add(1)
	ch := make(chan json.RawMessage, 1)
	b.pending[id] = ch
	b.mu.Unlock()
	req, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err := ws.write(req); err != nil {
		return nil, err
	}
	select {
	case raw, ok := <-ch:
		if !ok {
			return nil, errors.New("the browser window was closed")
		}
		var res struct {
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, err
		}
		if res.Error != nil {
			return nil, errors.New(res.Error.Message)
		}
		return res.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(60 * time.Second):
		return nil, errors.New("the browser did not answer within a minute")
	}
}

// eval runs JavaScript in the page and returns its value.
func (b *browserCtl) eval(ctx context.Context, js string) (json.RawMessage, error) {
	raw, err := b.call(ctx, "Runtime.evaluate", map[string]any{"expression": js, "returnByValue": true, "awaitPromise": true})
	if err != nil {
		return nil, err
	}
	var r struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
		Exception *struct {
			Text string `json:"text"`
		} `json:"exceptionDetails"`
	}
	_ = json.Unmarshal(raw, &r)
	if r.Exception != nil {
		return nil, errors.New("the page refused: " + r.Exception.Text)
	}
	return r.Result.Value, nil
}

// readPageJS numbers every visible interactive element (data-potluck-ref) and returns the page
// as text: title, address, the readable text, then the numbered elements.
const readPageJS = `(() => {
  const vis = e => { const r = e.getBoundingClientRect(), s = getComputedStyle(e); return r.width > 0 && r.height > 0 && s.visibility !== "hidden" && s.display !== "none"; };
  document.querySelectorAll("[data-potluck-ref]").forEach(e => e.removeAttribute("data-potluck-ref"));
  const sel = "a[href],button,input,select,textarea,[role=button],[role=link],[role=checkbox],[role=tab],[role=menuitem],[contenteditable=true],summary";
  const items = []; let n = 0;
  for (const e of document.querySelectorAll(sel)) {
    if (!vis(e) || e.disabled) continue;
    n++; e.setAttribute("data-potluck-ref", n);
    const tag = e.tagName.toLowerCase(), type = (e.getAttribute("type") || "").toLowerCase();
    const kind = tag === "a" ? "link" : tag === "input" ? (type === "checkbox" || type === "radio" ? type : type === "submit" || type === "button" ? "button" : "field" + (type && type !== "text" ? " (" + type + ")" : "")) : tag === "textarea" ? "field" : tag === "select" ? "choice" : e.getAttribute("role") || tag;
    let label = (e.getAttribute("aria-label") || e.innerText || e.value || e.getAttribute("placeholder") || e.getAttribute("title") || e.getAttribute("name") || "").replace(/\s+/g, " ").trim().slice(0, 100);
    if (tag === "input" && e.value && type !== "password") label += (label ? " " : "") + "= " + JSON.stringify(e.value.slice(0, 60));
    if (type === "checkbox" || type === "radio") label += e.checked ? " (checked)" : " (not checked)";
    if (tag === "a") { const h = e.getAttribute("href"); if (h && !h.startsWith("javascript")) label += " -> " + h.slice(0, 120); }
    items.push("[" + n + "] " + kind + ": " + label);
    if (n >= 400) break;
  }
  const text = (document.body ? document.body.innerText : "").replace(/\n{3,}/g, "\n\n").slice(0, 30000);
  return "Page: " + document.title + "\nAddress: " + location.href + "\n\n" + text + "\n\nThings you can use (act on them by number):\n" + items.join("\n");
})()`

// waitLoad waits for the page to finish loading (or a few seconds, for pages that never do).
func (b *browserCtl) waitLoad(ctx context.Context) {
	for i := 0; i < 40; i++ {
		v, err := b.eval(ctx, `document.readyState`)
		if err == nil && string(v) == `"complete"` {
			time.Sleep(300 * time.Millisecond) // scripts that draw after load
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// browserTool runs one of the agent's browser tools.
func browserTool(ctx context.Context, data, who, name string, args map[string]any) (string, error) {
	b := browsers
	if err := b.hold(who); err != nil {
		return "", err
	}
	b.mu.Lock()
	if b.dataDir == "" {
		b.dataDir = data
	}
	b.mu.Unlock()
	if err := b.ensure(ctx); err != nil {
		return "", err
	}
	str := func(k string) string { v, _ := args[k].(string); return v }
	ref := func() string {
		switch v := args["ref"].(type) {
		case float64:
			return fmt.Sprint(int(v))
		case string:
			// Only a number: it is put into the page's script.
			if r := strings.Trim(v, "[] "); refRe.MatchString(r) {
				return r
			}
		}
		return ""
	}
	read := func() (string, error) {
		v, err := b.eval(ctx, readPageJS)
		if err != nil {
			return "", err
		}
		var s string
		_ = json.Unmarshal(v, &s)
		return s, nil
	}
	switch name {
	case "browser_open":
		u := strings.TrimSpace(str("url"))
		if u == "" {
			return "", errors.New("which address?")
		}
		if !strings.Contains(u, "://") {
			u = "https://" + u
		}
		// Web pages only: a file:// address would hand the task any file on this computer,
		// past its file permissions and its project folder.
		if pu, err := url.Parse(u); err != nil || !(pu.Host != "" && (pu.Scheme == "http" || pu.Scheme == "https") || u == "about:blank") {
			return "", fmt.Errorf("%q is not a web address (http or https)", u)
		}
		if _, err := b.call(ctx, "Page.navigate", map[string]string{"url": u}); err != nil {
			return "", err
		}
		b.waitLoad(ctx)
		return read()
	case "browser_read":
		return read()
	case "browser_back":
		if _, err := b.eval(ctx, `history.back()`); err != nil {
			return "", err
		}
		b.waitLoad(ctx)
		return read()
	case "browser_click":
		r := ref()
		if r == "" {
			return "", errors.New("which number? (from browser_read)")
		}
		// A real mouse click at the element's centre: sites that ignore scripted clicks still react.
		v, err := b.eval(ctx, `(() => { const e = document.querySelector('[data-potluck-ref="`+r+`"]'); if (!e) return null; e.scrollIntoView({block: "center"}); const q = e.getBoundingClientRect(); return [q.left + q.width / 2, q.top + q.height / 2]; })()`)
		if err != nil {
			return "", err
		}
		var xy []float64
		if json.Unmarshal(v, &xy) != nil || len(xy) != 2 {
			return "", fmt.Errorf("there is no [%s] on the page now; read the page again", r)
		}
		for _, t := range []string{"mousePressed", "mouseReleased"} {
			if _, err := b.call(ctx, "Input.dispatchMouseEvent", map[string]any{"type": t, "x": xy[0], "y": xy[1], "button": "left", "clickCount": 1}); err != nil {
				return "", err
			}
		}
		b.waitLoad(ctx)
		return read()
	case "browser_type":
		r := ref()
		if r == "" {
			return "", errors.New("which field? (its number from browser_read)")
		}
		ok, err := b.eval(ctx, `(() => { const e = document.querySelector('[data-potluck-ref="`+r+`"]'); if (!e) return false; e.scrollIntoView({block: "center"}); e.focus(); if ("value" in e) { e.value = ""; e.dispatchEvent(new Event("input", {bubbles: true})); } return true; })()`)
		if err != nil {
			return "", err
		}
		if string(ok) != "true" {
			return "", fmt.Errorf("there is no [%s] on the page now; read the page again", r)
		}
		if _, err := b.call(ctx, "Input.insertText", map[string]string{"text": str("text")}); err != nil {
			return "", err
		}
		if s, _ := args["submit"].(bool); s {
			for _, t := range []string{"keyDown", "keyUp"} {
				_, _ = b.call(ctx, "Input.dispatchKeyEvent", map[string]any{"type": t, "key": "Enter", "code": "Enter", "windowsVirtualKeyCode": 13, "text": "\r"})
			}
			b.waitLoad(ctx)
		}
		return read()
	case "browser_screenshot":
		raw, err := b.call(ctx, "Page.captureScreenshot", map[string]string{"format": "png"})
		if err != nil {
			return "", err
		}
		var s struct {
			Data string `json:"data"`
		}
		_ = json.Unmarshal(raw, &s)
		img, _ := base64.StdEncoding.DecodeString(s.Data)
		path := filepath.Join(data, "browser-shots", time.Now().Format("20060102-150405")+".png")
		_ = os.MkdirAll(filepath.Dir(path), 0o700)
		if err := os.WriteFile(path, img, 0o600); err != nil {
			return "", err
		}
		return "Saved a screenshot of the page to " + path + ".", nil
	}
	return "", fmt.Errorf("unknown browser tool %s", name)
}

// ---------- a small WebSocket client (enough for the DevTools protocol) ----------

type wsConn struct {
	c  net.Conn
	r  *bufio.Reader
	mu sync.Mutex
}

func dialWS(u string) (*wsConn, error) {
	pu, err := url.Parse(u)
	if err != nil {
		return nil, err
	}
	c, err := net.DialTimeout("tcp", pu.Host, 10*time.Second)
	if err != nil {
		return nil, err
	}
	keyb := make([]byte, 16)
	_, _ = rand.Read(keyb)
	key := base64.StdEncoding.EncodeToString(keyb)
	fmt.Fprintf(c, "GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n", pu.RequestURI(), pu.Host, key)
	r := bufio.NewReader(c)
	res, err := http.ReadResponse(r, &http.Request{Method: "GET"})
	if err != nil {
		c.Close()
		return nil, err
	}
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	if res.StatusCode != 101 || res.Header.Get("Sec-WebSocket-Accept") != base64.StdEncoding.EncodeToString(sum[:]) {
		c.Close()
		return nil, fmt.Errorf("the browser refused the control connection (%s)", res.Status)
	}
	return &wsConn{c: c, r: r}, nil
}

func (w *wsConn) write(p []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	hdr := []byte{0x81} // final text frame
	switch n := len(p); {
	case n < 126:
		hdr = append(hdr, 0x80|byte(n))
	case n < 1<<16:
		hdr = append(hdr, 0x80|126, byte(n>>8), byte(n))
	default:
		hdr = append(hdr, 0x80|127)
		var l [8]byte
		binary.BigEndian.PutUint64(l[:], uint64(n))
		hdr = append(hdr, l[:]...)
	}
	mask := make([]byte, 4)
	_, _ = rand.Read(mask)
	hdr = append(hdr, mask...)
	body := make([]byte, len(p))
	for i := range p {
		body[i] = p[i] ^ mask[i%4]
	}
	_, err := w.c.Write(append(hdr, body...))
	return err
}

// read returns the next whole text message (joining continuation frames, answering pings).
func (w *wsConn) read() ([]byte, error) {
	var msg []byte
	for {
		var h [2]byte
		if _, err := io.ReadFull(w.r, h[:]); err != nil {
			return nil, err
		}
		fin, op := h[0]&0x80 != 0, h[0]&0x0f
		n := uint64(h[1] & 0x7f)
		switch n {
		case 126:
			var l [2]byte
			if _, err := io.ReadFull(w.r, l[:]); err != nil {
				return nil, err
			}
			n = uint64(binary.BigEndian.Uint16(l[:]))
		case 127:
			var l [8]byte
			if _, err := io.ReadFull(w.r, l[:]); err != nil {
				return nil, err
			}
			n = binary.BigEndian.Uint64(l[:])
		}
		if n > 256<<20 {
			return nil, errors.New("message too large")
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(w.r, payload); err != nil {
			return nil, err
		}
		switch op {
		case 0x8:
			return nil, io.EOF
		case 0x9: // ping: answer with a pong carrying the same data
			w.mu.Lock()
			_, _ = w.c.Write(append([]byte{0x8A, 0x80 | byte(len(payload)), 0, 0, 0, 0}, payload...))
			w.mu.Unlock()
			continue
		case 0xA:
			continue
		}
		msg = append(msg, payload...)
		if fin {
			return msg, nil
		}
	}
}

// close ends Potluck's browser window and everything it started.
func (b *browserCtl) close() {
	b.mu.Lock()
	cmd, ws := b.cmd, b.ws
	b.cmd, b.ws = nil, nil
	b.mu.Unlock()
	if ws != nil {
		_ = ws.c.Close()
	}
	if cmd != nil && cmd.Process != nil {
		killTree(cmd.Process.Pid)
		_ = cmd.Process.Kill()
	}
}
