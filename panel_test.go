package main

import (
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
