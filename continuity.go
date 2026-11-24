package main

// Continuity: bring a person's existing AI setup into Potluck, so switching over takes
// minutes instead of a weekend of copying.
//
// Two ways in, both landing in <data>/continuity:
//
//  1. Potluck looks for itself (scanContinuity): Claude Code (global and project CLAUDE.md,
//     memory folders, skills, the project folders it has worked in, MCP servers, permission
//     rules), Claude Desktop (MCP servers), Codex (AGENTS.md, config), Cursor (MCP servers,
//     rules) and any shared folder of projects the person names (folders with CLAUDE.md,
//     AGENTS.md, .cursorrules or a git repository).
//  2. The person's current AI writes a bundle (importBundle): a copy-paste prompt in the
//     control panel asks it to gather preferences, memories, projects, skills and connectors
//     into one JSON document and hand it to Potluck. That reaches what lives in a cloud
//     account (ChatGPT's memory, for example) and cannot be read from disk.
//
// What is kept:
//
//	profile.md        who the person is and how they like to work (merged instructions)
//	memory/<scope>/   memory notes, one file each; scope "global" or a project's name
//	skills/<name>/    SKILL.md and whatever files came with it
//	projects.json     project folders: path, instructions file, memory scope, last used
//	connectors.json   MCP servers and other connectors, as configured (file mode 0600)
//	permissions.json  allow / deny rules carried over
//	sources.json      what was imported from where and when
//
// Nothing here is sent anywhere: it is read by Potluck's own chat and agent on this computer.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ContProject struct {
	Name         string    `json:"name"`
	Path         string    `json:"path"`
	Instructions string    `json:"instructions,omitempty"` // CLAUDE.md / AGENTS.md / .cursorrules found in it
	Memory       string    `json:"memory,omitempty"`       // memory scope with this project's notes
	LastUsed     time.Time `json:"lastUsed,omitempty"`
	Source       string    `json:"source"`
	Notes        string    `json:"notes,omitempty"`
}

type ContConnector struct {
	Name    string            `json:"name"`
	Kind    string            `json:"kind"` // "mcp-stdio", "mcp-http", or what a bundle says
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Scope   string            `json:"scope,omitempty"` // "global" or a project path
	Source  string            `json:"source"`
	Notes   string            `json:"notes,omitempty"`
}

type ContSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"`
}

type ContSource struct {
	ID       string    `json:"id"`   // "claude-code", "claude-desktop", "codex", "cursor", "folder:<path>", "bundle:<from>"
	Name     string    `json:"name"` // shown to the person
	Path     string    `json:"path,omitempty"`
	Profile  int       `json:"profile"` // instruction files found
	Memories int       `json:"memories"`
	Skills   int       `json:"skills"`
	Projects int       `json:"projects"`
	Conns    int       `json:"connectors"`
	Rules    int       `json:"rules"`
	Imported time.Time `json:"imported,omitempty"`
}

// contFound is everything one source offers, before it is written.
type contFound struct {
	src      ContSource
	profile  []contDoc // instruction files, in order
	memories []contDoc // scope = doc.Scope
	skills   []contSkillDir
	projects []ContProject
	conns    []ContConnector
	allow    []string
	deny     []string
}

type contDoc struct {
	Title, Scope, Text string
}

type contSkillDir struct {
	Name, Description, Dir, Text string // Dir: copy the whole folder; Text: only SKILL.md text
}

func contDir(data string) string { return filepath.Join(data, "continuity") }

// ---------- finding things on this computer ----------

// scanContinuity looks for every supported setup on this computer, plus the given folders.
func scanContinuity(home string, folders []string) []*contFound {
	var out []*contFound
	for _, f := range []func(string) *contFound{scanClaudeCode, scanClaudeDesktop, scanCodex, scanCursor} {
		if c := f(home); c != nil && c.total() > 0 {
			out = append(out, c)
		}
	}
	for _, dir := range folders {
		if c := scanFolder(dir); c != nil && c.total() > 0 {
			out = append(out, c)
		}
	}
	return out
}

