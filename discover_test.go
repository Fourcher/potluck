package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// writeGGUF makes a small but valid model file, padded (sparsely) to size bytes.
func writeGGUF(t *testing.T, path string, size int64, fileType uint32) {
	t.Helper()
	var b bytes.Buffer
	w := func(v any) { _ = binary.Write(&b, binary.LittleEndian, v) }
	str := func(s string) { w(uint64(len(s))); b.WriteString(s) }
	b.WriteString("GGUF")
	w(uint32(3))
	w(uint64(1)) // tensors
	w(uint64(5)) // keys
	str("general.architecture")
	w(uint32(8))
	str("llama")
	str("general.name")
	w(uint32(8))
	str("Test Model")
	str("llama.block_count")
	w(uint32(4))
	w(uint32(1))
	str("llama.context_length")
	w(uint32(4))
	w(uint32(8192))
	str("general.file_type")
	w(uint32(4))
	w(fileType)
	str("blk.0.attn_q.weight")
	w(uint32(1))
	w(uint64((size - 4096) / 4))
	w(uint32(0)) // F32
	w(uint64(0))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, size); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverModels(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HF_HOME", "")
	t.Setenv("HF_HUB_CACHE", "")
	t.Setenv("LLAMA_CACHE", "")
	t.Setenv("OLLAMA_MODELS", "")
	const mb = 1 << 20
	// LM Studio, with a projector file next to it that is not a model.
	writeGGUF(t, filepath.Join(home, ".lmstudio", "models", "org", "Repo-GGUF", "Model-Q4_K_M.gguf"), 50*mb, 15)
	writeGGUF(t, filepath.Join(home, ".lmstudio", "models", "org", "Repo-GGUF", "mmproj-Model-F16.gguf"), 50*mb, 1)
	// A model in two files.
	writeGGUF(t, filepath.Join(home, "models", "Big-IQ2_M-00001-of-00002.gguf"), 60*mb, 29)
	writeGGUF(t, filepath.Join(home, "models", "Big-IQ2_M-00002-of-00002.gguf"), 60*mb, 29)
	// Too small to be a model.
	writeGGUF(t, filepath.Join(home, "models", "tiny.gguf"), 5*mb, 1)
	// Ollama: a manifest pointing at a blob with no .gguf name.
	om := filepath.Join(home, ".ollama", "models")
	writeGGUF(t, filepath.Join(om, "blobs", "sha256-abc123"), 45*mb, 7)
	man := `{"layers":[{"mediaType":"application/vnd.ollama.image.model","digest":"sha256:abc123"},{"mediaType":"application/vnd.ollama.image.template","digest":"sha256:def"}]}`
	if err := os.MkdirAll(filepath.Join(om, "manifests", "registry.ollama.ai", "library", "qwen3"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(om, "manifests", "registry.ollama.ai", "library", "qwen3", "8b"), []byte(man), 0o644); err != nil {
		t.Fatal(err)
	}
	// Hugging Face cache: snapshot links into blobs.
	hub := filepath.Join(home, ".cache", "huggingface", "hub", "models--org--HF-GGUF")
	writeGGUF(t, filepath.Join(hub, "blobs", "0123abcd"), 42*mb, 18)
	if runtime.GOOS != "windows" {
		if err := os.MkdirAll(filepath.Join(hub, "snapshots", "rev"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join("..", "..", "blobs", "0123abcd"), filepath.Join(hub, "snapshots", "rev", "HF-Q6_K.gguf")); err != nil {
			t.Fatal(err)
		}
	}

	data := t.TempDir()
	a := &Agent{dir: data, cfg: loadConfig(data)}
	found := a.discoverModels(context.Background(), false)
	byName := map[string]FoundModel{}
	for _, f := range found {
		byName[f.Name] = f
		if f.Error != "" {
			t.Errorf("%s: %s", f.Name, f.Error)
		}
	}
	want := map[string]string{"Model-Q4_K_M.gguf": "Q4_K_M", "Big-IQ2_M-00001-of-00002.gguf": "IQ2_M", "ollama-qwen3-8b.gguf": "Q8_0"}
	if runtime.GOOS != "windows" {
		want["HF-Q6_K.gguf"] = "Q6_K"
	}
	for n, q := range want {
		f, ok := byName[n]
		if !ok {
			t.Errorf("did not find %s (found %v)", n, keys(byName))
			continue
		}
		if f.Quant != q || f.Arch != "llama" || f.Ctx != 8192 {
			t.Errorf("%s: quant %q arch %q ctx %d", n, f.Quant, f.Arch, f.Ctx)
		}
	}
	if f := byName["Big-IQ2_M-00001-of-00002.gguf"]; len(f.Parts) != 2 || f.Size != 120*mb {
		t.Errorf("split model: %d parts, %d bytes", len(f.Parts), f.Size)
	}
	if len(found) != len(want) {
		for _, f := range found {
			t.Logf("%s %s", f.Name, f.Path)
		}
		t.Errorf("found %d models, want %d", len(found), len(want))
	}

	// Using one: it is read where it is, and forgetting it leaves the file alone.
	a.lastFound = found
	name, err := a.useFound(byName["ollama-qwen3-8b.gguf"].Path)
	if err != nil {
		t.Fatal(err)
	}
	if p := a.modelPath(name); p != byName["ollama-qwen3-8b.gguf"].Path {
		t.Errorf("model path %s", p)
	}
	if _, err := a.localHeader(name); err != nil {
		t.Errorf("header of a found model: %v", err)
	}
	split, err := a.useFound(byName["Big-IQ2_M-00001-of-00002.gguf"].Path)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := a.localHeader(split); err != nil || info.FileSize != 120*mb {
		t.Errorf("header of a found split model: %v", err)
	}
	st := a.Storage()
	if len(st.Models) != 2 || st.Models[0].Found == "" {
		t.Errorf("storage lists %+v", st.Models)
	}
	if _, err := a.Handle("delete_model", []byte(`{"file":"`+name+`"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(byName["ollama-qwen3-8b.gguf"].Path); err != nil {
		t.Errorf("forgetting a found model deleted the file: %v", err)
	}
	if a.isFound(name) {
		t.Errorf("still listed after forgetting")
	}
	// A second search marks what is in use and does not list Potluck's own files.
	if again := a.discoverModels(context.Background(), false); len(again) != len(want) {
		t.Errorf("second search found %d", len(again))
	}
}

func keys(m map[string]FoundModel) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestQuantName(t *testing.T) {
	for name, want := range map[string]string{
		"Inkling-2-Mini-Uncensored-AD-IQ2_M.gguf": "IQ2_M", "qwen3-8b-q4_k_m.gguf": "Q4_K_M", "Model.Q8_0.gguf": "Q8_0",
		"gemma-UD-Q4_K_XL.gguf": "UD-Q4_K_XL", "x-BF16.gguf": "BF16", "Big-UD-IQ2_XXS-00001-of-00003.gguf": "UD-IQ2_XXS",
	} {
		if got := quantName(name, 15); got != want && !(want == "" && got == "Q4_K_M") {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
}

func TestEngineBuild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script")
	}
	dir := filepath.Join(t.TempDir(), "llama.cpp-b12655", "build", "bin")
	_ = os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, "llama-server")
	_ = os.WriteFile(p, []byte("#!/bin/sh\necho 'version: 1.0.0-dev (build 1, commit 969d733)'\n"), 0o755)
	if b := engineBuild(p); b != 12655 {
		t.Errorf("source build in a b12655 folder: %d", b)
	}
	_ = os.WriteFile(p, []byte("#!/bin/sh\necho 'version: 1.0.0 (build 12359, commit b1b770d90)'\n"), 0o755)
	if b := engineBuild(p); b != 12359 {
		t.Errorf("release build: %d", b)
	}
}

func TestRunCommand(t *testing.T) {
	dir := t.TempDir()
	cmd, slow := "echo hello; echo oops >&2; exit 3", "sleep 30"
	if runtime.GOOS == "windows" {
		cmd, slow = "Write-Output hello; [Console]::Error.WriteLine('oops'); exit 3", "Start-Sleep 30"
	}
	res, err := runCommand(context.Background(), RunReq{Command: cmd}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if res.Exit != 3 || !strings.Contains(res.Output, "hello") || !strings.Contains(res.Output, "oops") || res.Dir != dir {
		t.Errorf("got %+v", res)
	}
	start := time.Now()
	res, err = runCommand(context.Background(), RunReq{Command: slow, Timeout: 1}, dir)
	if err != nil || !res.TimedOut || time.Since(start) > 10*time.Second {
		t.Errorf("timeout: %+v %v after %v", res, err, time.Since(start))
	}
	if _, err := runCommand(context.Background(), RunReq{Command: "echo", Dir: "no-such-folder"}, dir); err == nil {
		t.Errorf("ran in a folder that does not exist")
	}
	var b limitedBuffer
	for i := 0; i < 5000; i++ {
		_, _ = b.Write([]byte("0123456789\n"))
	}
	if s := b.String(); len(s) > outHead+outTail+200 || !strings.Contains(s, "left out") {
		t.Errorf("long output kept %d bytes", len(s))
	}
}

func TestRunGuards(t *testing.T) {
	ok := map[string]string{"X-Potluck": "1", "Origin": "http://127.0.0.1:7720"}
	cases := []struct {
		remote, host string
		hdr          map[string]string
		want         bool
	}{
		{"127.0.0.1:5000", "127.0.0.1:7720", ok, true},
		{"192.168.1.9:5000", "192.168.1.2:7720", ok, false},                                               // from the network
		{"127.0.0.1:5000", "127.0.0.1:7720", map[string]string{"Origin": "http://127.0.0.1:7720"}, false}, // no header
		{"127.0.0.1:5000", "127.0.0.1:7720", map[string]string{"X-Potluck": "1", "Origin": "https://evil.example"}, false},
		{"127.0.0.1:5000", "evil.example:7720", ok, false}, // a name pointed at this computer
		{"127.0.0.1:5000", "127.0.0.1:7720", map[string]string{"X-Potluck": "1", "Sec-Fetch-Site": "cross-site"}, false},
	}
	for i, c := range cases {
		r := chatReq(http.MethodPost, "/potluck/run", c.remote, c.host, c.hdr, `{"command":"echo hi"}`)
		got := chatOwner(r) && r.Header.Get("X-Potluck") == "1" && sameOrigin(r)
		if got != c.want {
			t.Errorf("case %d: allowed=%v, want %v", i, got, c.want)
		}
	}
}

func TestHereFit(t *testing.T) {
	info := NodeInfo{RAMTotal: 16 << 30, RAMAvail: 12 << 30, GPUs: []GPU{{Unified: true, Usable: true, Backend: "metal", VRAMTotal: 16 << 30}}}
	if v, _ := hereFit(info, 0, 8670<<20); v != "fits" {
		t.Errorf("8.7 GB model with 12 GB free: %s", v)
	}
	info.RAMAvail = 6 << 30
	if v, _ := hereFit(info, 0, 8670<<20); v != "tight" {
		t.Errorf("8.7 GB model with 6 GB free: %s", v)
	}
	if v, _ := hereFit(info, 0, 20<<30); v != "no" {
		t.Errorf("20 GB model on 16 GB: %s", v)
	}
}
