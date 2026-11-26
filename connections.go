package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Your other AI setups: any OpenAI-compatible server the person already uses (LM Studio,
// Ollama, a router such as LiteLLM or OpenRouter, another Potluck) shows up in Potluck's chat
// next to its own models. A model from a connection is named "ext:<connection>:<model>"; chat
// requests for it are passed straight through to that server.

type AIConnection struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"baseURL"` // ends in /v1
	Key     string `json:"key,omitempty"`
}

// connPresets are the setups Potluck looks for on this computer by itself.
var connPresets = []AIConnection{
	{Name: "LM Studio", BaseURL: "http://127.0.0.1:1234/v1"},
	{Name: "Ollama", BaseURL: "http://127.0.0.1:11434/v1"},
	{Name: "Jan", BaseURL: "http://127.0.0.1:1337/v1"},
	{Name: "llama.cpp server", BaseURL: "http://127.0.0.1:8080/v1"},
}

var (
	connMu    sync.Mutex
	connCache = map[string]connModels{}
)

type connModels struct {
	at     time.Time
	models []string
	err    string
}

func normBase(u string) (string, error) {
	u = strings.TrimRight(strings.TrimSpace(u), "/")
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "http" && p.Scheme != "https") || p.Host == "" {
		return "", errors.New("that address doesn't look like a server address (it should start with http:// or https://)")
	}
	if !strings.HasSuffix(p.Path, "/v1") {
		u += "/v1"
	}
	return u, nil
}

// connList asks a server for its models (cached for a minute).
func connList(c AIConnection, fresh bool) ([]string, error) {
	connMu.Lock()
	if m, ok := connCache[c.ID]; ok && !fresh && time.Since(m.at) < time.Minute {
		connMu.Unlock()
		if m.err != "" {
			return nil, errors.New(m.err)
		}
		return m.models, nil
	}
	connMu.Unlock()
	req, _ := http.NewRequest("GET", c.BaseURL+"/models", nil)
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	var out []string
	res, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err == nil {
		var body struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if res.StatusCode == 401 || res.StatusCode == 403 {
			err = errors.New("it refused the key (check the key, or add one if the server needs it)")
		} else if res.StatusCode != 200 {
			err = errors.New("the server answered " + res.Status)
		} else if e := json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(&body); e != nil {
			err = errors.New("the server's answer wasn't a model list")
		} else {
			for _, d := range body.Data {
				out = append(out, d.ID)
			}
		}
		res.Body.Close()
	}
	m := connModels{at: time.Now(), models: out}
	if err != nil {
		m.err = err.Error()
	}
	connMu.Lock()
	connCache[c.ID] = m
	connMu.Unlock()
	return out, err
}

// AddConnection checks the server answers, then remembers it.
func (h *Host) AddConnection(name, base, key string) (AIConnection, error) {
	b, err := normBase(base)
	if err != nil {
		return AIConnection{}, err
	}
	id := make([]byte, 4)
	_, _ = rand.Read(id)
	c := AIConnection{ID: hex.EncodeToString(id), Name: strings.TrimSpace(name), BaseURL: b, Key: strings.TrimSpace(key)}
	if c.Name == "" {
		c.Name = strings.TrimPrefix(strings.TrimPrefix(b, "https://"), "http://")
	}
	if _, err := connList(c, true); err != nil {
		return AIConnection{}, errors.New("Potluck couldn't reach it: " + err.Error())
	}
	h.cfg.Update(func(cf *Config) { cf.Connections = append(cf.Connections, c) })
	return c, nil
}

func (h *Host) RemoveConnection(id string) {
	h.cfg.Update(func(cf *Config) {
		var keep []AIConnection
		for _, c := range cf.Connections {
			if c.ID != id {
				keep = append(keep, c)
			}
		}
		cf.Connections = keep
	})
}

// FoundConnections: known setups answering on this computer that aren't connected yet.
func (h *Host) FoundConnections() []AIConnection {
	have := map[string]bool{}
	for _, c := range h.cfg.Get().Connections {
		have[c.BaseURL] = true
	}
	var out []AIConnection
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, p := range connPresets {
		if have[p.BaseURL] || strings.Contains(p.BaseURL, ":"+itoa(portChat)) {
			continue
		}
		wg.Add(1)
		go func(p AIConnection) {
			defer wg.Done()
			p.ID = "probe-" + p.Name
			if ms, err := connList(p, true); err == nil && len(ms) > 0 {
				mu.Lock()
				out = append(out, p)
				mu.Unlock()
			}
		}(p)
	}
	wg.Wait()
	return out
}

// mergedModels answers /v1/models with Potluck's model and the connections' models.
// mergedModels answers /v1/models: Potluck's own model, with the conversation size it was
// started with (context_length, so agents and other apps know when to trim), and for this
// computer also the person's other AI setups. Crew members are listed for everyone: they
// are Potluck's own models.
func (h *Host) mergedModels(w http.ResponseWriter, r *http.Request, api http.Handler, shared bool) {
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, r)
	var list struct {
		Object string           `json:"object"`
		Data   []map[string]any `json:"data"`
	}
	if rec.Code == 200 {
		_ = json.Unmarshal(rec.Body.Bytes(), &list)
	}
	list.Object = "list"
	h.mu.Lock()
	ctx := h.lastCtx
	h.mu.Unlock()
	for _, m := range list.Data {
		if ctx > 0 {
			m["context_length"] = ctx
		}
	}
	more := h.crewChatModels()
	if !shared {
		more = append(more, h.connChatModels()...)
	}
	for _, m := range more {
		list.Data = append(list.Data, map[string]any{"id": m.File, "object": "model", "owned_by": m.NodeName})
	}
	writeJSON(w, list)
}

// connChatModels lists the connections' models for the chat's model menu.
func (h *Host) connChatModels() []chatModel {
	var out []chatModel
	for _, c := range h.cfg.Get().Connections {
		ms, err := connList(c, false)
		if err != nil {
			continue
		}
		for _, m := range ms {
			out = append(out, chatModel{Title: m, File: "ext:" + c.ID + ":" + m, Node: "ext:" + c.ID, NodeName: c.Name})
		}
	}
	return out
}

// connRoute passes a chat request for a connection's model through to that server. It
// returns false when the request is for Potluck's own model.
func (h *Host) connRoute(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost || !strings.HasPrefix(r.URL.Path, "/v1/") {
		return false
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<20))
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if err != nil {
		return false
	}
	var peek struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(raw, &peek)
	rest, ok := strings.CutPrefix(peek.Model, "ext:")
	if !ok {
		return false
	}
	if isShared(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":{"message":"Models from your other AI setups can only be used on the computer running Potluck."}}`))
		return true
	}
	id, model, _ := strings.Cut(rest, ":")
	var conn *AIConnection
	for _, c := range h.cfg.Get().Connections {
		if c.ID == id {
			conn = &c
			break
		}
	}
	if conn == nil {
		fail(w, errors.New("that connection was removed"))
		return true
	}
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		fail(w, errors.New("could not read the request"))
		return true
	}
	body["model"] = model
	// Potluck's own extras (thinking budget for its engine) mean nothing to other servers.
	delete(body, "reasoning_budget_tokens")
	delete(body, "reasoning_budget_message")
	out, _ := json.Marshal(body)
	target, _ := url.Parse(conn.BaseURL)
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme, pr.Out.URL.Host = target.Scheme, target.Host
			pr.Out.URL.Path = strings.TrimSuffix(target.Path, "/v1") + r.URL.Path
			pr.Out.Host = target.Host
			pr.Out.Header.Del("Cookie")
			pr.Out.Header.Del("Authorization")
			if conn.Key != "" {
				pr.Out.Header.Set("Authorization", "Bearer "+conn.Key)
			}
			pr.Out.Body = io.NopCloser(bytes.NewReader(out))
			pr.Out.ContentLength = int64(len(out))
		},
		FlushInterval: 50 * time.Millisecond,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": "Potluck could not reach " + conn.Name + ": " + err.Error()}})
		},
	}
	rp.ServeHTTP(w, r)
	return true
}
