package main

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ulikunitz/xz"
)

// Asset is one file of a llama.cpp release.
type Asset struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// RuntimeSpec tells a computer which llama.cpp build to install. The main computer works it
// out (one GitHub lookup for everyone) so every machine runs exactly the same build: the
// worker protocol is version-checked.
type RuntimeSpec struct {
	Tag     string  `json:"tag"`
	Variant string  `json:"variant"`
	Assets  []Asset `json:"assets"`
	// Solo: the model runs on this computer alone, so an engine already installed here may
	// be used instead (helpers need Potluck's own build on every computer).
	Solo bool `json:"solo,omitempty"`
	// For: what the engine is for: "" the main model, "aux:<slot>" the helper or a crew
	// member. Stopping the main model cancels only its own jobs.
	For string `json:"for,omitempty"`
}

func safeJoin(root, name string) (string, error) {
	p := filepath.Join(root, filepath.FromSlash(name))
	if p != root && !strings.HasPrefix(p, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("archive entry escapes the folder: %s", name)
	}
	return p, nil
}

func extractZip(src, dst string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	for _, f := range zr.File {
		p, err := safeJoin(dst, f.Name)
		if err != nil {
			return err
		}
		if f.FileInfo().IsDir() {
			_ = os.MkdirAll(p, 0o755)
			continue
		}
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		out.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func extractTar(r io.Reader, dst string, keep func(name string) (string, bool)) error {
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := h.Name
		if keep != nil {
			var ok bool
			if name, ok = keep(h.Name); !ok {
				continue
			}
		}
		p, err := safeJoin(dst, name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			_ = os.MkdirAll(p, 0o755)
		case tar.TypeSymlink:
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			target := h.Linkname
			if keep != nil {
				target = filepath.Base(target) // flattened layout: links point at siblings
				if target == filepath.Base(p) {
					continue // a link to the same name in another folder: the real file is enough
				}
				if st, err := os.Lstat(p); err == nil && st.Mode().IsRegular() {
					continue // never replace a real library with a link
				}
			}
			if filepath.IsAbs(target) || strings.Contains(target, "..") {
				continue
			}
			_ = os.Remove(p)
			_ = os.Symlink(target, p)
		case tar.TypeReg:
			_ = os.MkdirAll(filepath.Dir(p), 0o755)
			_ = os.Remove(p) // a link with this name may already be here
			out, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode)&0o777|0o644)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, tr)
			out.Close()
			if err != nil {
				return err
			}
		}
	}
}

func findBin(root string, names ...string) string {
	found := ""
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || found != "" {
			return nil
		}
		base := strings.TrimSuffix(d.Name(), ".exe")
		for _, n := range names {
			if base == n {
				found = p
			}
		}
		return nil
	})
	return found
}

func simpleDownload(ctx context.Context, u, dest, sha string) error {
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("User-Agent", userAgent)
	res, err := dlClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return fmt.Errorf("%s answered %s", hostOf(u), res.Status)
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), res.Body)
	f.Close()
	if err != nil {
		return err
	}
	if sha != "" && !strings.EqualFold(hex.EncodeToString(h.Sum(nil)), sha) {
		_ = os.Remove(dest)
		return fmt.Errorf("checksum mismatch for %s", filepath.Base(dest))
	}
	return nil
}

