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
	Existed bool      `json:"existed"` // false: the task created it
	At      time.Time `json:"at"`
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
	m := st.backupManifest(s)
	if _, ok := m[key]; ok {
		return nil // kept already: the first version is the one to go back to
	}
	e := backupEntry{At: time.Now()}
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
	if status == "working" {
		return 0, errors.New("stop the task first: it may be changing these files right now")
	}
	root := mustReal(proj.Root)
	s.mu.Lock()
	base, untracked := s.GitBase, s.GitUntracked
	s.mu.Unlock()
	if base != "" {
		return gitRevert(root, base, untracked, path)
	}
	m := st.backupManifest(s)
	n := 0
	for k, e := range m {
		if path != "" && k != path {
			continue
		}
		p, err := inRoot(root, k)
		if err != nil {
			return n, err
		}
		if e.Existed {
			b, err := os.ReadFile(filepath.Join(st.backupDir(s), "files", filepath.FromSlash(k)))
			if err != nil {
				return n, err
			}
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				return n, err
			}
			if err := os.WriteFile(p, b, 0o644); err != nil {
				return n, err
			}
		} else if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return n, err
		}
		n++
	}
	if path != "" && n == 0 {
		return 0, fmt.Errorf("this task did not change %s", path)
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

// gitStart records where a task in a git repository starts from (see the top of this file).
func gitStart(root string) (base string, untracked []string) {
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return "", nil
	}
	if b := gitOut(root, "stash", "create"); b != "" {
		base = b // the uncommitted state, as a commit nobody sees
	} else {
		base = gitOut(root, "rev-parse", "HEAD")
	}
	if base == "" {
		return "", nil
	}
	if u := gitOut(root, "ls-files", "--others", "--exclude-standard", "-z"); u != "" {
		untracked = strings.Split(strings.TrimRight(u, "\x00"), "\x00")
	}
	return base, untracked
}

// gitOut runs git in root and gives its output, or "" when it fails or takes too long.
func gitOut(root string, args ...string) string {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	hideWindow(cmd)
	done := make(chan []byte, 1)
	go func() {
		b, err := cmd.Output()
		if err != nil {
			b = nil
		}
		done <- b
	}()
	select {
	case b := <-done:
		return strings.TrimSpace(string(b))
	case <-time.After(20 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return ""
	}
}

// gitChanges lists what differs from the task's starting point.
func gitChanges(root, base string, untrackedThen []string) []TaskChange {
	was := map[string]bool{}
	for _, u := range untrackedThen {
		was[u] = true
	}
	var out []TaskChange
	for _, l := range strings.Split(gitOut(root, "diff", "--name-status", "--no-renames", base, "--"), "\n") {
		st, path, ok := strings.Cut(l, "\t")
		if !ok {
			continue
		}
		kind := map[string]string{"M": "changed", "A": "created", "D": "deleted", "T": "changed"}[st[:1]]
		if kind == "" {
			kind = "changed"
		}
		out = append(out, TaskChange{Path: path, Kind: kind, Diff: trimTo(gitOut(root, "diff", "--no-color", base, "--", path), 200<<10)})
	}
	if u := gitOut(root, "ls-files", "--others", "--exclude-standard", "-z"); u != "" {
		for _, f := range strings.Split(strings.TrimRight(u, "\x00"), "\x00") {
			if f == "" || was[f] {
				continue
			}
			b, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(f)))
			out = append(out, TaskChange{Path: f, Kind: "created", Diff: unifiedDiff(f, "", string(b))})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// gitRevert puts one file (or every changed file) back as it was at the task's start.
func gitRevert(root, base string, untrackedThen []string, path string) (int, error) {
	n := 0
	for _, c := range gitChanges(root, base, untrackedThen) {
		if path != "" && c.Path != path {
			continue
		}
		p, err := inRoot(root, c.Path)
		if err != nil {
			return n, err
		}
		if exec.Command("git", "-C", root, "cat-file", "-e", base+":"+c.Path).Run() != nil {
			// Not there at the start: the task made it.
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				return n, err
			}
		} else if err := exec.Command("git", "-C", root, "checkout", base, "--", c.Path).Run(); err != nil {
			return n, fmt.Errorf("could not put %s back: %v", c.Path, err)
		}
		n++
	}
	if path != "" && n == 0 {
		return 0, fmt.Errorf("%s is as it was when the task started", path)
	}
	return n, nil
}
