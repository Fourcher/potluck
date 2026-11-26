package main

// Phones: the Potluck iPhone app talks to the main computer over the home network or
// Tailscale. The phone is paired once by scanning a code in the control panel; after that
// it holds a key of its own that the control panel can take away at any time.
//
// What a phone can do: chat with the running model (which loads it again if it was let go),
// see what is running, and switch to a model that is already on one of the computers.
// What it cannot do: run commands on this computer (agent mode), change settings, or reach
// the control panel. Agent mode asks before each command on the chat page of this computer
// only; a key that can start commands would be a remote shell if the phone were lost.
//
// The connection is encrypted with this computer's own certificate. The pairing code carries
// the certificate's fingerprint and the phone accepts no other, so nobody on the network can
// stand in for this computer. The phone server only listens while a phone is paired or a
// pairing code is showing, and only on home-network and Tailscale addresses.

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"rsc.io/qr"
)

// PhoneKey is a paired phone. Only a hash of its key is kept.
type PhoneKey struct {
	Name      string    `json:"name"`
	Hash      string    `json:"hash"`
	Added     time.Time `json:"added"`
	PushToken string    `json:"pushToken,omitempty"` // Apple push token, when the app allowed notifications
	PushEnv   string    `json:"pushEnv,omitempty"`
}

// phoneAPI: the only paths a phone may use besides /phone/*. Everything else on the chat
// address (the chat page, /potluck/run, /potluck/switch) stays out of reach.
var phoneAPI = map[string]bool{"/v1/chat/completions": true, "/v1/models": true, "/potluck/state": true, "/potluck/models": true}

var pushTokenRe = regexp.MustCompile(`^[0-9a-fA-F]{64,200}$`)

func hashKey(k string) string {
	s := sha256.Sum256([]byte(k))
	return hex.EncodeToString(s[:])
}

// phoneAddrs: the addresses a phone can reach: home network ranges and Tailscale (100.64/10).
func phoneAddrs() []string {
	var out []string
	for _, ip := range localIPs() {
		p := net.ParseIP(ip)
		if p == nil {
			continue
		}
		if p.IsPrivate() || isTailscale(p) {
			out = append(out, ip)
		}
	}
	return out
}

func isTailscale(ip net.IP) bool {
	_, cgnat, _ := net.ParseCIDR("100.64.0.0/10")
	return cgnat.Contains(ip)
}

// phoneLoop keeps the phone server listening while it is needed, on the addresses this
// computer has right now (they change when the network does).
func (h *Host) phoneLoop() {
	for {
		h.applyPhones()
		time.Sleep(30 * time.Second)
	}
}

func (h *Host) applyPhones() {
	h.mu.Lock()
	pairing := h.pairSecret != "" && time.Now().Before(h.pairUntil)
	h.mu.Unlock()
	want := []string{}
	if len(h.cfg.Get().Phones) > 0 || pairing {
		want = phoneAddrs()
	}
	h.phoneMu.Lock()
	defer h.phoneMu.Unlock()
	conf := &tls.Config{Certificates: []tls.Certificate{h.id.Cert}, MinVersion: tls.VersionTLS12}
	h.phoneSrvs = syncListeners(h.phoneSrvs, want, "phones", func(ip string) (*http.Server, error) {
		ln, err := net.Listen("tcp", net.JoinHostPort(ip, strconv.Itoa(portPhone)))
		if err != nil {
			return nil, err
		}
		srv := &http.Server{Handler: h.phoneHandler(), ReadHeaderTimeout: 20 * time.Second}
		go func() { _ = srv.Serve(tls.NewListener(ln, conf)) }()
		return srv, nil
	})
}

// phoneWho returns the paired phone a request comes from, by its key.
func (h *Host) phoneWho(r *http.Request) (string, bool) {
	k, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || k == "" {
		return "", false
	}
	want := hashKey(k)
	for id, p := range h.cfg.Get().Phones {
		if subtle.ConstantTimeCompare([]byte(p.Hash), []byte(want)) == 1 {
			return id, true
		}
	}
	return "", false
}

