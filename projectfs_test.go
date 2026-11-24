package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectFS(t *testing.T) {
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "src", "deep"), 0o755)
	_ = os.MkdirAll(filepath.Join(root, "node_modules", "x"), 0o755)
	_ = os.WriteFile(filepath.Join(root, "src", "main.go"), []byte("package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "src", "deep", "util.go"), []byte("package deep\n// helper\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "node_modules", "x", "i.js"), []byte("hi"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "CLAUDE.md"), []byte("Run go test before committing."), 0o644)
	do := func(in FSReq) string {
		in.Root = root
		out, err := projectFS(in)
		if err != nil {
			return "ERR " + err.Error()
		}
		return out
	}
	if l := do(FSReq{Op: "list"}); strings.Contains(l, "node_modules") || !strings.Contains(l, "src/deep/util.go") {
		t.Fatalf("list: %q", l)
	}
	if l := do(FSReq{Op: "list", Glob: "src/**/*.go"}); !strings.Contains(l, "src/main.go") || !strings.Contains(l, "src/deep/util.go") || strings.Contains(l, "CLAUDE") {
		t.Fatalf("glob: %q", l)
	}
	if r := do(FSReq{Op: "read", Path: "src/main.go", Start: 3, Max: 1}); !strings.Contains(r, "3\tfunc main() {") || !strings.Contains(r, "read on from line 4") {
		t.Fatalf("read: %q", r)
	}
	if s := do(FSReq{Op: "search", Query: "PRINTLN"}); !strings.Contains(s, "src/main.go:4:") {
		t.Fatalf("search: %q", s)
	}
	if s := do(FSReq{Op: "search", Query: "/^package \\w+$/"}); strings.Count(s, "\n") != 1 {
		t.Fatalf("regex search: %q", s)
	}
	if e := do(FSReq{Op: "edit", Path: "src/main.go", Old: "\"hi\"", New: "\"hello\""}); !strings.HasPrefix(e, "Edited") {
		t.Fatal(e)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "src", "main.go")); !strings.Contains(string(b), "hello") {
		t.Fatal("edit did not land")
	}
	if e := do(FSReq{Op: "edit", Path: "src/main.go", Old: "nope", New: "x"}); !strings.Contains(e, "not in") {
		t.Fatalf("missing text: %q", e)
	}
	_ = os.WriteFile(filepath.Join(root, "twice.txt"), []byte("a a"), 0o644)
	if e := do(FSReq{Op: "edit", Path: "twice.txt", Old: "a", New: "b"}); !strings.Contains(e, "2 times") {
		t.Fatalf("ambiguous edit: %q", e)
	}
	if e := do(FSReq{Op: "edit", Path: "twice.txt", Old: "a", New: "b", All: true}); !strings.Contains(e, "2 replacements") {
		t.Fatalf("replace all: %q", e)
	}
	if w := do(FSReq{Op: "write", Path: "new/dir/file.txt", Content: "x"}); !strings.HasPrefix(w, "Wrote") {
		t.Fatal(w)
	}
	for _, bad := range []string{"../escape.txt", "/etc/passwd", "src/../../x"} {
		if w := do(FSReq{Op: "write", Path: bad, Content: "x"}); !strings.Contains(w, "outside the project") {
			t.Fatalf("%s: %q", bad, w)
		}
	}
	pi, err := projectInfo(t.TempDir(), root)
	if err != nil || pi.InstrFile != "CLAUDE.md" || pi.Files < 4 {
		t.Fatalf("project info: %+v %v", pi, err)
	}
}