func (c *contFound) total() int {
	return len(c.profile) + len(c.memories) + len(c.skills) + len(c.projects) + len(c.conns) + len(c.allow) + len(c.deny)
}

func (c *contFound) count() ContSource {
	s := c.src
	s.Profile, s.Memories, s.Skills, s.Projects, s.Conns, s.Rules = len(c.profile), len(c.memories), len(c.skills), len(c.projects), len(c.conns), len(c.allow)+len(c.deny)
	return s
}

func readSmall(path string, max int64) string {
	st, err := os.Stat(path)
	if err != nil || st.IsDir() || st.Size() > max {
		return ""
	}
	b, _ := os.ReadFile(path)
	return string(b)
}

// projectInstructions returns the instruction files a project folder carries.
func projectInstructions(dir string) (string, string) {
	for _, n := range []string{"CLAUDE.md", "AGENTS.md", ".cursorrules", filepath.Join(".github", "copilot-instructions.md")} {
		if t := readSmall(filepath.Join(dir, n), 1<<20); strings.TrimSpace(t) != "" {
			return n, t
		}
	}
	return "", ""
}

var frontMatter = regexp.MustCompile(`(?s)^---\s*\n(.*?)\n---`)

// skillMeta reads name and description from a SKILL.md's front matter.
func skillMeta(text, fallback string) (string, string) {
	name, desc := fallback, ""
	if m := frontMatter.FindStringSubmatch(text); m != nil {
		for _, line := range strings.Split(m[1], "\n") {
			k, v, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			v = strings.Trim(strings.TrimSpace(v), `"'`)
			switch strings.TrimSpace(k) {
			case "name":
				if v != "" {
					name = v
				}
			case "description":
				desc = v
			}
		}
	}
	return name, desc
}

func skillsIn(root, source string) []contSkillDir {
	var out []contSkillDir
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		dir := filepath.Join(root, e.Name())
		text := readSmall(filepath.Join(dir, "SKILL.md"), 2<<20)
		if text == "" {
			continue
		}
		name, desc := skillMeta(text, e.Name())
		out = append(out, contSkillDir{Name: name, Description: desc, Dir: dir})
	}
	return out
}

// claudeProjectKey is how Claude Code names a project's folder under ~/.claude/projects:
// every character other than a letter or digit becomes "-".
func claudeProjectKey(path string) string {
	return regexp.MustCompile(`[^A-Za-z0-9]`).ReplaceAllString(path, "-")
}

