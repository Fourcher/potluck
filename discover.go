package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Finding models and engines that are already on this computer, put there by other apps
// (LM Studio, Ollama, Hugging Face downloads, llama.cpp) or by hand, so they can be used
// without downloading them again. Potluck never moves, changes or deletes these files: it
// only remembers where they are (Config.Found) and reads them.

// FoundModel is a model file found on this computer.
type FoundModel struct {
	Name   string   `json:"name"`  // the file name Potluck knows it by
	Path   string   `json:"path"`  // where it is (the first file, for a model in several files)
	Parts  []string `json:"parts"` // every file, in order
	From   string   `json:"from"`  // which app put it there
	Size   int64    `json:"size"`
	Title  string   `json:"title"`
	Arch   string   `json:"arch"`
	Quant  string   `json:"quant"`
	Ctx    int      `json:"ctx"`
	Params uint64   `json:"params"`
	Error  string   `json:"error,omitempty"` // could not be read
	InUse  bool     `json:"inUse"`           // already in Potluck's list
}

// FoundEngine is a llama.cpp server program found on this computer.
type FoundEngine struct {
	Path  string `json:"path"`
	Build int    `json:"build"` // 0 = could not tell
	Note  string `json:"note"`
}

type searchRoot struct {
	dir, from string
	depth     int
}

func homeDir() string { h, _ := os.UserHomeDir(); return h }

// modelRoots lists the places where apps keep model files, on this operating system.
func modelRoots(extra []string, potluckModels string) []searchRoot {
	home := homeDir()
	env := func(k string) string { return os.Getenv(k) }
	var r []searchRoot
	add := func(dir, from string, depth int) {
		if dir != "" {
			r = append(r, searchRoot{dir, from, depth})
		}
	}
	// LM Studio: the current folder, and the older one.
	add(filepath.Join(home, ".lmstudio", "models"), "LM Studio", 4)
	add(filepath.Join(home, ".cache", "lm-studio", "models"), "LM Studio", 4)
	// Hugging Face downloads (huggingface-cli, transformers, llama.cpp's -hf on newer builds).
	hub := env("HF_HUB_CACHE")
	if hub == "" && env("HF_HOME") != "" {
		hub = filepath.Join(env("HF_HOME"), "hub")
	}
	if hub == "" {
		hub = filepath.Join(home, ".cache", "huggingface", "hub")
	}
	add(hub, "Hugging Face", 5)
	// llama.cpp's own download folder.
	if c := env("LLAMA_CACHE"); c != "" {
		add(c, "llama.cpp", 4)
	}
	switch runtime.GOOS {
	case "darwin":
		add(filepath.Join(home, "Library", "Caches", "llama.cpp"), "llama.cpp", 4)
		add(filepath.Join(home, "Library", "Application Support", "Jan", "data", "models"), "Jan", 5)
		add(filepath.Join(home, "Library", "Application Support", "nomic.ai", "GPT4All"), "GPT4All", 2)
	case "windows":
		add(filepath.Join(env("LOCALAPPDATA"), "llama.cpp"), "llama.cpp", 4)
		add(filepath.Join(env("APPDATA"), "Jan", "data", "models"), "Jan", 5)
		add(filepath.Join(env("LOCALAPPDATA"), "nomic.ai", "GPT4All"), "GPT4All", 2)
	default:
		add(filepath.Join(home, ".cache", "llama.cpp"), "llama.cpp", 4)
		add(filepath.Join(home, ".local", "share", "Jan", "data", "models"), "Jan", 5)
		add(filepath.Join(home, ".local", "share", "nomic.ai", "GPT4All"), "GPT4All", 2)
	}
	add(filepath.Join(home, "models"), "Your models folder", 4)
	add(filepath.Join(home, "Models"), "Your models folder", 4)
	add(filepath.Join(home, "Downloads"), "Downloads", 2)
	for _, d := range extra {
		add(d, "A folder you added", 6)
	}
	_ = potluckModels
	return r
}