// ensureRuntime installs the llama.cpp build described by spec and returns its folder.
func (a *Agent) ensureRuntime(ctx context.Context, spec RuntimeSpec, note func(string)) (string, error) {
	// One install at a time on a computer: the main model, the helper, a crew member and the
	// early preparation can all ask at once, and share the same folder. A second asker waits
	// and then finds the engine ready.
	a.rtMu.Lock()
	defer a.rtMu.Unlock()
	dir := filepath.Join(a.dir, "runtime", spec.Tag+"-"+spec.Variant)
	if _, err := os.Stat(filepath.Join(dir, ".ok")); err == nil {
		return dir, a.ensureGlibc(ctx, note)
	}
	_ = os.RemoveAll(dir)
	tmp := filepath.Join(a.dir, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	for _, as := range spec.Assets {
		note("Getting the AI engine: " + as.Name)
		file := filepath.Join(tmp, as.Name)
		var err error
		if as.Size > 64<<20 {
			_ = os.Remove(file + ".ok")
			err = downloadFile(ctx, as.URL, file, as.Size, as.SHA256, 8, func(d, t int64, _ string) {
				note(fmt.Sprintf("Getting the AI engine: %d%%", d*100/max64(t, 1)))
			})
		} else {
			err = simpleDownload(ctx, as.URL, file, as.SHA256)
		}
		if err != nil {
			return "", fmt.Errorf("could not download %s: %w", as.Name, err)
		}
		if strings.HasSuffix(as.Name, ".zip") {
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
			return "", fmt.Errorf("could not unpack %s: %w", as.Name, err)
		}
	}
	if runtime.GOOS == "darwin" {
		// Downloads are quarantined and the Intel build ships unsigned; give it a local signature
		// so macOS will run it.
		_ = exec.Command("xattr", "-dr", "com.apple.quarantine", dir).Run()
		_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && d.Type()&os.ModeSymlink == 0 {
				if info, e := d.Info(); e == nil && (strings.HasSuffix(p, ".dylib") || info.Mode()&0o111 != 0) {
					_ = exec.Command("codesign", "-s", "-", "-f", p).Run()
				}
			}
			return nil
		})
	}
	if findBin(dir, "llama-server") == "" || findBin(dir, "ggml-rpc-server", "rpc-server") == "" {
		return "", errors.New("the AI engine download is missing its programs")
	}
	if err := a.ensureGlibc(ctx, note); err != nil {
		return "", err
	}
	_ = os.RemoveAll(tmp)
	return dir, os.WriteFile(filepath.Join(dir, ".ok"), []byte(time.Now().Format(time.RFC3339)+"\n"), 0o644)
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

// ensureGlibc handles Linux systems built on musl (Alpine): the prebuilt engine needs glibc,
// so a private copy from Debian stable goes into the data folder. Nothing is installed
// system-wide.
func (a *Agent) ensureGlibc(ctx context.Context, note func(string)) error {
	if runtime.GOOS != "linux" || !a.info.Musl {
		return nil
	}
	dir := filepath.Join(a.dir, "runtime", "glibc")
	if _, err := os.Stat(filepath.Join(dir, ".ok")); err == nil {
		return nil
	}
	note("Adding compatibility libraries for this Linux")
	arch := map[string]string{"amd64": "amd64", "arm64": "arm64"}[runtime.GOARCH]
	if arch == "" {
		return errors.New("this Linux uses musl and its processor type is not supported yet")
	}
	const mirror = "https://deb.debian.org/debian/"
	req, _ := http.NewRequestWithContext(ctx, "GET", mirror+"dists/stable/main/binary-"+arch+"/Packages.gz", nil)
	req.Header.Set("User-Agent", userAgent)
	res, err := dlClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	gz, err := gzip.NewReader(res.Body)
	if err != nil {
		return err
	}
	want := map[string]bool{"libc6": true, "libstdc++6": true, "libgcc-s1": true, "libgomp1": true}
	type pkg struct{ file, sha string }
	found := map[string]pkg{}
	sc := bufio.NewScanner(gz)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	var name string
	var cur pkg
	flush := func() {
		if want[name] && cur.file != "" {
			found[name] = cur
		}
		name, cur = "", pkg{}
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "Package: "):
			name = strings.TrimPrefix(line, "Package: ")
		case strings.HasPrefix(line, "Filename: "):
			cur.file = strings.TrimPrefix(line, "Filename: ")
		case strings.HasPrefix(line, "SHA256: "):
			cur.sha = strings.TrimPrefix(line, "SHA256: ")
		}
	}
	flush()
	_ = os.RemoveAll(dir)
	lib := filepath.Join(dir, "lib")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		return err
	}
	for n := range want {
		p, ok := found[n]
		if !ok {
			return fmt.Errorf("compatibility library %s not found", n)
		}
		deb := filepath.Join(dir, n+".deb")
		if err := simpleDownload(ctx, mirror+p.file, deb, p.sha); err != nil {
			return err
		}
		data, err := os.ReadFile(deb)
		_ = os.Remove(deb)
		if err != nil {
			return err
		}
		if err := extractDebLibs(data, lib); err != nil {
			return fmt.Errorf("%s: %w", n, err)
		}
	}
	if glibcLoader(dir) == "" {
		return errors.New("compatibility libraries are incomplete")
	}
	return os.WriteFile(filepath.Join(dir, ".ok"), []byte("ok\n"), 0o644)
}