func mcpFrom(servers map[string]json.RawMessage, scope, source string) []ContConnector {
	var out []ContConnector
	for name, raw := range servers {
		var s struct {
			Type    string            `json:"type"`
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
			URL     string            `json:"url"`
		}
		if json.Unmarshal(raw, &s) != nil {
			continue
		}
		kind := "mcp-stdio"
		if s.URL != "" {
			kind = "mcp-http"
		}
		out = append(out, ContConnector{Name: name, Kind: kind, Command: s.Command, Args: s.Args, Env: s.Env, URL: s.URL, Scope: scope, Source: source})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func scanClaudeCode(home string) *contFound {
	base := filepath.Join(home, ".claude")
	if _, err := os.Stat(base); err != nil {
		return nil
	}
	c := &contFound{src: ContSource{ID: "claude-code", Name: "Claude Code", Path: base}}
	if t := readSmall(filepath.Join(base, "CLAUDE.md"), 1<<20); strings.TrimSpace(t) != "" {
		c.profile = append(c.profile, contDoc{Title: "Claude Code: global instructions (~/.claude/CLAUDE.md)", Text: t})
	}
	c.skills = append(c.skills, skillsIn(filepath.Join(base, "skills"), "claude-code")...)

	// Projects: ~/.claude.json lists every folder Claude Code has worked in.
	var cfg struct {
		MCP      map[string]json.RawMessage `json:"mcpServers"`
		Projects map[string]struct {
			MCP          map[string]json.RawMessage `json:"mcpServers"`
			AllowedTools []string                   `json:"allowedTools"`
		} `json:"projects"`
	}
	if b, err := os.ReadFile(filepath.Join(home, ".claude.json")); err == nil {
		_ = json.Unmarshal(b, &cfg)
	}
	c.conns = append(c.conns, mcpFrom(cfg.MCP, "global", "claude-code")...)
	seen := map[string]bool{}
	addProject := func(path string) {
		if seen[path] {
			return
		}
		st, err := os.Stat(path)
		if err != nil || !st.IsDir() {
			return
		}
		seen[path] = true
		p := ContProject{Name: filepath.Base(path), Path: path, Source: "claude-code"}
		p.Instructions, _ = projectInstructions(path)
		key := claudeProjectKey(path)
		if logs, _ := filepath.Glob(filepath.Join(base, "projects", key, "*.jsonl")); len(logs) > 0 {
			for _, l := range logs {
				if st, err := os.Stat(l); err == nil && st.ModTime().After(p.LastUsed) {
					p.LastUsed = st.ModTime()
				}
			}
		}
		mems, _ := filepath.Glob(filepath.Join(base, "projects", key, "memory", "*.md"))
		for _, m := range mems {
			if t := readSmall(m, 1<<20); strings.TrimSpace(t) != "" {
				c.memories = append(c.memories, contDoc{Title: filepath.Base(m), Scope: p.Name, Text: t})
				p.Memory = p.Name
			}
		}
		c.projects = append(c.projects, p)
	}
	for path, pc := range cfg.Projects {
		addProject(path)
		c.conns = append(c.conns, mcpFrom(pc.MCP, path, "claude-code")...)
	}
	// Memory folders whose project is not in ~/.claude.json still hold notes worth keeping.
	dirs, _ := filepath.Glob(filepath.Join(base, "projects", "*", "memory"))
	known := map[string]bool{}
	for _, p := range c.projects {
		known[claudeProjectKey(p.Path)] = true
	}
	for _, d := range dirs {
		key := filepath.Base(filepath.Dir(d))
		if known[key] {
			continue
		}
		mems, _ := filepath.Glob(filepath.Join(d, "*.md"))
		for _, m := range mems {
			if t := readSmall(m, 1<<20); strings.TrimSpace(t) != "" {
				c.memories = append(c.memories, contDoc{Title: filepath.Base(m), Scope: key, Text: t})
			}
		}
	}
	sort.Slice(c.projects, func(i, j int) bool { return c.projects[i].LastUsed.After(c.projects[j].LastUsed) })

	var set struct {
		Permissions struct {
			Allow []string `json:"allow"`
			Deny  []string `json:"deny"`
		} `json:"permissions"`
	}
	if b, err := os.ReadFile(filepath.Join(base, "settings.json")); err == nil {
		_ = json.Unmarshal(b, &set)
	}
	c.allow, c.deny = set.Permissions.Allow, set.Permissions.Deny
	return c
}

func scanClaudeDesktop(home string) *contFound {
	var path string
	for _, p := range []string{
		filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json"),
		filepath.Join(os.Getenv("APPDATA"), "Claude", "claude_desktop_config.json"),
		filepath.Join(home, ".config", "Claude", "claude_desktop_config.json"),
	} {
		if _, err := os.Stat(p); err == nil {
			path = p
			break
		}
	}
	if path == "" {
		return nil
	}
	var cfg struct {
		MCP map[string]json.RawMessage `json:"mcpServers"`
	}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &cfg)
	}
	return &contFound{src: ContSource{ID: "claude-desktop", Name: "Claude Desktop", Path: path}, conns: mcpFrom(cfg.MCP, "global", "claude-desktop")}
}