// ollamaRoots lists where Ollama keeps its models.
func ollamaRoots() []string {
	if d := os.Getenv("OLLAMA_MODELS"); d != "" {
		return []string{d}
	}
	r := []string{filepath.Join(homeDir(), ".ollama", "models")}
	if runtime.GOOS == "linux" {
		r = append(r, "/usr/share/ollama/.ollama/models", "/var/lib/ollama/.ollama/models")
	}
	return r
}

// notAModel recognises GGUF files that are not language models on their own.
var notAModel = regexp.MustCompile(`(?i)(mmproj|imatrix|lora|vocab|draft|^mtp-|-mtp-|embed)`)

var firstPart = regexp.MustCompile(`-00001-of-(\d{5})\.gguf$`)
var otherPart = regexp.MustCompile(`-(\d{5})-of-(\d{5})\.gguf$`)

// minModelSize: smaller GGUF files are test files, vocabularies and the like.
const minModelSize = 40 << 20

// candidate is one model on disk, before its header is read.
type candidate struct {
	name  string
	parts []string
	from  string
}

// walkModels looks for .gguf files under root, no deeper than depth folders.
func walkModels(ctx context.Context, root searchRoot, seen map[string]bool, out *[]candidate) {
	base := strings.Count(filepath.Clean(root.dir), string(os.PathSeparator))
	_ = filepath.WalkDir(root.dir, func(p string, d os.DirEntry, err error) error {
		if ctx.Err() != nil {
			return filepath.SkipAll
		}
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			n := d.Name()
			if p != root.dir && (strings.HasPrefix(n, ".") && n != ".cache" || n == "node_modules" || n == "blobs") {
				return filepath.SkipDir
			}
			if strings.Count(filepath.Clean(p), string(os.PathSeparator))-base >= root.depth {
				return filepath.SkipDir
			}
			return nil
		}
		addCandidate(p, root.from, seen, out)
		return nil
	})
}

// addCandidate takes one file if it is the start of a model.
func addCandidate(p, from string, seen map[string]bool, out *[]candidate) {
	name := filepath.Base(p)
	if !strings.HasSuffix(strings.ToLower(name), ".gguf") || notAModel.MatchString(name) {
		return
	}
	if otherPart.MatchString(name) && !firstPart.MatchString(name) {
		return // found through its first file
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil || seen[fileKey(real)] {
		return
	}
	st, err := os.Stat(real)
	if err != nil || !st.Mode().IsRegular() {
		return
	}
	c := candidate{name: name, parts: []string{p}, from: from}
	if m := firstPart.FindStringSubmatch(name); m != nil {
		n, _ := strconv.Atoi(m[1])
		c.parts = nil
		for i := 1; i <= n; i++ {
			q := filepath.Join(filepath.Dir(p), firstPart.ReplaceAllString(name, fmt.Sprintf("-%05d-of-%s.gguf", i, m[1])))
			if _, err := os.Stat(q); err != nil {
				return // a piece is missing
			}
			c.parts = append(c.parts, q)
		}
	} else if st.Size() < minModelSize {
		return
	}
	seen[fileKey(real)] = true
	*out = append(*out, c)
}

// ollamaModels reads Ollama's lists of models. Ollama stores each model as a file named by
// its checksum, without a .gguf name, and describes it in a small list ("manifest").
func ollamaModels(seen map[string]bool, out *[]candidate) {
	for _, root := range ollamaRoots() {
		_ = filepath.WalkDir(filepath.Join(root, "manifests"), func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil || len(b) > 1<<20 {
				return nil
			}
			var man struct {
				Layers []struct {
					MediaType string `json:"mediaType"`
					Digest    string `json:"digest"`
				} `json:"layers"`
			}
			if json.Unmarshal(b, &man) != nil {
				return nil
			}
			for _, l := range man.Layers {
				if l.MediaType != "application/vnd.ollama.image.model" {
					continue // projectors, templates, licences
				}
				blob := filepath.Join(root, "blobs", strings.ReplaceAll(l.Digest, ":", "-"))
				real, err := filepath.EvalSymlinks(blob)
				if err != nil || seen[fileKey(real)] {
					continue
				}
				if st, err := os.Stat(real); err != nil || st.Size() < minModelSize {
					continue
				}
				// manifests/<registry>/<namespace>/<model>/<tag>
				tag, model := filepath.Base(p), filepath.Base(filepath.Dir(p))
				ns := filepath.Base(filepath.Dir(filepath.Dir(p)))
				name := model + "-" + tag
				if ns != "library" {
					name = ns + "-" + name
				}
				seen[fileKey(real)] = true
				*out = append(*out, candidate{name: "ollama-" + safeName(name) + ".gguf", parts: []string{blob}, from: "Ollama"})
			}
			return nil
		})
	}
}

