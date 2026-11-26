package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestHFKeyOnlyGoesToHuggingFace(t *testing.T) {
	setHFKey("hf_main")
	defer setHFKey("")
	for u, want := range map[string]string{
		"https://huggingface.co/a/b/resolve/main/x.gguf": "Bearer hf_main",
		"https://hf.co/a/b/resolve/main/x.gguf":          "Bearer hf_main",
		"http://huggingface.co/a/b/resolve/main/x.gguf":  "",
		"https://huggingface.co.evil.example/x":          "",
		"https://cas-bridge.xethub.hf.co/x":              "",
		"https://github.com/ggml-org/llama.cpp/releases": "",
		"https://pirateface.co/s/x":                      "",
	} {
		req, _ := http.NewRequest("GET", u, nil)
		hfAuth(req)
		if got := req.Header.Get("Authorization"); got != want {
			t.Errorf("%s: %q", u, got)
		}
	}
	// A key handed over with a helper's request wins over the computer's own.
	req, _ := http.NewRequestWithContext(withHFKey(context.Background(), "hf_sent"), "GET", "https://huggingface.co/a/b/resolve/main/x.gguf", nil)
	hfAuth(req)
	if req.Header.Get("Authorization") != "Bearer hf_sent" {
		t.Fatal(req.Header)
	}
}

func TestHFDeniedNamesTheRepo(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://huggingface.co/orcarouter/Inkling-2-Mini-abliterated-GGUF/resolve/main/x.gguf", nil)
	if m := hfDenied(req, 401); !strings.Contains(m, "orcarouter/Inkling-2-Mini-abliterated-GGUF is a gated model") {
		t.Fatal(m)
	}
	req.Header.Set("Authorization", "Bearer x")
	if m := hfDenied(req, 403); !strings.Contains(m, "refused your key") || strings.Contains(m, "Bearer") {
		t.Fatal(m)
	}
	if hfDenied(req, 404) != "" {
		t.Fatal("404 is not a refusal")
	}
}
