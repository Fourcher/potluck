package main

// Streaming from disk: InferSSD as a second engine.
//
// InferSSD (github.com/wici-ai/inferssd, Apache-2.0) is built on llama.cpp. For a
// mixture-of-experts model it keeps the part of the model used for every word in memory and
// reads the experts each word needs from the SSD, caching the popular ones in RAM and, with a
// CUDA card, in video memory. That lets one computer run a model several times bigger than
// its memory. It runs on one computer: it has no network pooling, so in Potluck it is a
// separate way to run a model, next to pooling and next to plain llama.cpp with its disk.
//
// What it runs on (release 0.1.5): Windows x64 (a CUDA package for NVIDIA cards from the
// Turing generation on, a Vulkan package with a processor fallback for everything else) and
// Apple Silicon Macs on macOS 15 or later. Linux needs a source build with systemd; Intel
// Macs are not supported. Only the model families it has profiles for can be streamed.

import (
	"compress/gzip"
	"context"
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

// streamTag is the InferSSD release Potluck installs. Pinned: a new release is adopted
// deliberately, after it has been tried.
const streamTag = "v0.1.5"

// streamArchs are the model families (GGUF general.architecture) InferSSD has profiles for.
var streamArchs = map[string]string{
	"dsv":       "DeepSeek-V4.1",
	"muse":      "Muse Spark",
	"qwen4exp":  "Qwen3.8-Flash-Next",
	"qwen35moe": "Qwen3.5 and 3.6 mixture-of-experts",
}

// streamNamePattern guesses from a file name whether a model is one of those families, for
// the model store, which has not read the model's header yet.
var streamNamePattern = regexp.MustCompile(`(?i)deepseek-v4|muse-spark|qwen3\.8|qwen3\.[56]-\d+b-a\d+b`)

// streamVariant says which InferSSD package suits a computer, or why none does.
func streamVariant(info NodeInfo) (variant, why string) {
	switch {
	case info.OS == "windows" && info.Arch == "amd64":
		for _, g := range info.GPUs {
			if g.Vendor == "nvidia" && g.ComputeCap >= 7.5 {
				return "windows-cuda", ""
			}
		}
		return "windows-vulkan", ""
	case info.OS == "darwin" && info.Arch == "arm64":
		if v := strings.TrimPrefix(info.OSName, "macOS "); v != "" {
			if major, err := strconv.Atoi(strings.SplitN(v, ".", 2)[0]); err == nil && major < 15 {
				return "", "needs macOS 15 or later"
			}
		}
		return "macos", ""
	case info.OS == "darwin":
		return "", "not available for Intel Macs"
	case info.OS == "linux":
		return "", "not available on Linux yet (it has to be built from source there)"
	}
	return "", "not available for this kind of computer"
}

// StreamPackage is one InferSSD release package, as its release lists it.
type StreamPackage struct {
	Version string `json:"version"`
	File    string `json:"file"`
	URL     string `json:"url"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	Variant string `json:"variant"`
	// MinDriver is the oldest NVIDIA driver the CUDA package runs on (CUDA package only).
	MinDriver string `json:"minimum_nvidia_driver_native,omitempty"`
}

// streamPackage looks up a package in the pinned release's list (kept on disk after the
// first look).
func (h *Host) streamPackage(variant string) (*StreamPackage, error) {
	list := map[string]string{"windows-cuda": "latest-windows-cuda.json", "windows-vulkan": "latest-windows-vulkan.json", "macos": "latest-macos.json"}[variant]
	if list == "" {
		return nil, errors.New("no InferSSD package for this computer")
	}
	cache := filepath.Join(h.a.dir, "inferssd-"+streamTag+"-"+variant+".json")
	var p StreamPackage
	if b, err := os.ReadFile(cache); err == nil && json.Unmarshal(b, &p) == nil && p.URL != "" {
		p.Variant = variant
		return &p, nil
	}
	if err := httpGetJSON("https://github.com/wici-ai/inferssd/releases/download/"+streamTag+"/"+list, &p); err != nil {
		return nil, fmt.Errorf("could not look up the InferSSD release: %w", err)
	}
	if p.URL == "" || len(p.SHA256) != 64 || !strings.HasPrefix(p.URL, "https://github.com/wici-ai/inferssd/releases/download/") {
		return nil, errors.New("the InferSSD release list is not what Potluck expects")
	}
	p.Variant = variant
	if b, err := json.Marshal(p); err == nil {
		_ = os.WriteFile(cache, b, 0o644)
	}
	return &p, nil
}

// ---------- planning ----------

// planStream lays a model out for streaming from one computer's disk.
func planStream(m *ModelInfo, n PlanNode, fileOnDisk bool) *Plan {
	p := &Plan{ModelBytes: m.TotalBytes(), Layers: m.Layers, Head: n.ID, Solo: true, Stream: true}
	for _, b := range m.LayerBytes {
		if b > p.LayerBytes {
			p.LayerBytes = b
		}
	}
	family, ok := streamArchs[m.Arch]
	switch {
	case !ok:
		p.Reason = "Streaming from disk works for DeepSeek-V4.1, Muse Spark and the Qwen3.5 to 3.8 mixture-of-experts models; this model is not one of them."
		return p
	case n.StreamVariant == "":
		p.Reason = n.Name + ": streaming from disk is " + nonEmpty(n.StreamWhy, "not available here") + "."
		return p
	case !fileOnDisk && n.DiskFree < m.FileSize+(4<<30):
		p.Reason = fmt.Sprintf("%s has %s of free disk space; this model needs %s plus a little room.", n.Name, humanBytes(n.DiskFree), humanBytes(m.FileSize))
		return p
	}
	_ = family
	inMemory := n.CPUBytes
	if n.Unified {
		inMemory = n.GPUBytes
	}
	if p.ModelBytes > inMemory {
		p.Overflow = p.ModelBytes - inMemory
	}
	p.OfferedSum, p.BiggestOne = inMemory+n.GPUBytes, inMemory+n.GPUBytes
	p.Shares = []PlanShare{{ID: n.ID, Name: n.Name, Role: "head", CPULayers: m.Layers, Bytes: p.ModelBytes, Offered: inMemory + n.GPUBytes}}
	p.Machines, p.Fits = 1, true
	return p
}

// bestStream picks the computer to stream a model from: the one with a CUDA card first (it
// caches experts in video memory too), then the one with the most memory, among those with
// room for the file. only, when set, limits the choice to that computer.
func (h *Host) bestStream(info *ModelInfo, only string) *Plan {
	if _, ok := streamArchs[info.Arch]; !ok {
		return nil
	}
	nodes := h.planNodes()
	sort.SliceStable(nodes, func(i, j int) bool {
		ci, cj := nodes[i].StreamVariant == "windows-cuda", nodes[j].StreamVariant == "windows-cuda"
		if ci != cj {
			return ci
		}
		return nodes[i].CPUBytes+nodes[i].GPUBytes > nodes[j].CPUBytes+nodes[j].GPUBytes
	})
	var first *Plan
	for _, n := range nodes {
		if only != "" && n.ID != only {
			continue
		}
		p := planStream(info, n, only != "")
		if p.Fits {
			return p
		}
		if first == nil {
			first = p
		}
	}
	return first
}

// streamChoice: a Start request's "solo" value is "stream:<computer>" for this mode.
func streamChoice(solo string) (id string, ok bool) {
	return strings.CutPrefix(solo, "stream:")
}

// ---------- the computer that runs it ----------

// ensureStream installs an InferSSD package and returns its folder.
func (a *Agent) ensureStream(ctx context.Context, p StreamPackage, note func(string)) (string, error) {
	if p.Version == "" || p.Variant == "" || strings.ContainsAny(p.Version+p.Variant, `/\`+"\x00") || strings.Contains(p.Version, "..") {
		return "", errors.New("bad InferSSD package")
	}
	dir := filepath.Join(a.dir, "runtime", "inferssd-"+p.Version+"-"+p.Variant)
	if _, err := os.Stat(filepath.Join(dir, ".ok")); err == nil {
		return dir, nil
	}
	_ = os.RemoveAll(dir)
	tmp := filepath.Join(a.dir, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	file := filepath.Join(tmp, filepath.Base(p.File))
	_ = os.Remove(file + ".ok")
	note("Getting the disk-streaming engine")
	err := downloadFile(ctx, p.URL, file, p.Size, p.SHA256, 8, func(d, t int64, _ string) {
		note(fmt.Sprintf("Getting the disk-streaming engine: %d%%", d*100/max64(t, 1)))
	})
	if err != nil {
		return "", fmt.Errorf("could not download InferSSD: %w", err)
	}
	if strings.HasSuffix(p.File, ".zip") {
		err = extractZip(file, dir)
	} else {
		var f *os.File
		if f, err = os.Open(file); err == nil {
			var gz *gzip.Reader
			if gz, err = gzip.NewReader(f); err == nil {
				err = extractTar(gz, dir, nil)
			}
			f.Close()
		}
	}
	_ = os.Remove(file)
	_ = os.Remove(file + ".ok")
	if err != nil {
		return "", fmt.Errorf("could not unpack InferSSD: %w", err)
	}
	if runtime.GOOS == "darwin" {
		_ = exec.Command("xattr", "-dr", "com.apple.quarantine", dir).Run()
	}
	if streamBin(dir) == "" {
		return "", errors.New("the InferSSD download is missing its program")
	}
	_ = os.RemoveAll(tmp)
	// Older InferSSD packages are not needed any more.
	if old, _ := filepath.Glob(filepath.Join(a.dir, "runtime", "inferssd-*")); len(old) > 0 {
		for _, o := range old {
			if o != dir {
				_ = os.RemoveAll(o)
			}
		}
	}
	return dir, os.WriteFile(filepath.Join(dir, ".ok"), []byte(time.Now().Format(time.RFC3339)+"\n"), 0o644)
}

// streamBin finds InferSSD's launcher in an unpacked package.
func streamBin(dir string) string {
	name := "inferssd"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	var found string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == name && filepath.Base(filepath.Dir(p)) == "bin" && found == "" {
			found = p
		}
		return nil
	})
	return found
}

// streamHome is where InferSSD keeps its state (machine measurements, run records, prompt
// caches). It is pointed inside Potluck's data folder so nothing lands in the user's home
// folder, and so removing Potluck's data removes it too.
func (a *Agent) streamHome() string { return filepath.Join(a.dir, "inferssd-home") }

// streamEnv points every place a program may keep its state at streamHome.
func (a *Agent) streamEnv() []string {
	home := a.streamHome()
	_ = os.MkdirAll(home, 0o755)
	env := []string{"HOME=" + home, "XDG_DATA_HOME=" + filepath.Join(home, "data"), "XDG_CACHE_HOME=" + filepath.Join(home, "cache"),
		"XDG_CONFIG_HOME=" + filepath.Join(home, "config"), "XDG_STATE_HOME=" + filepath.Join(home, "state"), "HF_HOME=" + filepath.Join(home, "hf")}
	if runtime.GOOS == "windows" {
		env = append(env, "USERPROFILE="+home, "LOCALAPPDATA="+filepath.Join(home, "AppData", "Local"), "APPDATA="+filepath.Join(home, "AppData", "Roaming"))
	}
	return env
}

// pruneStreamRuns keeps InferSSD's run records from piling up: the newest few are kept.
func (a *Agent) pruneStreamRuns() {
	var runs []string
	_ = filepath.WalkDir(a.streamHome(), func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() && d.Name() == "runs" {
			if entries, e := os.ReadDir(p); e == nil {
				for _, en := range entries {
					runs = append(runs, filepath.Join(p, en.Name()))
				}
			}
			return filepath.SkipDir
		}
		return nil
	})
	sort.Slice(runs, func(i, j int) bool {
		a, _ := os.Stat(runs[i])
		b, _ := os.Stat(runs[j])
		return a != nil && b != nil && a.ModTime().After(b.ModTime())
	})
	for i, r := range runs {
		if i >= 3 {
			_ = os.RemoveAll(r)
		}
	}
}

// streamArgs is the launcher's command line for one model.
func streamArgs(model string, port, ctx int, hostBudgetMiB int64, noGPU bool) []string {
	args := []string{"serve", "-m", model, "--host", "127.0.0.1", "--port", strconv.Itoa(port)}
	if ctx > 0 {
		args = append(args, "--context", strconv.Itoa(ctx))
	}
	if hostBudgetMiB > 0 {
		args = append(args, "--set", "host.budget_mib="+strconv.FormatInt(hostBudgetMiB, 10))
	}
	if noGPU {
		args = append(args, "--set", "vram.budget=0")
	}
	return args
}
