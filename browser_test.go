package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// A real browser on this computer (only when asked: it opens a window).
func TestBrowserReal(t *testing.T) {
	if os.Getenv("POTLUCK_TEST_BROWSER") == "" || findBrowser() == "" {
		t.Skip("set POTLUCK_TEST_BROWSER=1 with Chrome, Chromium, Edge or Brave installed")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			fmt.Fprint(w, `<html><head><title>Shop</title></head><body><h1>Welcome</h1><a href="/about">About us</a>
				<form action="/search"><input name="q" placeholder="Search"><button>Go</button></form></body></html>`)
		case "/search":
			fmt.Fprintf(w, `<html><head><title>Results</title></head><body>You searched for %s</body></html>`, r.URL.Query().Get("q"))
		case "/about":
			fmt.Fprint(w, `<html><head><title>About</title></head><body>We sell roof paint.</body></html>`)
		}
	}))
	defer srv.Close()
	data := t.TempDir()
	defer browsers.close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	page, err := browserTool(ctx, data, "t1", "browser_open", map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(page, "Page: Shop") || !strings.Contains(page, "Welcome") {
		t.Fatalf("page text:\n%s", page)
	}
	num := func(page, what string) int {
		m := regexp.MustCompile(`\[(\d+)\] [^\n]*` + what).FindStringSubmatch(page)
		if m == nil {
			t.Fatalf("no %q in:\n%s", what, page)
		}
		var n int
		fmt.Sscan(m[1], &n)
		return n
	}
	field := num(page, "Search")
	res, err := browserTool(ctx, data, "t1", "browser_type", map[string]any{"ref": float64(field), "text": "silver paint", "submit": true})
	if err != nil || !strings.Contains(res, "You searched for silver paint") {
		t.Fatalf("type and submit: %v\n%s", err, res)
	}
	res, _ = browserTool(ctx, data, "t1", "browser_back", nil)
	link := num(res, "About us")
	res, err = browserTool(ctx, data, "t1", "browser_click", map[string]any{"ref": float64(link)})
	if err != nil || !strings.Contains(res, "We sell roof paint") {
		t.Fatalf("click: %v\n%s", err, res)
	}
	if _, err := browserTool(ctx, data, "t1", "browser_click", map[string]any{"ref": float64(999)}); err == nil {
		t.Fatal("clicking a number that is not there should say so")
	}
}