func scanCodex(home string) *contFound {
	base := filepath.Join(home, ".codex")
	if _, err := os.Stat(base); err != nil {
		return nil
	}
	c := &contFound{src: ContSource{ID: "codex", Name: "Codex", Path: base}}
	if t := readSmall(filepath.Join(base, "AGENTS.md"), 1<<20); strings.TrimSpace(t) != "" {
		c.profile = append(c.profile, contDoc{Title: "Codex: global instructions (~/.codex/AGENTS.md)", Text: t})
	}
	c.skills = append(c.skills, skillsIn(filepath.Join(base, "skills"), "codex")...)
	// MCP servers in config.toml: [mcp_servers.<name>] with command / args / url.
	if t := readSmall(filepath.Join(base, "config.toml"), 1<<20); t != "" {
		c.conns = append(c.conns, codexMCP(t)...)
	}
	return c
}

// codexMCP reads the [mcp_servers.<name>] tables of a Codex config.toml (just the keys used).
func codexMCP(toml string) []ContConnector {
	var out []ContConnector
	var cur *ContConnector
	str := regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
	for _, line := range strings.Split(toml, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			cur = nil
			if name, ok := strings.CutPrefix(strings.Trim(line, "[]"), "mcp_servers."); ok && !strings.Contains(name, ".") {
				out = append(out, ContConnector{Name: strings.Trim(name, `"`), Kind: "mcp-stdio", Scope: "global", Source: "codex"})
				cur = &out[len(out)-1]
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if cur == nil || !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "command":
			if m := str.FindStringSubmatch(v); m != nil {
				cur.Command = m[1]
			}
		case "url":
			if m := str.FindStringSubmatch(v); m != nil {
				cur.URL, cur.Kind = m[1], "mcp-http"
			}
		case "args":
			for _, m := range str.FindAllStringSubmatch(v, -1) {
				cur.Args = append(cur.Args, m[1])
			}
		}
	}
	return out
}

func scanCursor(home string) *contFound {
	path := filepath.Join(home, ".cursor", "mcp.json")
	if _, err := os.Stat(path); err != nil {
		return nil
	}
	var cfg struct {
		MCP map[string]json.RawMessage `json:"mcpServers"`
	}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, &cfg)
	}
	return &contFound{src: ContSource{ID: "cursor", Name: "Cursor", Path: path}, conns: mcpFrom(cfg.MCP, "global", "cursor")}
}

// scanFolder finds projects in a shared folder of work: any folder up to three levels down
// with instructions for an AI or a git repository. It does not go into the projects.
func scanFolder(root string) *contFound {
	st, err := os.Stat(root)
	if err != nil || !st.IsDir() {
		return nil
	}
	c := &contFound{src: ContSource{ID: "folder:" + root, Name: "Projects in " + filepath.Base(root), Path: root}}
	skip := map[string]bool{"node_modules": true, ".git": true, "venv": true, ".venv": true, "build": true, "dist": true, "DerivedData": true, "__pycache__": true}
	// A big folder (a synced Documents folder, say) can take long to list: look for at most
	// ten seconds and keep what was found by then.
	deadline := time.Now().Add(10 * time.Second)
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if time.Now().After(deadline) {
			c.src.Name = "Projects in " + filepath.Base(root) + " (stopped looking after 10 seconds)"
			return filepath.SkipAll
		}
		if err != nil || !d.IsDir() {
			return nil
		}
		if p != root && (skip[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
			return filepath.SkipDir
		}
		depth := strings.Count(strings.TrimPrefix(p, root), string(os.PathSeparator))
		instr, _ := projectInstructions(p)
		_, gitErr := os.Stat(filepath.Join(p, ".git"))
		if p != root && (instr != "" || gitErr == nil) {
			proj := ContProject{Name: d.Name(), Path: p, Instructions: instr, Source: c.src.ID}
			if st, err := os.Stat(p); err == nil {
				proj.LastUsed = st.ModTime()
			}
			c.projects = append(c.projects, proj)
			return filepath.SkipDir // a project: do not look for projects inside it
		}
		if depth >= 3 {
			return filepath.SkipDir
		}
		return nil
	})
	return c
}

