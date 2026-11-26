package main

// What a task changed in its project, and undoing it. A task left to work on a codebase for
// the night is reviewed in the morning.
//
// In a git repository the task's starting point is recorded when it starts (git stash create:
// the files as they were, uncommitted changes included, without touching them), with the list
// of untracked files then. Its changes are everything that differs from that, however they
// were made (its file tools, sed, a build, a script), and undo puts files back from it.
// Elsewhere, a file is kept before the task first changes it with edit_file or write_file;
// changes made by commands are not kept there, and the page says so.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type backupEntry struct {
	Existed bool        `json:"existed"` // false: the task created it
	At      time.Time   `json:"at"`
	Mode    os.FileMode `json:"mode,omitempty"` // put back with it (a script stays runnable)
}

// TaskChange is one file a task changed, as shown in its changes view.
type TaskChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"` // changed | created | deleted (gone since) | same (back as it was)
	Diff string `json:"diff,omitempty"`
}

func (st *sessionStore) backupDir(s *Session) string { return filepath.Join(st.dir, s.ID+"-backup") }

func (st *sessionStore) backupManifest(s *Session) map[string]backupEntry {
	m := map[string]backupEntry{}
	_ = readJSONFile(filepath.Join(st.backupDir(s), "manifest.json"), &m)
	return m
}

// backupBefore keeps a file as it is before the task first changes it.
func (st *sessionStore) backupBefore(s *Session, root, rel string) error {
	p, err := inRoot(root, rel)
	if err != nil {
		return err
	}
	key, _ := filepath.Rel(filepath.Clean(mustReal(root)), p)
	key = filepath.ToSlash(key)
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	if s.deleted {
		return errors.New("this task was deleted")
	}
	m := st.backupManifest(s)
	if _, ok := m[key]; ok {
		return nil // kept already: the first version is the one to go back to
	}
	e := backupEntry{At: time.Now()}
	if fi, err := os.Stat(p); err == nil {
		e.Mode = fi.Mode().Perm()
	}
	if b, err := os.ReadFile(p); err == nil {
		dst := filepath.Join(st.backupDir(s), "files", filepath.FromSlash(key))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(dst, b, 0o600); err != nil {
			return err
		}
		e.Existed = true
	} else if !os.IsNotExist(err) {
		return err
	}
	m[key] = e
	if err := os.MkdirAll(st.backupDir(s), 0o700); err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(st.backupDir(s), "manifest.json"), m)
}

func mustReal(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// changes lists what the task changed, with a diff for each file.
func (st *sessionStore) changes(s *Session) ([]TaskChange, error) {
	s.mu.Lock()
	proj := s.Project
	s.mu.Unlock()
	if proj == nil {
		return nil, errors.New("this task has no project folder")
	}
	root := mustReal(proj.Root)
	s.mu.Lock()
	base, untracked := s.GitBase, s.GitUntracked
	s.mu.Unlock()
	if base != "" {
		return gitChanges(root, base, untracked), nil
	}
	m := st.backupManifest(s)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []TaskChange
	for _, k := range keys {
		e := m[k]
		var before []byte
		if e.Existed {
			before, _ = os.ReadFile(filepath.Join(st.backupDir(s), "files", filepath.FromSlash(k)))
		}
		now, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(k)))
		c := TaskChange{Path: k}
		switch {
		case err != nil && e.Existed:
			c.Kind, c.Diff = "deleted", unifiedDiff(k, string(before), "")
		case err != nil:
			continue // created, then removed again: nothing left to show
		case !e.Existed:
			c.Kind, c.Diff = "created", unifiedDiff(k, "", string(now))
		case string(before) == string(now):
			c.Kind = "same"
		default:
			c.Kind, c.Diff = "changed", unifiedDiff(k, string(before), string(now))
		}
		out = append(out, c)
	}
	return out, nil
}

// revert puts a file (or every file, for "") back as it was before the task changed it.
func (st *sessionStore) revert(s *Session, path string) (int, error) {
	s.mu.Lock()
	proj, status := s.Project, s.Status
	s.mu.Unlock()
	if proj == nil {
		return 0, errors.New("this task has no project folder")
	}
	if status == "working" || status == "waiting" {
		return 0, errors.New("stop the task first: it may be changing these files right now")
	}
	root := mustReal(proj.Root)
	s.mu.Lock()
	base, untracked, end := s.GitBase, s.GitUntracked, s.TaskEnd
	s.mu.Unlock()
	// A file changed since the task left it (by the person, by another task) is not undone:
	// that would throw their work away too.
	var since []string
	mayUndo := func(rel string) bool {
		if h, ok := end[rel]; ok && fileHash(root, rel) != h {
			since = append(since, rel)
			return false
		}
		return true
	}
	var n int
	var err error
	if base != "" {
		n, err = gitRevert(root, base, untracked, path, mayUndo)
	} else {
		n, err = st.backupRevert(s, root, path, mayUndo)
	}
	if err != nil {
		return n, err
	}
	if len(since) > 0 {
		return n, fmt.Errorf("%d put back; left as they are, because they changed after the task: %s", n, strings.Join(since, ", "))
	}
	if path != "" && n == 0 {
		return 0, fmt.Errorf("%s is as it was when the task started", path)
	}
	return n, nil
}

