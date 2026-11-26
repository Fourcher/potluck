package main

// Project tools: what the chat page's agent uses to work inside a code project on the
// computer running Potluck, the way a coding agent does: list and find files, read them,
// search them, write new ones and make exact edits to existing ones. Like /potluck/run, only
// the chat page on this computer may use them, and the page asks the person first unless they
// chose to let the agent work freely.
//
// Paths are relative to the project folder. A path that leaves the folder is refused, so a
// slip of the model cannot edit something else; commands (run_command) are how the agent
// reaches beyond the project when the person allows it.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type FSReq struct {
	Op      string `json:"op"`   // list | read | search | write | edit | project
	Root    string `json:"root"` // the project folder (absolute)
	Path    string `json:"path"`
	Start   int    `json:"start"` // read: first line (1-based)
	Max     int    `json:"max"`   // read: how many lines
	Query   string `json:"query"` // search: text or /regular expression/
	Glob    string `json:"glob"`  // list/search: only files matching this pattern (*.go, src/**/*.ts)
	Content string `json:"content"`
	Old     string `json:"old"`
	New     string `json:"new"`
	All     bool   `json:"all"` // edit: replace every occurrence
}

var fsSkip = map[string]bool{".git": true, "node_modules": true, ".venv": true, "venv": true, "__pycache__": true, "DerivedData": true, ".build": true, "dist": true, "build": true, ".next": true, "target": true}

// inRoot resolves a path inside the project, refusing anything outside it.
func inRoot(root, rel string) (string, error) {
	if root == "" || !filepath.IsAbs(root) {
		return "", errors.New("which project? (an absolute folder path)")
	}
	root = filepath.Clean(root)
	p := rel
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, rel)
	}
	p = filepath.Clean(p)
	if !within(root, p) {
		return "", fmt.Errorf("%s is outside the project folder", rel)
	}
	// The same check on the real paths: a link inside the project (docs -> the home folder)
	// must not lead the tools out of it. The project folder itself may sit behind a link.
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("the project folder %s cannot be opened: %v", root, err)
	}
	real, err := realPath(p)
	if err != nil {
		return "", err
	}
	if !within(realRoot, real) {
		return "", fmt.Errorf("%s leads outside the project folder (through a link)", rel)
	}
	return real, nil
}

func within(root, p string) bool {
	return p == root || strings.HasPrefix(p, root+string(os.PathSeparator))
}