// ---------- a bundle written by the person's own AI ----------

// ContBundle is the document the copy-paste prompt asks another AI to write.
type ContBundle struct {
	Version     int    `json:"potluckImport"`
	From        string `json:"from"`
	Profile     string `json:"profile"`
	Preferences string `json:"preferences"`
	Memories    []struct {
		Title   string `json:"title"`
		Text    string `json:"text"`
		Project string `json:"project"`
	} `json:"memories"`
	Skills []struct {
		Name         string `json:"name"`
		Description  string `json:"description"`
		Instructions string `json:"instructions"`
	} `json:"skills"`
	Projects []struct {
		Name  string `json:"name"`
		Path  string `json:"path"`
		Notes string `json:"notes"`
	} `json:"projects"`
	Connectors []ContConnector `json:"connectors"`
}

// bundleFound turns a bundle into the same shape as a scan, so both are saved the same way.
func bundleFound(raw []byte) (*contFound, error) {
	raw = []byte(strings.TrimSpace(string(raw)))
	// Accept the JSON on its own or inside a ```json fence, as chat apps show it.
	if i := strings.Index(string(raw), "{"); i > 0 {
		raw = raw[i:]
	}
	if j := strings.LastIndex(string(raw), "}"); j >= 0 {
		raw = raw[:j+1]
	}
	var b ContBundle
	if err := json.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("that is not a Potluck import (it should be the JSON your AI wrote): %w", err)
	}
	from := strings.TrimSpace(b.From)
	if from == "" {
		from = "another AI"
	}
	c := &contFound{src: ContSource{ID: "bundle:" + from, Name: from + " (written by your AI)"}}
	if t := strings.TrimSpace(b.Profile); t != "" {
		c.profile = append(c.profile, contDoc{Title: from + ": about you", Text: t})
	}
	if t := strings.TrimSpace(b.Preferences); t != "" {
		c.profile = append(c.profile, contDoc{Title: from + ": how you like to work", Text: t})
	}
	for i, m := range b.Memories {
		if strings.TrimSpace(m.Text) == "" {
			continue
		}
		title := m.Title
		if title == "" {
			title = fmt.Sprintf("memory-%d", i+1)
		}
		scope := "global"
		if m.Project != "" {
			scope = m.Project
		}
		c.memories = append(c.memories, contDoc{Title: title, Scope: scope, Text: m.Text})
	}
	for _, s := range b.Skills {
		if strings.TrimSpace(s.Instructions) == "" || s.Name == "" {
			continue
		}
		text := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n%s\n", s.Name, strings.ReplaceAll(s.Description, "\n", " "), s.Instructions)
		c.skills = append(c.skills, contSkillDir{Name: s.Name, Description: s.Description, Text: text})
	}
	for _, p := range b.Projects {
		if p.Path == "" && p.Name == "" {
			continue
		}
		name := p.Name
		if name == "" {
			name = filepath.Base(p.Path)
		}
		proj := ContProject{Name: name, Path: p.Path, Notes: p.Notes, Source: c.src.ID}
		if p.Path != "" {
			proj.Instructions, _ = projectInstructions(p.Path)
		}
		c.projects = append(c.projects, proj)
	}
	for _, k := range b.Connectors {
		k.Source = c.src.ID
		if k.Kind == "" {
			k.Kind = "mcp-stdio"
		}
		c.conns = append(c.conns, k)
	}
	if c.total() == 0 {
		return nil, errors.New("the import is empty: ask your AI to fill in at least one section")
	}
	return c, nil
}