func (st *sessionStore) backupRevert(s *Session, root, path string, mayUndo func(string) bool) (int, error) {
	m := st.backupManifest(s)
	n := 0
	for k, e := range m {
		if path != "" && k != path || !mayUndo(k) {
			continue
		}
		if e.Existed {
			p, err := inRoot(root, k)
			if err != nil {
				return n, err
			}
			b, err := os.ReadFile(filepath.Join(st.backupDir(s), "files", filepath.FromSlash(k)))
			if err != nil {
				return n, err
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return n, err
			}
			mode := e.Mode
			if mode == 0 {
				mode = 0o644
			}
			if err := os.WriteFile(p, b, mode); err != nil {
				return n, err
			}
			_ = os.Chmod(p, mode) // an existing file keeps its old mode otherwise
		} else if err := removeInRoot(root, k); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// unifiedDiff shows the lines that differ, with three lines around each change. Very large
// files are summed up instead (the comparison grows with the product of their lengths).
func unifiedDiff(name, a, b string) string {
	al, bl := splitLines(a), splitLines(b)
	if len(al)*len(bl) > 16_000_000 {
		return fmt.Sprintf("(%s: %d lines before, %d now; too large to compare here)", name, len(al), len(bl))
	}
	// Longest common subsequence, then walk it.
	n, m := len(al), len(bl)
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if al[i] == bl[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}
	type op struct {
		k    byte // ' ' '-' '+'
		text string
		a, b int
	}
	var ops []op
	i, j := 0, 0
	for i < n || j < m {
		switch {
		case i < n && j < m && al[i] == bl[j]:
			ops = append(ops, op{' ', al[i], i, j})
			i, j = i+1, j+1
		case j < m && (i == n || lcs[i][j+1] >= lcs[i+1][j]):
			ops = append(ops, op{'+', bl[j], i, j})
			j++
		default:
			ops = append(ops, op{'-', al[i], i, j})
			i++
		}
	}
	var out strings.Builder
	fmt.Fprintf(&out, "--- %s (before)\n+++ %s (now)\n", name, name)
	const ctx = 3
	for k := 0; k < len(ops); {
		if ops[k].k == ' ' {
			k++
			continue
		}
		start := max(0, k-ctx)
		end := k
		for end < len(ops) {
			if ops[end].k != ' ' {
				end++
				continue
			}
			run := end
			for run < len(ops) && ops[run].k == ' ' {
				run++
			}
			if run-end > 2*ctx || run == len(ops) {
				end = min(end+ctx, len(ops))
				break
			}
			end = run
		}
		fmt.Fprintf(&out, "@@ line %d @@\n", ops[start].a+1)
		for _, o := range ops[start:end] {
			out.WriteByte(o.k)
			out.WriteString(o.text)
			out.WriteByte('\n')
		}
		k = end
		if out.Len() > 400<<10 {
			out.WriteString("… (more changes not shown)\n")
			break
		}
	}
	return out.String()
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// gitContext is what a task starting in a git repository is told about its state: the
// branch, what is uncommitted, and the latest commits. Empty when git is not there.
func gitContext(root string) string {
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return ""
	}
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		hideWindow(cmd)
		done := make(chan []byte, 1)
		go func() { b, _ := cmd.Output(); done <- b }()
		select {
		case b := <-done:
			return strings.TrimSpace(string(b))
		case <-time.After(10 * time.Second):
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			return ""
		}
	}
	status := run("status", "--short", "--branch")
	if status == "" {
		return ""
	}
	if lines := strings.Split(status, "\n"); len(lines) > 41 {
		status = strings.Join(lines[:41], "\n") + fmt.Sprintf("\n… and %d more", len(lines)-41)
	}
	out := "(The project's git state as the task starts.)\n\n$ git status --short --branch\n" + status
	if log := run("log", "--oneline", "-5"); log != "" {
		out += "\n\n$ git log --oneline -5\n" + log
	}
	return out
}

// gitStart records where a task in a git repository starts from (see the top of this file):
// the files as they are (git stash create; HEAD when nothing is uncommitted), kept under
// refs/potluck/<task> so git's clean-up never removes it, and the files untracked then (never
// listed as the task's work, never removed by undo: there is no copy of them to go back to).
func gitStart(root, task string) (base string, untracked []string) {
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return "", nil
	}
	if b := gitOut(root, "stash", "create"); b != "" {
		base = b
	} else {
		base = gitOut(root, "rev-parse", "HEAD")
	}
	if base == "" {
		return "", nil
	}
	gitOut(root, "update-ref", "refs/potluck/"+task, base)
	return base, gitList(root, "ls-files", "--others", "--exclude-standard", "-z")
}

