package main

import "testing"

func TestApplyThinking(t *testing.T) {
	b := map[string]any{"model": "potluck", "thinking": "off"}
	applyThinking(b)
	kw := b["chat_template_kwargs"].(map[string]any)
	if _, left := b["thinking"]; left || kw["reasoning_effort"] != "none" || kw["enable_thinking"] != false || b["reasoning_budget_tokens"] != 0 {
		t.Fatalf("off: %v", b)
	}
	b = map[string]any{"thinking": true, "reasoning_budget_tokens": 2500, "chat_template_kwargs": map[string]any{"reasoning_effort": "medium"}}
	applyThinking(b)
	kw = b["chat_template_kwargs"].(map[string]any)
	if kw["reasoning_effort"] != "medium" || kw["enable_thinking"] != true || b["reasoning_budget_tokens"] != 2500 {
		t.Fatalf("the client's own settings must win: %v", b)
	}
	b = map[string]any{"model": "ext:or:gpt", "thinking": "high"}
	applyThinking(b)
	if _, ok := b["chat_template_kwargs"]; ok || b["thinking"] != nil {
		t.Fatalf("another AI service gets neither the field nor Potluck's controls: %v", b)
	}
	if applyThinking(map[string]any{"model": "potluck"}) {
		t.Fatal("no thinking field: nothing to change")
	}
}
