package main

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// Gated models. Some Hugging Face repositories ("gated") let anyone list their files but only
// hand them to a signed-in account that has accepted the repository's terms on its page. The
// person adds their own Hugging Face key once (Models page, on the main computer); Potluck
// sends it only to huggingface.co, never anywhere else (the file servers huggingface.co
// redirects to get a signed link instead, and Go drops the key on a redirect to another host).
// Helpers get it with each download they are asked to make and keep it in memory only.

var (
	hfKeyMu sync.Mutex
	hfKey   string // the main computer's key, for its own requests
)

func setHFKey(k string) {
	hfKeyMu.Lock()
	hfKey = strings.TrimSpace(k)
	hfKeyMu.Unlock()
	// Listings made without the key hide a gated repository's checksums: read them again.
	treeCacheMu.Lock()
	clear(treeCache)
	treeCacheMu.Unlock()
}

func currentHFKey() string {
	hfKeyMu.Lock()
	defer hfKeyMu.Unlock()
	return hfKey
}

type hfKeyCtx struct{}

// withHFKey carries a key handed over with a request (on a helper) to the downloads it starts.
func withHFKey(ctx context.Context, k string) context.Context {
	if k == "" {
		return ctx
	}
	return context.WithValue(ctx, hfKeyCtx{}, k)
}

// isHF: only Hugging Face's own site ever sees the key.
func isHF(u *url.URL) bool {
	h := strings.ToLower(u.Hostname())
	return u.Scheme == "https" && (h == "huggingface.co" || h == "hf.co")
}

// hfAuth adds the key to a request for huggingface.co, from the request's context or this
// computer's own setting.
func hfAuth(req *http.Request) {
	if !isHF(req.URL) {
		return
	}
	k, _ := req.Context().Value(hfKeyCtx{}).(string)
	if k == "" {
		k = currentHFKey()
	}
	if k != "" {
		req.Header.Set("Authorization", "Bearer "+k)
	}
}

// hfDenied explains a refusal from Hugging Face in the person's terms.
func hfDenied(req *http.Request, status int) string {
	if !isHF(req.URL) || (status != http.StatusUnauthorized && status != http.StatusForbidden) {
		return ""
	}
	repo := ""
	if p := strings.Split(strings.Trim(req.URL.Path, "/"), "/"); len(p) >= 2 {
		if p[0] == "api" && len(p) >= 4 {
			repo = p[2] + "/" + p[3]
		} else {
			repo = p[0] + "/" + p[1]
		}
	}
	has := req.Header.Get("Authorization") != ""
	if has {
		return "Hugging Face refused your key for " + repo + ": open https://huggingface.co/" + repo + " while signed in, accept its terms, and check the key may read gated repositories"
	}
	return repo + " is a gated model: open https://huggingface.co/" + repo + " while signed in, accept its terms, then add your Hugging Face key on Potluck's Models page"
}
