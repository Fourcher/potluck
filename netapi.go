package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Sharing with other devices and apps ("Let other devices and apps use Potluck"): the chat
// page and the OpenAI-compatible API on this computer's home-network and Tailscale addresses
// only (never every address), and only with the key. Apps such as Apollo, Open WebUI or a
// router send it as "Authorization: Bearer <key>"; a browser on another device opens the
// link with ?key=…, which leaves a cookie so the page keeps working.

const shareCookie = "potluck_key"

// sharedReq marks a request that came in through the share address.
type sharedReq struct{}

func isShared(r *http.Request) bool { v, _ := r.Context().Value(sharedReq{}).(bool); return v }

// shareKey returns the key, making one the first time.
func (h *Host) shareKey() string {
	if k := h.cfg.Get().ShareKey; k != "" {
		return k
	}
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	k := "pl-" + hex.EncodeToString(b)
	h.cfg.Update(func(c *Config) { c.ShareKey = k })
	return k
}

// RotateShareKey replaces the key: every device and app using the old one is locked out.
func (h *Host) RotateShareKey() string {
	h.cfg.Update(func(c *Config) { c.ShareKey = "" })
	return h.shareKey()
}

// shareURLs: where other devices reach Potluck, best first.
func (h *Host) shareURLs() []string {
	var out []string
	for _, ip := range phoneAddrs() {
		out = append(out, "http://"+net.JoinHostPort(ip, strconv.Itoa(portShare)))
	}
	return out
}

func (h *Host) shareLoop() {
	for {
		h.applyNetShare()
		time.Sleep(30 * time.Second)
	}
}

// applyNetShare listens on the addresses this computer has now while sharing is on.
func (h *Host) applyNetShare() {
	want := []string{}
	if h.cfg.Get().ShareChat {
		want = phoneAddrs()
		h.shareKey()
	}
	sort.Strings(want)
	key := strings.Join(want, ",")
	h.shareMu.Lock()
	defer h.shareMu.Unlock()
	if key == h.shareOn {
		return
	}
	for _, s := range h.shareSrvs {
		_ = s.Close()
	}
	h.shareSrvs, h.shareOn = nil, key
	for _, ip := range want {
		ln, err := net.Listen("tcp", net.JoinHostPort(ip, strconv.Itoa(portShare)))
		if err != nil {
			continue
		}
		srv := &http.Server{Handler: h.shareHandler(), ReadHeaderTimeout: 20 * time.Second}
		h.shareSrvs = append(h.shareSrvs, srv)
		go func() { _ = srv.Serve(ln) }()
	}
}

func (h *Host) shareAllowed(r *http.Request) bool {
	want := h.cfg.Get().ShareKey
	if want == "" {
		return false
	}
	got := ""
	if k, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		got = strings.TrimSpace(k)
	} else if k := r.Header.Get("X-Api-Key"); k != "" {
		got = k
	} else if c, err := r.Cookie(shareCookie); err == nil {
		got = c.Value
	}
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func (h *Host) shareHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A link with the key: remember it in a cookie and continue without it in the address.
		if k := r.URL.Query().Get("key"); k != "" {
			if subtle.ConstantTimeCompare([]byte(k), []byte(h.cfg.Get().ShareKey)) == 1 {
				http.SetCookie(w, &http.Cookie{Name: shareCookie, Value: k, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 400 * 24 * 3600})
				q := r.URL.Query()
				q.Del("key")
				r.URL.RawQuery = q.Encode()
				// A page opened from the link: reload it without the key in the address. A
				// request from an app (a POST) would lose its body to a redirect: let it
				// through with the key it brought.
				if r.Method == http.MethodGet || r.Method == http.MethodHead {
					http.Redirect(w, r, r.URL.String(), http.StatusSeeOther)
					return
				}
				r.Header.Set("Authorization", "Bearer "+k)
			}
		}
		if !h.shareAllowed(r) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("WWW-Authenticate", `Bearer realm="Potluck"`)
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"Potluck needs its key. Find it in Potluck under Running, Use Potluck from other devices and apps."}}`))
			return
		}
		h.mu.Lock()
		chat := h.chatAPI
		h.mu.Unlock()
		if chat == nil {
			http.Error(w, "Potluck is starting", http.StatusServiceUnavailable)
			return
		}
		// The chat handler still applies its own this-computer-only rules (running commands,
		// the control panel's actions) by the request's real address. Shared requests reach
		// Potluck's own models only, never the person's other setups (whose keys may cost money).
		chat.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sharedReq{}, true)))
	})
}