// extractDebLibs pulls every shared library out of a .deb (an `ar` archive holding data.tar.xz).
func extractDebLibs(data []byte, lib string) error {
	if !bytes.HasPrefix(data, []byte("!<arch>\n")) {
		return errors.New("not a .deb file")
	}
	off := 8
	for off+60 <= len(data) {
		hdr := data[off : off+60]
		member := strings.TrimRight(strings.TrimSpace(string(hdr[:16])), "/")
		var size int
		if _, err := fmt.Sscan(strings.TrimSpace(string(hdr[48:58])), &size); err != nil {
			return err
		}
		off += 60
		if off+size > len(data) {
			return errors.New("truncated .deb file")
		}
		body := data[off : off+size]
		off += size + size&1
		if !strings.HasPrefix(member, "data.tar") {
			continue
		}
		var r io.Reader = bytes.NewReader(body)
		switch {
		case strings.HasSuffix(member, ".xz"):
			xr, err := xz.NewReader(r)
			if err != nil {
				return err
			}
			r = xr
		case strings.HasSuffix(member, ".gz"):
			gr, err := gzip.NewReader(r)
			if err != nil {
				return err
			}
			r = gr
		case member != "data.tar":
			return fmt.Errorf("unsupported package compression: %s", member)
		}
		return extractTar(r, lib, func(name string) (string, bool) {
			base := filepath.Base(name)
			return base, strings.Contains(base, ".so")
		})
	}
	return errors.New("package has no data archive")
}

func glibcLoader(dir string) string {
	for _, n := range []string{"ld-linux-x86-64.so.2", "ld-linux-aarch64.so.1"} {
		p := filepath.Join(dir, "lib", n)
		if _, err := os.Stat(p); err == nil {
			_ = os.Chmod(p, 0o755)
			return p
		}
	}
	return ""
}

// ---------- running the engine ----------

type proc struct {
	name    string
	logPath string
	mu      sync.Mutex
	cmd     *exec.Cmd
	stopped bool
	running bool
	done    chan struct{}
}

func (p *proc) Running() bool {
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

func (p *proc) Stop() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.stopped = true
	cmd := p.cmd
	p.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		killTree(cmd.Process.Pid)
		_ = cmd.Process.Kill()
	}
	// Wait for it to be gone, so its memory is free when the caller plans again. A kill that
	// has not taken after half a minute is said so in the log rather than passed over.
	select {
	case <-p.done:
	case <-time.After(30 * time.Second):
		if f, err := os.OpenFile(p.logPath, os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
			fmt.Fprintf(f, "\n[potluck] %s did not end within 30 seconds of being stopped\n", p.name)
			f.Close()
		}
	}
}

// engineCmd prepares one of the engine's programs to run on this computer.
func (a *Agent) engineCmd(bin string, args ...string) *exec.Cmd {
	binDir := filepath.Dir(bin)
	var cmd *exec.Cmd
	if ld := glibcLoader(filepath.Join(a.dir, "runtime", "glibc")); ld != "" && a.info.Musl {
		full := append([]string{"--library-path", filepath.Dir(ld) + ":" + binDir, bin}, args...)
		cmd = exec.Command(ld, full...)
	} else {
		cmd = exec.Command(bin, args...)
	}
	cmd.Dir = binDir // engine backends are looked up next to the program and in the current folder
	cmd.Env = append(os.Environ(), "LLAMA_CACHE="+filepath.Join(a.dir, "cache"), "HF_HUB_CACHE="+filepath.Join(a.dir, "cache", "hub"))
	if strings.HasPrefix(strings.ToLower(filepath.Base(bin)), "inferssd") {
		cmd.Env = append(cmd.Env, a.streamEnv()...)
	}
	if runtime.GOOS == "linux" && !a.info.Musl {
		cmd.Env = append(cmd.Env, "LD_LIBRARY_PATH="+binDir)
	}
	hideWindow(cmd)
	return cmd
}

var deviceLine = regexp.MustCompile(`(?m)^\s+([A-Za-z]+\d+): (.+) \((\d+) MiB, (\d+) MiB free\)`)

