package main

// One switch for thinking. Models are told whether to think in different ways: Qwen-style
// chat templates read enable_thinking, Inkling 2 reads a reasoning effort (none, minimal, low,
// medium, high, xhigh, max), and some think a little whatever they are told. Every Potluck
// client (chat page, iPhone, agent tasks, the helper, other apps) sends one field instead,
//
//	"thinking": "off" | "low" | "medium" | "high"   (or true / false)
//
// and Potluck turns it into what the model reads.
//
// Off means off: the model's lowest effort and a thinking budget of 0, so it answers at once.
// Inkling 2's "none" alone does not stop it (measured on the 2-bit file: up to 1,356 tokens of
// thinking for a haiku); with a budget of 0 it answered all six test prompts with no thinking
// and no loop, at the makers' sampling settings. (This morning's loop at budget 0 came with
// the engine's generic sampling settings; Potluck now fills in the makers', sampling.go.)
// On, the budget is only a backstop against a model that will not stop.

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

const (
	thinkBackstopOff = 0
	thinkBackstopOn  = 8192
	thinkStopMessage = "Stop thinking now and give the answer."
)

// thinkLevel reads the request's thinking field: "" when absent.
func thinkLevel(v any) string {
	switch t := v.(type) {
	case bool:
		if t {
			return "high"
		}
		return "off"
	case string:
		switch strings.ToLower(t) {
		case "off", "none", "no", "false":
			return "off"
		case "minimal", "low", "medium", "high", "xhigh", "max":
			return strings.ToLower(t)
		case "on", "yes", "true":
			return "high"
		}
	}
	return ""
}

// applyThinking turns the thinking field into the model's own controls. Settings the client
// gave itself win. It reports whether the body changed.
func applyThinking(body map[string]any) bool {
	v, ok := body["thinking"]
	if !ok {
		return false
	}
	delete(body, "thinking")
	lvl := thinkLevel(v)
	if lvl == "" {
		return true
	}
	if m, _ := body["model"].(string); strings.HasPrefix(m, "ext:") {
		return true // another AI service: its own controls, unknown to Potluck
	}
	kw, _ := body["chat_template_kwargs"].(map[string]any)
	if kw == nil {
		kw = map[string]any{}
	}
	effort := lvl
	if lvl == "off" {
		effort = "none"
	}
	if _, set := kw["enable_thinking"]; !set {
		kw["enable_thinking"] = lvl != "off"
	}
	if _, set := kw["reasoning_effort"]; !set {
		kw["reasoning_effort"] = effort
	}
	body["chat_template_kwargs"] = kw
	if _, set := body["reasoning_budget_tokens"]; !set {
		if lvl == "off" {
			body["reasoning_budget_tokens"] = thinkBackstopOff
		} else {
			body["reasoning_budget_tokens"] = thinkBackstopOn
		}
		if _, set := body["reasoning_budget_message"]; !set {
			body["reasoning_budget_message"] = thinkStopMessage
		}
	}
	return true
}

// thinkingRequest rewrites a chat request that carries the thinking field.
func thinkingRequest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || (r.URL.Path != "/v1/chat/completions" && r.URL.Path != "/chat/completions") {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<20))
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if err != nil || !bytes.Contains(raw, []byte(`"thinking"`)) {
		return
	}
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil || !applyThinking(body) {
		return
	}
	if b, err := json.Marshal(body); err == nil {
		r.Body = io.NopCloser(bytes.NewReader(b))
		r.ContentLength = int64(len(b))
		r.Header.Del("Content-Length")
	}
}
