package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A task's file changes are kept, shown as diffs, and can be put back.
func TestTaskChangesAndUndo(t *testing.T) {
	_, st := withFakeModel(t)
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc A() int { return 1 }\n"), 0o644)
	s := st.create(&ProjectRef{Root: dir, Name: "p"}, Policy{Files: "edit"})
	// What runTool does before edit_file and write_file.
	if err := st.backupBefore(s, dir, "a.go"); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc A() int { return 2 }\n"), 0o644)
	if err := st.backupBefore(s, dir, "a.go"); err != nil { // a second change keeps the first copy
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc A() int { return 3 }\n"), 0o644)
	_ = st.backupBefore(s, dir, "new/b.go")
	_ = os.MkdirAll(filepath.Join(dir, "new"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "new/b.go"), []byte("package b\n"), 0o644)
	ch, err := st.changes(s)
	if err != nil || len(ch) != 2 {
		t.Fatalf("changes %+v %v", ch, err)
	}
	if ch[0].Path != "a.go" || ch[0].Kind != "changed" || !strings.Contains(ch[0].Diff, "-func A() int { return 1 }") || !strings.Contains(ch[0].Diff, "+func A() int { return 3 }") {
		t.Fatalf("diff:\n%s", ch[0].Diff)
	}
	if ch[1].Path != "new/b.go" || ch[1].Kind != "created" {
		t.Fatalf("%+v", ch[1])
	}
	if n, err := st.revert(s, ""); err != nil || n != 2 {
		t.Fatalf("revert %d %v", n, err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.go")); !strings.Contains(string(b), "return 1") {
		t.Fatal("a.go not put back")
	}
	if _, err := os.Stat(filepath.Join(dir, "new/b.go")); err == nil {
		t.Fatal("a created file was not removed")
	}
}

func TestGitContext(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	run := func(a ...string) {
		c := exec.Command("git", append([]string{"-C", dir}, a...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		_ = c.Run()
	}
	run("init", "-q", "-b", "main")
	_ = os.WriteFile(filepath.Join(dir, "x"), []byte("1"), 0o644)
	run("add", "x")
	run("commit", "-q", "-m", "first commit")
	_ = os.WriteFile(filepath.Join(dir, "y"), []byte("2"), 0o644)
	g := gitContext(dir)
	if !strings.Contains(g, "## main") || !strings.Contains(g, "?? y") || !strings.Contains(g, "first commit") {
		t.Fatalf("git context:\n%s", g)
	}
	if gitContext(t.TempDir()) != "" {
		t.Fatal("a folder without git got a git context")
	}
}