// gitForget drops a task's starting point when the task is deleted.
func gitForget(root, task string) {
	gitOut(root, "update-ref", "-d", "refs/potluck/"+task)
}

// gitOut runs git in root and gives its output, or "" when it fails or takes too long.
// Paths are never quoted (core.quotePath) and never read as patterns (--literal-pathspecs).
func gitOut(root string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root, "-c", "core.quotePath=false", "--literal-pathspecs"}, args...)...)
	hideWindow(cmd)
	b, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// gitList is a -z list from git.
func gitList(root string, args ...string) []string {
	var out []string
	for _, f := range strings.Split(gitOut(root, args...), "\x00") {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// gitChanges lists what differs from the task's starting point.
func gitChanges(root, base string, untrackedThen []string) []TaskChange {
	was := map[string]bool{}
	for _, u := range untrackedThen {
		was[u] = true
	}
	var out []TaskChange
	// -z: "M\0path\0" pairs, whatever characters the names hold.
	f := gitList(root, "diff", "--name-status", "--no-renames", "-z", base, "--")
	for i := 0; i+1 < len(f); i += 2 {
		st, path := f[i], f[i+1]
		if was[path] {
			continue // there before the task, untracked: not its work, and no copy to go back to
		}
		kind := map[string]string{"M": "changed", "A": "created", "D": "deleted", "T": "changed"}[st[:1]]
		if kind == "" {
			kind = "changed"
		}
		out = append(out, TaskChange{Path: path, Kind: kind, Diff: trimTo(gitOut(root, "diff", "--no-color", base, "--", path), 200<<10)})
	}
	for _, f := range gitList(root, "ls-files", "--others", "--exclude-standard", "-z") {
		if was[f] {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
		out = append(out, TaskChange{Path: f, Kind: "created", Diff: unifiedDiff(f, "", string(b))})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// gitRevert puts one file (or every changed file) back as it was at the task's start.
func gitRevert(root, base string, untrackedThen []string, path string, mayUndo func(string) bool) (int, error) {
	n := 0
	for _, c := range gitChanges(root, base, untrackedThen) {
		if path != "" && c.Path != path || !mayUndo(c.Path) {
			continue
		}
		if exec.Command("git", "-C", root, "--literal-pathspecs", "cat-file", "-e", base+":"+c.Path).Run() != nil {
			// Not there at the start: the task made it.
			if err := removeInRoot(root, c.Path); err != nil {
				return n, err
			}
		} else {
			// The working copy only: what the person had staged stays staged.
			cmd := exec.Command("git", "-C", root, "--literal-pathspecs", "restore", "--source="+base, "--worktree", "--", c.Path)
			if out, err := cmd.CombinedOutput(); err != nil {
				return n, fmt.Errorf("could not put %s back: %s", c.Path, strings.TrimSpace(string(out)))
			}
		}
		n++
	}
	return n, nil
}

// removeInRoot removes a file the task made: the entry itself (a link, not what it points
// to), after checking that its folder is inside the project.
func removeInRoot(root, rel string) error {
	p := filepath.Join(root, filepath.FromSlash(rel))
	if _, err := inRoot(root, filepath.Dir(rel)); err != nil {
		return err
	}
	if !within(filepath.Clean(root), filepath.Clean(p)) {
		return fmt.Errorf("%s is outside the project folder", rel)
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// fileHash is what a file holds, for telling whether it changed; "" when it is not there.
func fileHash(root, rel string) string {
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

// noteTaskEnd records what the task left in each file it changed, at the end of a run, so undo
// can leave alone a file changed since (by the person, or another task).
func (st *sessionStore) noteTaskEnd(s *Session) {
	ch, err := st.changes(s)
	if err != nil {
		return
	}
	s.mu.Lock()
	root := mustReal(s.Project.Root)
	s.mu.Unlock()
	end := map[string]string{}
	for _, c := range ch {
		end[c.Path] = fileHash(root, c.Path)
	}
	s.mu.Lock()
	s.TaskEnd = end
	s.mu.Unlock()
}