// ---------- saving ----------

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._ -]+`)

func contName(s string) string {
	s = strings.TrimSpace(unsafeName.ReplaceAllString(s, "-"))
	if s == "" || s == "." || s == ".." {
		s = "untitled"
	}
	if len(s) > 80 {
		s = s[:80]
	}
	return s
}

// saveContinuity writes what was found into <data>/continuity, merging with what is there.
func saveContinuity(data string, found []*contFound) ([]ContSource, error) {
	dir := contDir(data)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	var sources []ContSource
	_ = readJSONFile(filepath.Join(dir, "sources.json"), &sources)
	var projects []ContProject
	_ = readJSONFile(filepath.Join(dir, "projects.json"), &projects)
	var conns []ContConnector
	_ = readJSONFile(filepath.Join(dir, "connectors.json"), &conns)
	var perms struct {
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
	}
	_ = readJSONFile(filepath.Join(dir, "permissions.json"), &perms)

	profile := readSmall(filepath.Join(dir, "profile.md"), 8<<20)
	for _, c := range found {
		for _, d := range c.profile {
			marker := "<!-- from: " + d.Title + " -->"
			if i := strings.Index(profile, marker); i >= 0 { // imported again: replace that part
				end := strings.Index(profile[i+len(marker):], "<!-- from: ")
				if end < 0 {
					profile = profile[:i]
				} else {
					profile = profile[:i] + profile[i+len(marker)+end:]
				}
			}
			profile += marker + "\n## " + d.Title + "\n\n" + strings.TrimSpace(d.Text) + "\n\n"
		}
		for _, m := range c.memories {
			scope := safeName(m.Scope)
			if m.Scope == "" {
				scope = "global"
			}
			md := filepath.Join(dir, "memory", scope)
			_ = os.MkdirAll(md, 0o700)
			name := safeName(strings.TrimSuffix(m.Title, ".md")) + ".md"
			if err := os.WriteFile(filepath.Join(md, name), []byte(m.Text), 0o600); err != nil {
				return nil, err
			}
		}
		for _, s := range c.skills {
			sd := filepath.Join(dir, "skills", safeName(s.Name))
			_ = os.RemoveAll(sd)
			if s.Dir != "" {
				if err := copyTree(s.Dir, sd, 50<<20); err != nil {
					return nil, err
				}
			} else {
				_ = os.MkdirAll(sd, 0o700)
				if err := os.WriteFile(filepath.Join(sd, "SKILL.md"), []byte(s.Text), 0o600); err != nil {
					return nil, err
				}
			}
		}
		for _, p := range c.projects {
			replaced := false
			for i := range projects {
				if p.Path != "" && projects[i].Path == p.Path {
					if p.Memory == "" {
						p.Memory = projects[i].Memory
					}
					projects[i], replaced = p, true
				}
			}
			if !replaced {
				projects = append(projects, p)
			}
		}
		for _, k := range c.conns {
			replaced := false
			for i := range conns {
				if conns[i].Name == k.Name && conns[i].Scope == k.Scope {
					conns[i], replaced = k, true
				}
			}
			if !replaced {
				conns = append(conns, k)
			}
		}
		perms.Allow = mergeStrings(perms.Allow, c.allow)
		perms.Deny = mergeStrings(perms.Deny, c.deny)
		s := c.count()
		s.Imported = time.Now()
		kept := sources[:0]
		for _, old := range sources {
			if old.ID != s.ID {
				kept = append(kept, old)
			}
		}
		sources = append(kept, s)
	}
	sort.SliceStable(projects, func(i, j int) bool { return projects[i].LastUsed.After(projects[j].LastUsed) })
	if err := os.WriteFile(filepath.Join(dir, "profile.md"), []byte(profile), 0o600); err != nil {
		return nil, err
	}
	for name, v := range map[string]any{"projects.json": projects, "connectors.json": conns, "permissions.json": perms, "sources.json": sources} {
		if err := writeJSONFile(filepath.Join(dir, name), v); err != nil {
			return nil, err
		}
	}
	return sources, nil
}

func mergeStrings(a, b []string) []string {
	seen := map[string]bool{}
	for _, s := range a {
		seen[s] = true
	}
	for _, s := range b {
		if !seen[s] {
			a, seen[s] = append(a, s), true
		}
	}
	return a
}

func readJSONFile(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// writeJSONFile writes privately (0600): connectors can carry keys in their settings.
func writeJSONFile(path string, v any) error {
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// copyTree copies a folder (a skill and its files), up to max bytes in all.
func copyTree(src, dst string, max int64) error {
	var total int64
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return os.MkdirAll(target, 0o700)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if total += info.Size(); total > max {
			return fmt.Errorf("skill %s is larger than %s; copy it by hand if you need all of it", filepath.Base(src), humanBytes(max))
		}
		in, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, in)
		out.Close()
		return err
	})
}

// ---------- what the chat and agent read ----------

// ContContext is what Potluck's chat and agent use from the imported setup.
type ContContext struct {
	Profile    string          `json:"profile"`
	Memory     string          `json:"memory"` // global memory notes, joined
	Skills     []ContSkill     `json:"skills"`
	Projects   []ContProject   `json:"projects"`
	Connectors []ContConnector `json:"connectors"` // settings without their environment values
	Sources    []ContSource    `json:"sources"`
}

func loadContContext(data string) ContContext {
	dir := contDir(data)
	var c ContContext
	// The "from" markers only help re-imports replace their own part; the model need not see them.
	c.Profile = strings.TrimSpace(regexp.MustCompile(`<!-- from: .*? -->\n`).ReplaceAllString(readSmall(filepath.Join(dir, "profile.md"), 8<<20), ""))
	var mem []string
	files, _ := filepath.Glob(filepath.Join(dir, "memory", "global", "*.md"))
	sort.Strings(files)
	for _, f := range files {
		if t := strings.TrimSpace(readSmall(f, 1<<20)); t != "" {
			mem = append(mem, t)
		}
	}
	c.Memory = strings.Join(mem, "\n\n---\n\n")
	skills, _ := os.ReadDir(filepath.Join(dir, "skills"))
	for _, s := range skills {
		text := readSmall(filepath.Join(dir, "skills", s.Name(), "SKILL.md"), 2<<20)
		if text == "" {
			continue
		}
		name, desc := skillMeta(text, s.Name())
		c.Skills = append(c.Skills, ContSkill{Name: name, Description: desc, Source: s.Name()})
	}
	_ = readJSONFile(filepath.Join(dir, "projects.json"), &c.Projects)
	_ = readJSONFile(filepath.Join(dir, "connectors.json"), &c.Connectors)
	for i := range c.Connectors {
		for k := range c.Connectors[i].Env {
			c.Connectors[i].Env[k] = "(set)" // values stay on disk; the pages only learn they exist
		}
	}
	_ = readJSONFile(filepath.Join(dir, "sources.json"), &c.Sources)
	return c
}

// readSkill returns a skill's SKILL.md by name, for the agent's use_skill tool.
func readSkill(data, name string) (string, error) {
	dir := filepath.Join(contDir(data), "skills")
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		text := readSmall(filepath.Join(dir, e.Name(), "SKILL.md"), 2<<20)
		if n, _ := skillMeta(text, e.Name()); text != "" && (strings.EqualFold(n, name) || strings.EqualFold(e.Name(), name)) {
			return text, nil
		}
	}
	return "", fmt.Errorf("no skill called %q", name)
}

// projectMemory returns a project's memory notes joined, for when the agent works in it.
func projectMemory(data, scope string) string {
	if scope == "" {
		return ""
	}
	files, _ := filepath.Glob(filepath.Join(contDir(data), "memory", safeName(scope), "*.md"))
	sort.Strings(files)
	var out []string
	for _, f := range files {
		if t := strings.TrimSpace(readSmall(f, 1<<20)); t != "" {
			out = append(out, t)
		}
	}
	return strings.Join(out, "\n\n---\n\n")
}

// continuityPrompt is what the person copies into their current AI (Claude, ChatGPT, Codex,
// Cursor...) to bring what that AI knows about them over to Potluck.
func continuityPrompt() string {
	base := "http://127.0.0.1:" + strconv.Itoa(portChat)
	return `I'm moving my work to Potluck, a private AI that runs on my own computers, and I want it to pick up where you and I are. Please help me bring my setup over. Potluck already reads Claude Code, Claude Desktop, Codex and Cursor files on this computer by itself, so focus on what only you know or can reach: things you remember about me, my preferences, my projects and the ways we work together.