// realPath resolves every link in p. For a file not made yet, it resolves the nearest
// folder that exists and adds the rest of the path back.
func realPath(p string) (string, error) {
	rest := ""
	for {
		r, err := filepath.EvalSymlinks(p)
		if err == nil {
			return filepath.Join(r, rest), nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", err
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

func matchGlob(pattern, rel string) bool {
	if pattern == "" {
		return true
	}
	rel = filepath.ToSlash(rel)
	if !strings.Contains(pattern, "/") {
		ok, _ := filepath.Match(pattern, filepath.Base(rel))
		return ok
	}
	// a/**/b: any number of folders in between.
	re := regexp.QuoteMeta(pattern)
	re = strings.ReplaceAll(re, `\*\*/`, `(.*/)?`)
	re = strings.ReplaceAll(re, `\*\*`, `.*`)
	re = strings.ReplaceAll(re, `\*`, `[^/]*`)
	re = strings.ReplaceAll(re, `\?`, `[^/]`)
	ok, _ := regexp.MatchString("^"+re+"$", rel)
	return ok
}

// walkProject lists the project's files (not the dependency and build folders), up to max.
func walkProject(root, sub, glob string, max int) ([]string, bool) {
	var out []string
	more := false
	start, err := inRoot(root, sub)
	if err != nil {
		return nil, false
	}
	// Paths are listed relative to the real project folder, which start is inside.
	if r, err := filepath.EvalSymlinks(filepath.Clean(root)); err == nil {
		root = r
	}
	_ = filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != start && (fsSkip[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if d.Type()&fs.ModeSymlink != 0 {
			if _, err := inRoot(root, rel); err != nil {
				return nil // a link to somewhere outside the project
			}
		}
		if !matchGlob(glob, rel) {
			return nil
		}
		if len(out) >= max {
			more = true
			return filepath.SkipAll
		}
		out = append(out, filepath.ToSlash(rel))
		return nil
	})
	sort.Strings(out)
	return out, more
}

func isText(b []byte) bool {
	n := len(b)
	if n > 8000 {
		n = 8000
	}
	for _, c := range b[:n] {
		if c == 0 {
			return false
		}
	}
	return true
}

func projectFS(in FSReq) (string, error) {
	switch in.Op {
	case "list":
		files, more := walkProject(in.Root, in.Path, in.Glob, 2000)
		if len(files) == 0 {
			return "(no files here)", nil
		}
		out := strings.Join(files, "\n")
		if more {
			out += "\n(more files not shown: list a subfolder or give a pattern)"
		}
		return out, nil

	case "read":
		p, err := inRoot(in.Root, in.Path)
		if err != nil {
			return "", err
		}
		st, err := os.Stat(p)
		if err != nil {
			return "", err
		}
		if st.IsDir() {
			return "", fmt.Errorf("%s is a folder: list it instead", in.Path)
		}
		if st.Size() > 20<<20 {
			return "", fmt.Errorf("%s is %s, too big to read; search it instead", in.Path, humanBytes(st.Size()))
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		if !isText(b) {
			return "", fmt.Errorf("%s is not a text file", in.Path)
		}
		lines := strings.Split(string(b), "\n")
		from, max := in.Start, in.Max
		if from < 1 {
			from = 1
		}
		if max <= 0 || max > 2000 {
			max = 2000
		}
		var sb strings.Builder
		for i := from - 1; i < len(lines) && i < from-1+max; i++ {
			fmt.Fprintf(&sb, "%6d\t%s\n", i+1, lines[i])
		}
		if from-1+max < len(lines) {
			fmt.Fprintf(&sb, "(lines %d to %d of %d; read on from line %d)\n", from, from-1+max, len(lines), from+max)
		}
		return sb.String(), nil

	case "search":
		if in.Query == "" {
			return "", errors.New("search for what?")
		}
		match := func(s string) bool { return strings.Contains(strings.ToLower(s), strings.ToLower(in.Query)) }
		if q := in.Query; len(q) > 2 && strings.HasPrefix(q, "/") && strings.HasSuffix(q, "/") {
			re, err := regexp.Compile(q[1 : len(q)-1])
			if err != nil {
				return "", fmt.Errorf("that pattern does not work: %w", err)
			}
			match = re.MatchString
		}
		files, _ := walkProject(in.Root, in.Path, in.Glob, 20000)
		var hits []string
		for _, rel := range files {
			if len(hits) >= 200 {
				break
			}
			f, err := os.Open(filepath.Join(in.Root, rel))
			if err != nil {
				continue
			}
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 1<<20), 1<<20)
			for n := 1; sc.Scan(); n++ {
				line := sc.Text()
				if n == 1 && !isText([]byte(line)) {
					break
				}
				if match(line) {
					if len(line) > 240 {
						line = line[:240] + "…"
					}
					hits = append(hits, fmt.Sprintf("%s:%d: %s", rel, n, strings.TrimSpace(line)))
					if len(hits) >= 200 {
						break
					}
				}
			}
			f.Close()
		}
		if len(hits) == 0 {
			return fmt.Sprintf("No match in %d files.", len(files)), nil
		}
		return strings.Join(hits, "\n"), nil

	case "write":
		p, err := inRoot(in.Root, in.Path)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		mode := os.FileMode(0o644)
		if st, err := os.Stat(p); err == nil {
			mode = st.Mode().Perm()
		}
		if err := os.WriteFile(p, []byte(in.Content), mode); err != nil {
			return "", err
		}
		return fmt.Sprintf("Wrote %s (%d lines).", in.Path, strings.Count(in.Content, "\n")+1), nil

	case "edit":
		p, err := inRoot(in.Root, in.Path)
		if err != nil {
			return "", err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		s := string(b)
		if in.Old == "" {
			return "", errors.New("give the exact text to replace (old)")
		}
		n := strings.Count(s, in.Old)
		switch {
		case n == 0:
			return "", fmt.Errorf("the text to replace is not in %s. Read the file again and copy the text exactly, spaces and line breaks included", in.Path)
		case n > 1 && !in.All:
			return "", fmt.Errorf("the text to replace appears %d times in %s. Include more of the surrounding lines so it is unique, or set all to replace every one", n, in.Path)
		}
		if in.All {
			s = strings.ReplaceAll(s, in.Old, in.New)
		} else {
			s = strings.Replace(s, in.Old, in.New, 1)
		}
		st, _ := os.Stat(p)
		if err := os.WriteFile(p, []byte(s), st.Mode().Perm()); err != nil {
			return "", err
		}
		count := 1
		if in.All {
			count = n
		}
		return fmt.Sprintf("Edited %s (%d %s).", in.Path, count, map[bool]string{true: "replacements", false: "replacement"}[count > 1]), nil
	}
	return "", errors.New("unknown file operation " + in.Op)
}

// ProjectInfo is what the page puts in the agent's instructions when a chat works in a project.
type ProjectInfo struct {
	Root         string    `json:"root"`
	Name         string    `json:"name"`
	Instructions string    `json:"instructions,omitempty"` // CLAUDE.md / AGENTS.md text
	InstrFile    string    `json:"instrFile,omitempty"`
	Memory       string    `json:"memory,omitempty"` // memory notes brought over for it
	Files        int       `json:"files"`
	Git          bool      `json:"git"`
	Modified     time.Time `json:"modified,omitempty"`
}

func projectInfo(data, root string) (ProjectInfo, error) {
	root = filepath.Clean(root)
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		return ProjectInfo{}, fmt.Errorf("%s is not a folder on this computer", root)
	}
	pi := ProjectInfo{Root: root, Name: filepath.Base(root), Modified: st.ModTime()}
	pi.InstrFile, pi.Instructions = projectInstructions(root)
	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		pi.Git = true
	}
	files, _ := walkProject(root, "", "", 20000)
	pi.Files = len(files)
	var known []ContProject
	_ = readJSONFile(filepath.Join(contDir(data), "projects.json"), &known)
	for _, p := range known {
		if filepath.Clean(p.Path) == root && p.Memory != "" {
			pi.Memory = projectMemory(data, p.Memory)
		}
	}
	// What Potluck's own tasks remembered about this project (the remember tool).
	if own := strings.TrimSpace(readSmall(ownMemoryFile(data, root, "project"), 1<<20)); own != "" {
		pi.Memory = strings.TrimSpace(pi.Memory + "\n\n" + own)
	}
	return pi, nil
}

// handleFS serves /potluck/fs for the chat page.
func handleFS(w http.ResponseWriter, r *http.Request, data string) {
	if !chatOwner(r) || r.Header.Get("X-Potluck") != "1" || !sameOrigin(r) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "project files can only be used from the chat page on the computer running Potluck"})
		return
	}
	var in FSReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<20)).Decode(&in); err != nil {
		fail(w, err)
		return
	}
	if in.Op == "project" {
		pi, err := projectInfo(data, in.Root)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, pi)
		return
	}
	out, err := projectFS(in)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]string{"text": out})
}
