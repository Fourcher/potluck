package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A link inside the project must not lead the file tools out of it.
func TestProjectLinksStayInside(t *testing.T) {
	base := t.TempDir()
	proj, outside := filepath.Join(base, "proj"), filepath.Join(base, "home")
	_ = os.MkdirAll(filepath.Join(proj, "src"), 0o755)
	_ = os.MkdirAll(outside, 0o755)
	_ = os.WriteFile(filepath.Join(outside, "secret"), []byte("key"), 0o600)
	_ = os.WriteFile(filepath.Join(proj, "src", "a.go"), []byte("package a"), 0o644)
	if err := os.Symlink(outside, filepath.Join(proj, "docs")); err != nil {
		t.Skip("no symlinks here")
	}
	_ = os.Symlink(filepath.Join(outside, "secret"), filepath.Join(proj, "src", "s"))
	_ = os.Symlink(filepath.Join(proj, "src", "a.go"), filepath.Join(proj, "inside"))
	for _, rel := range []string{"docs/secret", "docs/new.txt", "src/s"} {
		if _, err := inRoot(proj, rel); err == nil {
			t.Fatalf("%s was allowed out of the project", rel)
		}
	}
	if _, err := projectFS(FSReq{Op: "write", Root: proj, Path: "docs/.zshrc", Content: "x"}); err == nil {
		t.Fatal("write through a link out of the project was allowed")
	}
	if _, err := os.Stat(filepath.Join(outside, ".zshrc")); err == nil {
		t.Fatal("a file was written outside the project")
	}
	for _, rel := range []string{"src/a.go", "inside", "src/new.go"} {
		if _, err := inRoot(proj, rel); err != nil {
			t.Fatalf("%s should be allowed: %v", rel, err)
		}
	}
	// The project itself reached through a link still works, with plain relative paths.
	alias := filepath.Join(base, "alias")
	_ = os.Symlink(proj, alias)
	files, _ := walkProject(alias, "", "", 100)
	got := strings.Join(files, ",")
	if strings.Contains(got, "secret") || strings.Contains(got, "src/s") || !strings.Contains(got, "src/a.go") {
		t.Fatalf("listing through a linked project: %s", got)
	}
}