// gpuDeviceName asks the installed engine which graphics device it can use and returns the
// name the engine gives it ("CUDA0", "Vulkan1", ...). Empty means: no usable card, run on the
// processor. With several candidates it picks the one that matches the card found in the
// hardware inventory, so graphics built into the processor are passed over.
func (a *Agent) gpuDeviceName(rt string) string {
	base := filepath.Base(rt)
	if !strings.Contains(base, "cuda") && !strings.Contains(base, "vulkan") {
		return ""
	}
	want := a.info.usableGPU()
	if want == nil {
		return ""
	}
	cmd := a.engineCmd(findBin(rt, "llama-server"), "--list-devices")
	done := make(chan []byte, 1)
	go func() { out, _ := cmd.CombinedOutput(); done <- out }()
	var out []byte
	select {
	case out = <-done:
	case <-time.After(40 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return ""
	}
	best, bestDiff := "", int64(-1)
	for _, m := range deviceLine.FindAllStringSubmatch(string(out), -1) {
		name, desc := m[1], m[2]
		if strings.HasPrefix(name, "RPC") || vendorOf(desc) != want.Vendor {
			continue
		}
		total, _ := strconv.ParseInt(m[3], 10, 64)
		diff := total<<20 - int64(want.VRAMTotal)
		if diff < 0 {
			diff = -diff
		}
		if bestDiff < 0 || diff < bestDiff {
			best, bestDiff = name, diff
		}
	}
	return best
}

// startProc runs an engine program, keeps its output in a log file, and (for the worker
// program) starts it again if it stops: it quits when a client hangs up at the wrong moment.
func (a *Agent) startProc(name, rtDir, bin string, args []string, restart, lowPrio bool) (*proc, error) {
	logDir := filepath.Join(a.dir, "logs")
	_ = os.MkdirAll(logDir, 0o755)
	p := &proc{name: name, logPath: filepath.Join(logDir, name+".log"), done: make(chan struct{})}
	logf, err := os.Create(p.logPath)
	if err != nil {
		return nil, err
	}
	build := func() *exec.Cmd {
		cmd := a.engineCmd(bin, args...)
		cmd.Stdout, cmd.Stderr = logf, logf
		tieToParent(cmd)
		return cmd
	}
	first := make(chan error, 1)
	go func() {
		// The engine is tied to the thread that starts it on Linux (tieToParent), so this
		// goroutine keeps its thread for the engine's whole life.
		runtime.LockOSThread()
		defer close(p.done)
		defer logf.Close()
		quick := 0
		for i := 0; ; i++ {
			cmd := build()
			started := time.Now()
			err := cmd.Start()
			if i == 0 {
				first <- err
			}
			if err != nil {
				return
			}
			p.mu.Lock()
			p.cmd, p.running = cmd, true
			p.mu.Unlock()
			// Engines always run below normal priority. With the computer otherwise idle that
			// costs nothing; when memory or the processor get tight, the desktop, remote logins
			// and Potluck's guard still get their turn instead of freezing behind the engine.
			// lowPrio (a pedal below 100%) used to be the only case; it now changes nothing.
			_ = lowPrio
			adoptEngine(cmd.Process.Pid)
			lowerPriority(cmd.Process.Pid)
			lastResort(cmd.Process.Pid)
			noteEngine(cmd.Process.Pid, cmd.Path)
			_ = cmd.Wait()
			forgetEngine(cmd.Process.Pid)
			p.mu.Lock()
			p.running = false
			stopped := p.stopped
			p.mu.Unlock()
			if stopped || !restart {
				return
			}
			// Start it again, but not forever: an engine that keeps dying within a minute of
			// starting has a cause a restart will not fix (a bad file, no memory, a broken
			// driver). After five such ends in a row it stays down, and its log says why.
			if time.Since(started) < time.Minute {
				quick++
			} else {
				quick = 0
			}
			if quick >= 5 {
				fmt.Fprintf(logf, "\n[potluck] %s ended %d times in a row within a minute of starting; not starting it again\n", name, quick)
				return
			}
			wait := time.Second << quick // 1, 2, 4, 8, 16 seconds
			fmt.Fprintf(logf, "\n[potluck] %s stopped, starting it again in %s\n", name, wait)
			time.Sleep(wait)
			p.mu.Lock()
			stopped = p.stopped
			p.mu.Unlock()
			if stopped {
				return
			}
		}
	}()
	if err := <-first; err != nil {
		return nil, err
	}
	return p, nil
}

func tailFile(path string, maxBytes int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, _ := f.Stat()
	if st.Size() > maxBytes {
		_, _ = f.Seek(-maxBytes, io.SeekEnd)
	}
	b, _ := io.ReadAll(f)
	return string(b)
}

func headFile(path string, maxBytes int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	b, _ := io.ReadAll(io.LimitReader(f, maxBytes))
	return string(b)
}