Gather all of this, as completely and specifically as you can:
1. profile: who I am, my work, what I'm working towards, people and tools I deal with.
2. preferences: how I like you to work and talk (tone, level of detail, things I've asked you to always or never do).
3. memories: every saved memory or fact you keep about me, one per item, each with a short title. Add "project" when a memory belongs to one project.
4. projects: the projects we've worked on, each with its name, its folder path on this computer if there is one, and a few lines on what it is and where it stands.
5. skills: any custom instructions, workflows, prompts or skills I've set up with you, each with a name, a one-line description, and the full instructions.
6. connectors: the integrations, MCP servers, plugins or apps you use for me, with what each is for and how it's set up. Do not include passwords, API keys or tokens; say which ones a connector needs instead.

Get it the easiest way you can. Read files and run scripts first. If something only shows up inside an app or a web page (a memory settings page, for example), and you can control the screen or a browser, use that. Don't make things up: leave out anything you aren't sure of.

Then hand it to Potluck as one JSON document of this shape:
{"potluckImport": 1, "from": "<your name, e.g. ChatGPT or Claude>", "profile": "...", "preferences": "...",
 "memories": [{"title": "...", "text": "...", "project": "..."}],
 "projects": [{"name": "...", "path": "...", "notes": "..."}],
 "skills": [{"name": "...", "description": "...", "instructions": "..."}],
 "connectors": [{"name": "...", "kind": "mcp-stdio | mcp-http | app", "command": "...", "args": ["..."], "url": "...", "notes": "..."}]}

How to hand it over, in this order of preference:
- If you can make web requests or run commands on this computer: POST it to ` + base + `/potluck/import with the headers "Content-Type: application/json" and "X-Potluck: 1". Potluck answers with what it imported.
- Otherwise, if you can write files: save it as potluck-import.json in my Downloads folder.
- Otherwise: show it to me in one code block, and I'll paste it into Potluck (Computers, then "Bring your AI setup over", then "Paste what your AI wrote").

Finally, tell me in a few lines what you handed over and anything you couldn't include.`
}

