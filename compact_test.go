package main

import (
	"strings"
	"testing"
)

func TestCompactCutKeepsResultsWithCalls(t *testing.T) {
	m := func(role string) SMsg { return SMsg{Role: role} }
	// task, step 0 (3 calls), step 1 (3 calls): the last six start at a result of step 0.
	log := []SMsg{m("user"), m("assistant"), m("tool"), m("tool"), m("tool"), m("assistant"), m("tool"), m("tool"), m("tool")}
	if c := compactCut(log); c != 1 || log[c].Role != "assistant" {
		t.Fatalf("cut at %d (%s): step 0's results would be lost", c, log[c].Role)
	}
	// A latest step with six calls: nothing older than it but the task.
	log = []SMsg{m("user"), m("assistant"), m("tool"), m("tool"), m("tool"), m("tool"), m("tool"), m("tool")}
	if c := compactCut(log); c != 1 {
		t.Fatalf("cut at %d, want 1 (keep the whole latest step)", c)
	}
	for c := compactCut(log); c < len(log); c++ {
		_ = c
	}
}

func TestCompactLogKeepsTaskAndRecentWork(t *testing.T) {
	task := "Fix the build " + strings.Repeat("with details ", 800) // over 4000 characters
	old := []SMsg{{Role: "user", Content: task}}
	for i := 0; i < 50; i++ {
		old = append(old, SMsg{Role: "assistant", Content: "step " + strings.Repeat("x", 1000) + string(rune('A'+i%26))})
	}
	out := compactLog(old, 20000)
	if !strings.Contains(out, task) {
		t.Fatal("the task was cut")
	}
	if !strings.HasSuffix(strings.TrimSpace(out), string(rune('A'+49%26))) {
		t.Fatal("the most recent step was left out")
	}
	if !strings.Contains(out, "older steps left out") || len(out) > 20000+200 {
		t.Fatalf("not trimmed from the oldest end: %d chars", len(out))
	}
}

func TestSafeNameNeverEmpty(t *testing.T) {
	a, b := safeName("コードレビュー"), safeName("翻訳")
	for _, n := range []string{a, b, safeName("..."), safeName("!!"), safeName("")} {
		if n == "" || n == "." || n == ".." || strings.ContainsAny(n, "/\\") {
			t.Fatalf("unsafe name %q", n)
		}
	}
	if a == b {
		t.Fatal("two different names became the same folder")
	}
	if safeName("my-skill") != "my-skill" {
		t.Fatal("a plain name changed")
	}
}
