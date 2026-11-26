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

// In a git repository a task's changes are everything since it started, made however,
// and undo puts them back; what was uncommitted at the start stays as it was then.
func TestGitTaskChanges(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	git := func(a ...string) {
		c := exec.Command("git", append([]string{"-C", dir}, a...)...)
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		_ = c.Run()
	}
	git("init", "-q", "-b", "main")
	_ = os.WriteFile(filepath.Join(dir, "app.py"), []byte("print('hi')\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("1\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "start")
	_ = os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("1\n2 (uncommitted before the task)\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "mine.txt"), []byte("untracked before\n"), 0o644)
	base, untracked := gitStart(dir, "t1")
	if base == "" {
		t.Fatal("no starting point")
	}
	// The task, by command: sed, a new file, a deletion.
	_ = os.WriteFile(filepath.Join(dir, "app.py"), []byte("print('hello')\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("done\n"), 0o644)
	ch := gitChanges(dir, base, untracked)
	got := map[string]string{}
	for _, c := range ch {
		got[c.Path] = c.Kind
	}
	if len(ch) != 2 || got["app.py"] != "changed" || got["notes.txt"] != "created" {
		t.Fatalf("changes %v", got)
	}
	if n, err := gitRevert(dir, base, untracked, "", func(string) bool { return true }); err != nil || n != 2 {
		t.Fatalf("undo %d %v", n, err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "app.py"))
	k, _ := os.ReadFile(filepath.Join(dir, "keep.txt"))
	_, notes := os.Stat(filepath.Join(dir, "notes.txt"))
	_, mine := os.Stat(filepath.Join(dir, "mine.txt"))
	if string(b) != "print('hi')\n" || !strings.Contains(string(k), "uncommitted before") || notes == nil || mine != nil {
		t.Fatalf("after undo: app.py %q keep %q notes gone %v mine kept %v", b, k, notes != nil, mine == nil)
	}
}

// Undo never destroys the person's work: a file untracked before the task (even once the
// task staged it), the target of a link the task made, a file changed after the task, and
// names with accents all come out right.
func TestGitUndoKeepsThePersonsWork(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	_, st := withFakeModel(t)
	dir := t.TempDir()
	git := func(a ...string) { c := exec.Command("git", append([]string{"-C", dir}, a...)...); c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t"); _ = c.Run() }
	git("init", "-q", "-b", "main")
	_ = os.MkdirAll(filepath.Join(dir, "config"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "config", "app.json"), []byte("{}\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "café.txt"), []byte("1\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "later.txt"), []byte("1\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "start")
	_ = os.WriteFile(filepath.Join(dir, "draft.md"), []byte("my untracked draft\n"), 0o644)

	s := st.create(&ProjectRef{Root: dir, Name: "p"}, Policy{})
	s.GitBase, s.GitUntracked = gitStart(dir, s.ID)
	// The task: git add -A, a link, an accented file changed, later.txt changed.
	_ = os.WriteFile(filepath.Join(dir, "café.txt"), []byte("2\n"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, "later.txt"), []byte("2\n"), 0o644)
	_ = os.Symlink("config/app.json", filepath.Join(dir, "current.json"))
	git("add", "-A")
	s.Status = "idle"
	st.noteTaskEnd(s)
	// The person edits later.txt after the task.
	_ = os.WriteFile(filepath.Join(dir, "later.txt"), []byte("3 (mine)\n"), 0o644)

	ch, _ := st.changes(s)
	for _, c := range ch {
		if c.Path == "draft.md" {
			t.Fatal("the person's untracked draft is listed as the task's work")
		}
	}
	n, err := st.revert(s, "")
	if n != 2 || err == nil || !strings.Contains(err.Error(), "later.txt") {
		t.Fatalf("undo: %d %v", n, err)
	}
	read := func(f string) string { b, _ := os.ReadFile(filepath.Join(dir, f)); return string(b) }
	if read("draft.md") != "my untracked draft\n" {
		t.Fatal("undo deleted the person's untracked draft")
	}
	if read("config/app.json") != "{}\n" {
		t.Fatal("undo deleted the file a link pointed to")
	}
	if _, err := os.Lstat(filepath.Join(dir, "current.json")); err == nil {
		t.Fatal("the link the task made is still there")
	}
	if read("café.txt") != "1\n" {
		t.Fatal("a file with an accent was not put back")
	}
	if read("later.txt") != "3 (mine)\n" {
		t.Fatal("undo threw away the person's edit made after the task")
	}
	gitForget(dir, s.ID)
}