func (h *Host) phoneHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/phone/pair" && r.Method == http.MethodPost {
			h.phonePair(w, r)
			return
		}
		id, ok := h.phoneWho(r)
		if !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "This phone is not paired with this computer any more. Pair it again from Potluck's control panel."}})
			return
		}
		h.mu.Lock()
		if h.phoneSeen == nil {
			h.phoneSeen = map[string]time.Time{}
		}
		h.phoneSeen[id] = time.Now()
		chat := h.chatAPI
		h.mu.Unlock()
		switch {
		case r.URL.Path == "/phone/hello" && r.Method == http.MethodGet:
			writeJSON(w, map[string]any{"name": h.selfName(), "version": version, "phone": h.cfg.Get().Phones[id].Name})
		case r.URL.Path == "/phone/switch" && r.Method == http.MethodPost:
			var in struct {
				File string `json:"file"`
				Node string `json:"node"`
				Ctx  int    `json:"ctx"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
				fail(w, err)
				return
			}
			if err := h.chatSwitch(in.File, in.Node, in.Ctx); err != nil {
				fail(w, err)
				return
			}
			writeJSON(w, map[string]bool{"ok": true})
		case r.URL.Path == "/phone/questions" && r.Method == http.MethodGet:
			// What the person's tasks are waiting on, to answer from the phone (sessions.go).
			type item struct {
				*Question
				Task string `json:"task"`
			}
			var out []item
			st := h.sessionStore()
			for _, q := range st.questions() {
				title := ""
				if s := st.get(q.Session); s != nil {
					s.mu.Lock()
					title = s.Title
					s.mu.Unlock()
				}
				out = append(out, item{q, title})
			}
			writeJSON(w, map[string]any{"questions": out})
		case r.URL.Path == "/phone/answer" && r.Method == http.MethodPost:
			var in struct {
				Session  string `json:"session"`
				Question string `json:"question"`
				Answer   string `json:"answer"`
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
				fail(w, err)
				return
			}
			if in.Session == testSession {
				h.mu.Lock()
				h.pushTestAnswer = trimTo(in.Answer, 200)
				h.mu.Unlock()
				writeJSON(w, map[string]bool{"ok": true})
				return
			}
			st := h.sessionStore()
			s := st.get(in.Session)
			if s == nil {
				fail(w, errors.New("that task is gone"))
				return
			}
			if err := st.answer(s, in.Question, in.Answer); err != nil {
				fail(w, err)
				return
			}
			writeJSON(w, map[string]bool{"ok": true})
		case r.URL.Path == "/phone/push" && r.Method == http.MethodPost:
			// The phone's push token, so Potluck can alert it when a task needs an answer.
			var in struct {
				Token string `json:"token"`
				Env   string `json:"env"` // "development" (built from Xcode) or "production"
			}
			if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&in); err != nil {
				fail(w, err)
				return
			}
			// Apple's device tokens are hex; anything else would end up in the push address.
			if !pushTokenRe.MatchString(in.Token) || (in.Env != "development" && in.Env != "production") {
				fail(w, errors.New("that is not a push token from Apple"))
				return
			}
			h.cfg.Update(func(c *Config) {
				c.editPhone(id, func(p *PhoneKey) { p.PushToken, p.PushEnv = in.Token, in.Env })
			})
			writeJSON(w, map[string]bool{"ok": true})
		case r.URL.Path == "/phone/tasks" && r.Method == http.MethodGet:
			writeJSON(w, map[string]any{"tasks": h.sessionStore().phoneTasks()})
		case strings.HasPrefix(r.URL.Path, "/phone/tasks/"):
			// One task: read it, add a message to it (it carries on under the permissions it
			// was given on the computer), or stop it. A phone starts no new work this way.
			st := h.sessionStore()
			tid, action, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/phone/tasks/"), "/")
			s := st.get(tid)
			if s == nil || s.Kind == "helper" {
				http.NotFound(w, r)
				return
			}
			switch {
			case action == "" && r.Method == http.MethodGet:
				writeJSON(w, st.phoneTask(s))
			case action == "message" && r.Method == http.MethodPost:
				var in struct {
					Text string `json:"text"`
				}
				if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil || strings.TrimSpace(in.Text) == "" {
					fail(w, errors.New("write a message"))
					return
				}
				if err := st.send(s, in.Text); err != nil {
					fail(w, err)
					return
				}
				writeJSON(w, map[string]bool{"ok": true})
			case action == "stop" && r.Method == http.MethodPost:
				st.stop(s)
				writeJSON(w, map[string]bool{"ok": true})
			default:
				http.NotFound(w, r)
			}
		case phoneAPI[r.URL.Path] && chat != nil:
			// Under the same rules as any other device: Potluck's own models only, never the
			// person's (often paid) external AI connections, which stay with this computer.
			chat.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sharedReq{}, true)))
		default:
			http.NotFound(w, r)
		}
	})
}

// phonePair swaps the one-time secret from the pairing code for a key of the phone's own.
func (h *Host) phonePair(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Secret string `json:"secret"`
		Name   string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&in); err != nil {
		fail(w, err)
		return
	}
	h.mu.Lock()
	good := h.pairSecret != "" && time.Now().Before(h.pairUntil) && subtle.ConstantTimeCompare([]byte(in.Secret), []byte(h.pairSecret)) == 1
	if good {
		h.pairSecret = "" // one phone per code
	}
	h.mu.Unlock()
	if !good {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "That pairing code has expired or was already used. Show a new one in Potluck's control panel."}})
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || len(name) > 60 {
		name = "iPhone"
	}
	key, id := randomHex(32), randomHex(6)
	h.cfg.Update(func(c *Config) {
		if c.Phones == nil {
			c.Phones = map[string]PhoneKey{}
		}
		c.Phones[id] = PhoneKey{Name: name, Hash: hashKey(key), Added: time.Now()}
	})
	writeJSON(w, map[string]string{"key": key, "id": id, "name": h.selfName(), "version": version})
}

// PairingCode makes a new one-time pairing code, good for 10 minutes, and starts listening.
func (h *Host) PairingCode() (map[string]any, error) {
	addrs := phoneAddrs()
	if len(addrs) == 0 {
		return nil, errors.New("this computer is not on a home network or Tailscale right now")
	}
	// Tailscale first: it works away from home too.
	sort.SliceStable(addrs, func(i, j int) bool {
		return isTailscale(net.ParseIP(addrs[i])) && !isTailscale(net.ParseIP(addrs[j]))
	})
	secret := randomHex(16)
	h.mu.Lock()
	h.pairSecret, h.pairUntil = secret, time.Now().Add(10*time.Minute)
	h.mu.Unlock()
	h.applyPhones()
	q := url.Values{}
	q.Set("n", h.selfName())
	q.Set("a", strings.Join(addrs, ","))
	q.Set("p", strconv.Itoa(portPhone))
	q.Set("f", h.id.FP)
	q.Set("s", secret)
	link := "potluck://pair?" + q.Encode()
	code, err := qr.Encode(link, qr.M)
	if err != nil {
		return nil, err
	}
	code.Scale = 6
	img := "data:image/png;base64," + base64.StdEncoding.EncodeToString(code.PNG())
	out := map[string]any{"link": link, "qr": img, "addrs": addrs, "minutes": 10, "appURL": iPhoneAppURL}
	if app, err := qr.Encode(iPhoneAppURL, qr.M); err == nil {
		app.Scale = 6
		out["appQR"] = "data:image/png;base64," + base64.StdEncoding.EncodeToString(app.PNG())
	}
	return out, nil
}

// iPhoneAppURL is the Potluck app's App Store page.
const iPhoneAppURL = "https://apps.apple.com/app/id6838920258"

// Phones lists the paired phones for the control panel.
func (h *Host) Phones() []map[string]any {
	h.mu.Lock()
	seen := map[string]time.Time{}
	for k, v := range h.phoneSeen {
		seen[k] = v
	}
	pairing := h.pairSecret != "" && time.Now().Before(h.pairUntil)
	h.mu.Unlock()
	out := []map[string]any{}
	for id, p := range h.cfg.Get().Phones {
		m := map[string]any{"id": id, "name": p.Name, "added": p.Added}
		if t, ok := seen[id]; ok {
			m["seen"] = t
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i]["added"].(time.Time).Before(out[j]["added"].(time.Time)) })
	if pairing {
		out = append(out, map[string]any{"pairing": true})
	}
	return out
}

// ForgetPhone takes a phone's key away.
func (h *Host) ForgetPhone(id string) {
	h.cfg.Update(func(c *Config) { delete(c.Phones, id) })
	h.applyPhones()
}

// StopPairing withdraws a pairing code that is showing.
func (h *Host) StopPairing() {
	h.mu.Lock()
	h.pairSecret = ""
	h.mu.Unlock()
	h.applyPhones()
}

// editPhone changes a paired phone only if it is still paired. A request or an alert that
// started before the person chose Forget must not write the phone back as an empty entry
// (shown in the panel, holding the phone listener open, keeping a lost phone's push token).
func (c *Config) editPhone(id string, f func(*PhoneKey)) {
	p, ok := c.Phones[id]
	if !ok {
		return
	}
	f(&p)
	c.Phones[id] = p
}

// PhoneTask is a task as the phone shows it: what it is doing, its report, its last words.
type PhoneTask struct {
	ID       string     `json:"id"`
	Title    string     `json:"title"`
	Status   string     `json:"status"`
	Error    string     `json:"error,omitempty"`
	Updated  time.Time  `json:"updated"`
	Report   string     `json:"report,omitempty"`
	Question *Question  `json:"question,omitempty"`
	Messages []PhoneMsg `json:"messages,omitempty"`
}

type PhoneMsg struct {
	Role string `json:"role"` // user | assistant | step (a tool the task used)
	Text string `json:"text"`
}

// phoneTasks lists the tasks, newest first (not the Potluck helper's own conversations).
func (st *sessionStore) phoneTasks() []PhoneTask {
	st.mu.Lock()
	list := make([]*Session, 0, len(st.sessions))
	for _, s := range st.sessions {
		list = append(list, s)
	}
	st.mu.Unlock()
	var out []PhoneTask
	for _, s := range list {
		s.mu.Lock()
		if s.Kind != "helper" {
			out = append(out, PhoneTask{ID: s.ID, Title: s.Title, Status: s.Status, Error: s.Error, Updated: s.Updated, Report: firstLine(s.Report), Question: s.Question})
		}
		s.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	if len(out) > 50 {
		out = out[:50]
	}
	return out
}

// phoneTask is one task with its latest messages (tool results shortened to the tool's name).
func (st *sessionStore) phoneTask(s *Session) PhoneTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := PhoneTask{ID: s.ID, Title: s.Title, Status: s.Status, Error: s.Error, Updated: s.Updated, Report: s.Report, Question: s.Question}
	msgs := s.Messages
	if len(msgs) > 60 {
		msgs = msgs[len(msgs)-60:]
	}
	for _, m := range msgs {
		switch m.Role {
		case "user":
			t.Messages = append(t.Messages, PhoneMsg{"user", trimTo(m.Content, 4000)})
		case "assistant":
			if strings.TrimSpace(m.Content) != "" {
				t.Messages = append(t.Messages, PhoneMsg{"assistant", trimTo(m.Content, 8000)})
			}
			for _, c := range m.Calls {
				t.Messages = append(t.Messages, PhoneMsg{"step", c.Name + " " + trimTo(c.Args, 160)})
			}
		}
	}
	return t
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}