// suggestFolders proposes folders to look for projects in: the folders that hold the
// projects already known, then the usual places people keep code.
func suggestFolders(data, home string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p == "" || seen[p] || p == home || p == "/" {
			return
		}
		if st, err := os.Stat(p); err == nil && st.IsDir() {
			seen[p] = true
			out = append(out, p)
		}
	}
	var known []ContProject
	_ = readJSONFile(filepath.Join(contDir(data), "projects.json"), &known)
	count := map[string]int{}
	for _, p := range known {
		count[filepath.Dir(p.Path)]++
	}
	// Before anything is imported: the folders Claude Code has worked in.
	var cc struct {
		Projects map[string]json.RawMessage `json:"projects"`
	}
	if readJSONFile(filepath.Join(home, ".claude.json"), &cc) == nil {
		for path := range cc.Projects {
			count[filepath.Dir(path)]++
		}
	}
	type kv struct {
		dir string
		n   int
	}
	var parents []kv
	for d, n := range count {
		parents = append(parents, kv{d, n})
	}
	sort.Slice(parents, func(i, j int) bool { return parents[i].n > parents[j].n })
	for _, p := range parents {
		if p.n >= 2 {
			add(p.dir)
		}
	}
	for _, n := range []string{"Projects", "projects", "Code", "code", "Developer", "dev", "src", "repos", "GitHub", "Documents"} {
		add(filepath.Join(home, n))
	}
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}