// fileKey identifies a file for spotting the same one twice. macOS and Windows disks
// usually ignore upper and lower case, so ~/models and ~/Models are one folder.
func fileKey(real string) string {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return strings.ToLower(real)
	}
	return real
}

var unsafeChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// safeName makes a file or folder name from any text. It is never empty: a name with no
// letters it can keep (written in Japanese, or just "!!") becomes "n-" and a short hash of
// it, so it can never stand for its parent folder (a skill called "..." once meant the whole
// skills folder) and two such names stay apart.
func safeName(s string) string {
	if n := strings.Trim(unsafeChars.ReplaceAllString(s, "_"), "_."); n != "" {
		return n
	}
	sum := sha256.Sum256([]byte(s))
	return "n-" + hex.EncodeToString(sum[:4])
}

// spotlightModels asks macOS's own file index for every .gguf file on the computer. It
// answers in a fraction of a second and does not read the disk.
func spotlightModels(ctx context.Context) []string {
	if runtime.GOOS != "darwin" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, "mdfind", `kMDItemFSName == "*.gguf"c`).Output()
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// deepRoots: where a whole-computer search walks when there is no file index to ask.
func deepRoots() []string {
	switch runtime.GOOS {
	case "windows":
		var r []string
		for c := 'C'; c <= 'Z'; c++ {
			d := string(c) + `:\`
			if _, err := os.Stat(d); err == nil {
				r = append(r, d)
			}
		}
		return r
	case "darwin":
		r := []string{homeDir()}
		if ents, err := os.ReadDir("/Volumes"); err == nil {
			for _, e := range ents {
				r = append(r, filepath.Join("/Volumes", e.Name()))
			}
		}
		return r
	default:
		return []string{homeDir(), "/mnt", "/media", "/opt", "/srv", "/data"}
	}
}

// skipDeep: folders a whole-computer search never needs to look in.
var skipDeep = map[string]bool{"Windows": true, "Program Files (x86)": true, "$Recycle.Bin": true, "System Volume Information": true,
	"Library": true, "node_modules": true, ".git": true, ".Trash": true, "proc": true, "sys": true, "dev": true, "Applications": true}

// discoverModels finds models on this computer. deep also searches the whole computer.
func (a *Agent) discoverModels(ctx context.Context, deep bool) []FoundModel {
	cfg := a.cfg.Get()
	own := filepath.Join(a.dir, "models")
	seen := map[string]bool{}
	// Files already in Potluck's own folder are listed there; skip them here.
	if ents, err := os.ReadDir(own); err == nil {
		for _, e := range ents {
			if real, err := filepath.EvalSymlinks(filepath.Join(own, e.Name())); err == nil {
				seen[fileKey(real)] = true
			}
		}
	}
	var cands []candidate
	ollamaModels(seen, &cands)
	for _, r := range modelRoots(cfg.ModelDirs, own) {
		walkModels(ctx, r, seen, &cands)
	}
	// What a whole-computer search found before is remembered, so a model kept in an
	// unusual folder stays listed without searching again.
	remembered := filepath.Join(a.dir, "found-elsewhere.json")
	var before []string
	if b, err := os.ReadFile(remembered); err == nil {
		_ = json.Unmarshal(b, &before)
	}
	if deep {
		start := len(cands)
		if hits := spotlightModels(ctx); hits != nil {
			for _, p := range hits {
				if p != "" && !strings.HasPrefix(p, a.dir) {
					addCandidate(p, "Found on this computer", seen, &cands)
				}
			}
		} else {
			ctx2, cancel := context.WithTimeout(ctx, 90*time.Second)
			for _, d := range deepRoots() {
				walkDeep(ctx2, d, seen, &cands)
			}
			cancel()
		}
		keep := map[string]bool{}
		for _, p := range before {
			if _, err := os.Stat(p); err == nil {
				keep[p] = true
			}
		}
		for _, c := range cands[start:] {
			keep[c.parts[0]] = true
		}
		list := make([]string, 0, len(keep))
		for p := range keep {
			list = append(list, p)
		}
		sort.Strings(list)
		if b, err := json.Marshal(list); err == nil {
			_ = os.WriteFile(remembered, b, 0o644)
		}
	}
	for _, p := range before {
		if _, err := os.Stat(p); err == nil {
			addCandidate(p, "Found on this computer", seen, &cands)
		}
	}
	inUse := map[string]bool{}
	for _, p := range cfg.Found {
		inUse[p] = true
	}
	var out []FoundModel
	for _, c := range cands {
		f := FoundModel{Name: c.name, Path: c.parts[0], Parts: c.parts, From: c.from, InUse: inUse[c.parts[0]]}
		info, err := headerOfFiles(c.parts)
		if err != nil {
			f.Error = err.Error()
			for _, p := range c.parts {
				if st, err := os.Stat(p); err == nil {
					f.Size += st.Size()
				}
			}
		} else {
			f.Size, f.Arch, f.Ctx, f.Params = info.FileSize, info.Arch, info.CtxTrain, info.Params
			f.Title = info.Name
			f.Quant = quantName(c.name, info.FileType)
		}
		if f.Title == "" {
			f.Title = strings.TrimSuffix(firstPart.ReplaceAllString(c.name, ".gguf"), ".gguf")
		}
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Size > out[j].Size })
	return out
}

// walkDeep searches a whole drive, skipping system folders and hidden ones.
func walkDeep(ctx context.Context, root string, seen map[string]bool, out *[]candidate) {
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if ctx.Err() != nil {
			return filepath.SkipAll
		}
		if err != nil {
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if p != root && (skipDeep[d.Name()] || strings.HasPrefix(d.Name(), ".") && d.Name() != ".cache" && d.Name() != ".lmstudio") {
				return filepath.SkipDir
			}
			return nil
		}
		addCandidate(p, "Found on this computer", seen, out)
		return nil
	})
}

// headerOfFiles reads the blueprint of a model, following all its files.
func headerOfFiles(paths []string) (*ModelInfo, error) {
	var infos []*ModelInfo
	for i, path := range paths {
		_, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("%s is missing", filepath.Base(path))
		}
		hint := 0
		if i > 0 {
			hint = infos[0].Layers
		}
		info, err := readHeaderFile(path, hint)
		if err != nil {
			return nil, fmt.Errorf("%s does not look like a model file: %w", filepath.Base(path), err)
		}
		infos = append(infos, info)
	}
	return mergeParts(infos), nil
}

var quantInName = regexp.MustCompile(`(?i)(?:^|[-_.])((?:UD-)?(?:IQ\d_[A-Z]+|IQ\d_[A-Z]_[A-Z]|Q\d_K_XL|Q\d_K_[SML]|Q\d_K|Q\d_\d|Q\d_[01]|BF16|F16|F32|MXFP4|TQ\d_\d))(?:[-_.]|$)`)

// fileTypes names llama.cpp's general.file_type values.
var fileTypes = map[int]string{0: "F32", 1: "F16", 2: "Q4_0", 3: "Q4_1", 7: "Q8_0", 8: "Q5_0", 9: "Q5_1", 10: "Q2_K", 11: "Q3_K_S", 12: "Q3_K_M", 13: "Q3_K_L",
	14: "Q4_K_S", 15: "Q4_K_M", 16: "Q5_K_S", 17: "Q5_K_M", 18: "Q6_K", 19: "IQ2_XXS", 20: "IQ2_XS", 21: "Q2_K_S", 22: "IQ3_XS", 23: "IQ3_XXS",
	24: "IQ1_S", 25: "IQ4_NL", 26: "IQ3_S", 27: "IQ3_M", 28: "IQ2_S", 29: "IQ2_M", 30: "IQ4_XS", 31: "IQ1_M", 32: "BF16", 36: "TQ1_0", 37: "TQ2_0", 38: "MXFP4"}

// quantName says how a model is compressed: from its name when it says so, else its header.
func quantName(name string, fileType int) string {
	if m := quantInName.FindStringSubmatch(name); m != nil {
		return strings.ToUpper(m[1])
	}
	if s, ok := fileTypes[fileType]; ok {
		return s
	}
	return ""
}

// ---------- engines ----------

var buildRe = regexp.MustCompile(`build (\d+)`)
var buildInPath = regexp.MustCompile(`[/\\-]b(\d{4,6})(?:[/\\-]|$)`)

// engineBuild runs a llama-server program to ask its build number. Builds made from a
// source download without git history say "build 1"; their folder name often tells.
func engineBuild(path string) int {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Dir = filepath.Dir(path)
	hideWindow(cmd)
	b, _ := cmd.CombinedOutput()
	n := 0
	if m := buildRe.FindSubmatch(b); m != nil {
		n, _ = strconv.Atoi(string(m[1]))
	}
	if n <= 1 {
		if m := buildInPath.FindStringSubmatch(path); m != nil {
			n, _ = strconv.Atoi(m[1])
		} else {
			n = 0
		}
	}
	return n
}

// discoverEngines finds llama.cpp server programs on this computer.
func (a *Agent) discoverEngines(ctx context.Context) []FoundEngine {
	exe := "llama-server"
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	var paths []string
	if p, err := exec.LookPath("llama-server"); err == nil {
		paths = append(paths, p)
	}
	home := homeDir()
	paths = append(paths, "/opt/homebrew/bin/"+exe, "/usr/local/bin/"+exe, "/usr/bin/"+exe,
		filepath.Join(home, "llama.cpp", "build", "bin", exe), filepath.Join(home, "llama.cpp", exe))
	if p := a.cfg.Get().EnginePath; p != "" && p != "potluck" {
		paths = append(paths, p)
	}
	if runtime.GOOS == "darwin" {
		ctx2, cancel := context.WithTimeout(ctx, 20*time.Second)
		if b, err := exec.CommandContext(ctx2, "mdfind", `kMDItemFSName == "llama-server"`).Output(); err == nil {
			paths = append(paths, strings.Split(strings.TrimSpace(string(b)), "\n")...)
		}
		cancel()
	}
	seen := map[string]bool{}
	var out []FoundEngine
	for _, p := range paths {
		if p == "" || strings.HasPrefix(p, a.dir) {
			continue
		}
		real, err := filepath.EvalSymlinks(p)
		if err != nil || seen[fileKey(real)] {
			continue
		}
		st, err := os.Stat(real)
		if err != nil || st.IsDir() || st.Mode()&0o111 == 0 && runtime.GOOS != "windows" {
			continue
		}
		seen[fileKey(real)] = true
		e := FoundEngine{Path: real, Build: engineBuild(real)}
		switch pinned := tagBuild(engineTag); {
		case e.Build == 0:
			e.Note = "Could not tell which version this is."
		case e.Build < pinned:
			e.Note = fmt.Sprintf("Version %d is older than Potluck's own (%d); newer models such as Inkling 2 need %d or later.", e.Build, pinned, pinned)
		default:
			e.Note = fmt.Sprintf("Version %d. Used when a model runs on this computer alone.", e.Build)
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Build > out[j].Build })
	return out
}

// tagBuild turns a release name such as b12608 into its number.
func tagBuild(tag string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(tag, "b"))
	return n
}

// localEngine picks the engine found on this computer that a model running here alone
// should use, or "" for Potluck's own. Only builds at least as new as Potluck's own.
func (a *Agent) localEngine() string {
	p := a.cfg.Get().EnginePath
	if p == "potluck" {
		return ""
	}
	a.mu.Lock()
	cached, at := a.engineChoice, a.engineChoiceAt
	a.mu.Unlock()
	if time.Since(at) < 10*time.Minute && p == "" {
		return cached
	}
	best := ""
	if p != "" {
		if b := engineBuild(p); b >= tagBuild(engineTag) {
			best = p
		}
	} else {
		for _, e := range a.discoverEngines(context.Background()) {
			if e.Build >= tagBuild(engineTag) {
				best = e.Path
				break
			}
		}
	}
	a.mu.Lock()
	a.engineChoice, a.engineChoiceAt = best, time.Now()
	a.mu.Unlock()
	return best
}

// modelPath is where a model file Potluck knows by name lives on this computer: in its own
// models folder, or where another app keeps it.
func (a *Agent) modelPath(name string) string {
	name = filepath.Base(name)
	if p, ok := a.cfg.Get().Found[name]; ok {
		return p
	}
	return filepath.Join(a.dir, "models", name)
}

// isFound says whether a model name refers to a file Potluck did not download itself.
func (a *Agent) isFound(name string) bool {
	_, ok := a.cfg.Get().Found[filepath.Base(name)]
	return ok
}

// useFound adds a found model to Potluck's list, under a name that does not clash.
func (a *Agent) useFound(path string) (string, error) {
	var parts []string
	var name, from string
	for _, f := range a.discoverModelsCached() {
		if f.Path == path {
			parts, name, from = f.Parts, f.Name, f.From
		}
	}
	if parts == nil {
		// Not in the last search: accept any readable model file the user points to.
		seen := map[string]bool{}
		var c []candidate
		addCandidate(path, "A file you chose", seen, &c)
		if len(c) == 0 {
			return "", errors.New("that is not a model file Potluck can use")
		}
		parts, name, from = c[0].parts, c[0].name, c[0].from
	}
	if _, err := headerOfFiles(parts); err != nil {
		return "", err
	}
	_ = from
	own := filepath.Join(a.dir, "models")
	a.cfg.Update(func(c *Config) {
		if c.Found == nil {
			c.Found = map[string]string{}
		}
		for n, p := range c.Found {
			if p == parts[0] {
				name = n // already known
			}
		}
		base := name
		for i := 2; ; i++ {
			_, clash := os.Stat(filepath.Join(own, name))
			if p, ok := c.Found[name]; (!ok || p == parts[0]) && clash != nil {
				break
			}
			name = strings.TrimSuffix(base, ".gguf") + "-" + strconv.Itoa(i) + ".gguf"
		}
		if m := firstPart.FindStringSubmatch(name); m != nil && len(parts) > 1 {
			for i, p := range parts {
				c.Found[firstPart.ReplaceAllString(name, fmt.Sprintf("-%05d-of-%s.gguf", i+1, m[1]))] = p
			}
		} else {
			c.Found[name] = parts[0]
		}
	})
	return name, nil
}

// forgetFound removes a found model from Potluck's list. The file itself stays.
func (a *Agent) forgetFound(name string) {
	name = filepath.Base(name)
	a.cfg.Update(func(c *Config) {
		key := otherPart.ReplaceAllString(name, "")
		for n := range c.Found {
			if n == name || otherPart.MatchString(name) && otherPart.ReplaceAllString(n, "") == key {
				delete(c.Found, n)
			}
		}
	})
}

func (a *Agent) discoverModelsCached() []FoundModel {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastFound
}
