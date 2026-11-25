package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Another website can make the browser send a plain GET (a link, an image) to the panel; it
// cannot add the X-Potluck header. Found by the self-review: GET /api/quit needed no header.
func TestPanelNeedsHeaderEvenForGET(t *testing.T) {
	for _, c := range []struct {
		host, method, hdr string
		ok                bool
	}{
		{"127.0.0.1:7716", "GET", "", false},
		{"127.0.0.1:7716", "POST", "", false},
		{"127.0.0.1:7716", "GET", "1", true},
		{"localhost:7716", "POST", "1", true},
		{"evil.example:7716", "GET", "1", false},
	} {
		r := httptest.NewRequest(c.method, "http://"+c.host+"/api/quit", nil)
		if c.hdr != "" {
			r.Header.Set("X-Potluck", c.hdr)
		}
		if got := panelAllowed(r); got != c.ok {
			t.Errorf("%+v: %v", c, got)
		}
	}
}

// A map handed out by Get must never change underneath its reader (found by the self-review
// check: Phones was written in place while the phone key check looped over it, a fatal error).
func TestConfigGetIsNotChangedByUpdate(t *testing.T) {
	s := loadConfig(t.TempDir())
	s.Update(func(c *Config) { c.Phones = map[string]PhoneKey{"a": {Name: "one"}} })
	got := s.Get().Phones
	done := make(chan bool)
	go func() {
		for i := 0; i < 200; i++ {
			s.Update(func(c *Config) { c.Phones[string(rune('b'+i%20))] = PhoneKey{Name: "x"} })
		}
		close(done)
	}()
	for i := 0; i < 200; i++ {
		for range got {
		}
	}
	<-done
	if len(got) != 1 || len(s.Get().Phones) != 21 {
		t.Fatalf("handed-out %d, now %d", len(got), len(s.Get().Phones))
	}
}

// An address that cannot be opened now is tried again later, without closing the servers
// that work (found by the self-review: one failure stopped all retries until the addresses
// changed).
func TestSyncListenersRetriesOnlyTheMissing(t *testing.T) {
	opened := map[string]int{}
	fail := map[string]bool{"b": true}
	open := func(ip string) (*http.Server, error) {
		opened[ip]++
		if fail[ip] {
			return nil, errors.New("busy")
		}
		return &http.Server{}, nil
	}
	cur := syncListeners(nil, []string{"a", "b"}, "test", open)
	a := cur["a"]
	if a == nil || cur["b"] != nil {
		t.Fatalf("%v", cur)
	}
	fail["b"] = false
	cur = syncListeners(cur, []string{"a", "b"}, "test", open)
	if cur["a"] != a || cur["b"] == nil || opened["a"] != 1 || opened["b"] != 2 {
		t.Fatalf("%v %v", cur, opened)
	}
	cur = syncListeners(cur, []string{"b"}, "test", open)
	if len(cur) != 1 {
		t.Fatalf("%v", cur)
	}
}
