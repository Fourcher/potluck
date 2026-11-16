package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// GPU describes one graphics device. Usable means llama.cpp's prebuilt binaries can run on it.
type GPU struct {
	Name      string `json:"name"`
	Vendor    string `json:"vendor"`
	VRAMTotal uint64 `json:"vramTotal"`
	VRAMFree  uint64 `json:"vramFree"`
	Unified   bool   `json:"unified"` // shares system RAM (Apple Silicon)
	Usable    bool   `json:"usable"`
}

// Limits is how hard a computer is allowed to work: the "pedal".
type Limits struct {
	Pedal      int    `json:"pedal"`      // 0-100: share of memory and CPU cores offered
	Enabled    bool   `json:"enabled"`    // off = never used
	SchedOn    bool   `json:"schedOn"`    // only work between SchedStart and SchedEnd
	SchedStart string `json:"schedStart"` // "22:00"
	SchedEnd   string `json:"schedEnd"`   // "07:00"
}

func defaultLimits(isMain bool) Limits {
	l := Limits{Pedal: 100, Enabled: true, SchedStart: "22:00", SchedEnd: "07:00"}
	if isMain {
		l.Enabled = false // the main computer coordinates; it only lends muscle if you turn it on
		l.Pedal = 50
	}
	return l
}

// ActiveNow says whether the computer may be used at this moment.
func (l Limits) ActiveNow(now time.Time) bool {
	if !l.Enabled || l.Pedal <= 0 {
		return false
	}
	if !l.SchedOn {
		return true
	}
	parse := func(s string) int {
		p := strings.Split(s, ":")
		if len(p) != 2 {
			return -1
		}
		h, e1 := strconv.Atoi(p[0])
		m, e2 := strconv.Atoi(p[1])
		if e1 != nil || e2 != nil {
			return -1
		}
		return h*60 + m
	}
	s, e, n := parse(l.SchedStart), parse(l.SchedEnd), now.Hour()*60+now.Minute()
	if s < 0 || e < 0 || s == e {
		return true
	}
	if s < e {
		return n >= s && n < e
	}
	return n >= s || n < e // window crosses midnight
}

// NodeInfo is what a computer reports about itself.
type NodeInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	OS       string `json:"os"`
	OSName   string `json:"osName"`
	Arch     string `json:"arch"`
	CPU      string `json:"cpu"`
	Cores    int    `json:"cores"`
	Threads  int    `json:"threads"`
	RAMTotal uint64 `json:"ramTotal"`
	RAMAvail uint64 `json:"ramAvail"`
	GPUs     []GPU  `json:"gpus"`
	DiskFree uint64 `json:"diskFree"`
	Musl     bool   `json:"musl"`
	Version  string `json:"version"`
	Limits   Limits `json:"limits"`
	// HeldBytes is model memory this app's own processes hold right now, so that planning a
	// restart does not count it as "in use by something else".
	HeldBytes uint64 `json:"heldBytes"`
}

func run(timeout time.Duration, name string, args ...string) string {
	cmd := exec.Command(name, args...)
	hideWindow(cmd)
	done := make(chan []byte, 1)
	go func() { out, _ := cmd.Output(); done <- out }()
	select {
	case out := <-done:
		return strings.TrimSpace(string(out))
	case <-time.After(timeout):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return ""
	}
}

func readFile(path string) string { b, _ := os.ReadFile(path); return string(b) }

// staticInfo collects the facts that do not change while running. It can take a second or two.
func staticInfo(dataDir string) NodeInfo {
	n := NodeInfo{OS: runtime.GOOS, Arch: runtime.GOARCH, Threads: runtime.NumCPU(), Cores: runtime.NumCPU(), Version: version}
	host, _ := os.Hostname()
	n.Name = strings.TrimSuffix(host, ".local")
	switch runtime.GOOS {
	case "linux":
		n.OSName = "Linux"
		for _, line := range strings.Split(readFile("/etc/os-release"), "\n") {
			if strings.HasPrefix(line, "PRETTY_NAME=") {
				n.OSName = strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
			}
		}
		cores := map[string]bool{}
		phys := ""
		for _, line := range strings.Split(readFile("/proc/cpuinfo"), "\n") {
			k, v, _ := strings.Cut(line, ":")
			k, v = strings.TrimSpace(k), strings.TrimSpace(v)
			switch k {
			case "model name":
				if n.CPU == "" {
					n.CPU = v
				}
			case "physical id":
				phys = v
			case "core id":
				cores[phys+":"+v] = true
			}
		}
		if len(cores) > 0 {
			n.Cores = len(cores)
		}
		if n.CPU == "" {
			n.CPU = runtime.GOARCH + " processor"
		}
		musl, _ := filepath.Glob("/lib/ld-musl-*")
		glibc := false
		for _, p := range []string{"/lib64/ld-linux-x86-64.so.2", "/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2", "/lib/ld-linux-aarch64.so.1", "/lib/aarch64-linux-gnu/ld-linux-aarch64.so.1"} {
			if _, err := os.Stat(p); err == nil {
				glibc = true
			}
		}
		n.Musl = len(musl) > 0 && !glibc
		if out := run(4*time.Second, "lspci"); out != "" {
			re := regexp.MustCompile(`(?i)(VGA compatible controller|3D controller|Display controller): (.+)`)
			for _, m := range re.FindAllStringSubmatch(out, -1) {
				n.GPUs = append(n.GPUs, GPU{Name: strings.TrimSpace(regexp.MustCompile(` \(rev .+\)$`).ReplaceAllString(m[2], "")), Vendor: vendorOf(m[2])})
			}
		}
	case "darwin":
		n.OSName = "macOS " + run(3*time.Second, "sw_vers", "-productVersion")
		n.CPU = run(3*time.Second, "sysctl", "-n", "machdep.cpu.brand_string")
		if c, err := strconv.Atoi(run(3*time.Second, "sysctl", "-n", "hw.physicalcpu")); err == nil && c > 0 {
			n.Cores = c
		}
		if m, err := strconv.ParseUint(run(3*time.Second, "sysctl", "-n", "hw.memsize"), 10, 64); err == nil {
			n.RAMTotal = m
		}
		if runtime.GOARCH == "arm64" {
			n.GPUs = []GPU{{Name: "Apple GPU (Metal)", Vendor: "apple", Unified: true, Usable: true, VRAMTotal: n.RAMTotal}}
		} else {
			out := run(8*time.Second, "system_profiler", "SPDisplaysDataType")
			for _, m := range regexp.MustCompile(`Chipset Model: (.+)`).FindAllStringSubmatch(out, -1) {
				n.GPUs = append(n.GPUs, GPU{Name: strings.TrimSpace(m[1]), Vendor: vendorOf(m[1])}) // not usable: Intel Mac builds are CPU-only
			}
		}
	case "windows":
		ps := `$os=Get-CimInstance Win32_OperatingSystem;$cpu=Get-CimInstance Win32_Processor|Select-Object -First 1;` +
			`$g=@(Get-CimInstance Win32_VideoController|ForEach-Object{$_.Name});` +
			`@{os=$os.Caption;cpu=$cpu.Name;cores=[int](Get-CimInstance Win32_Processor|Measure-Object NumberOfCores -Sum).Sum;gpus=$g}|ConvertTo-Json -Compress`
		var w struct {
			OS    string   `json:"os"`
			CPU   string   `json:"cpu"`
			Cores int      `json:"cores"`
			GPUs  []string `json:"gpus"`
		}
		_ = json.Unmarshal([]byte(run(20*time.Second, "powershell", "-NoProfile", "-NonInteractive", "-Command", ps)), &w)
		n.OSName, n.CPU = strings.TrimSpace(strings.TrimPrefix(w.OS, "Microsoft ")), strings.TrimSpace(w.CPU)
		if w.Cores > 0 {
			n.Cores = w.Cores
		}
		for _, g := range w.GPUs {
			if !regexp.MustCompile(`(?i)virtual|remote|basic|meta `).MatchString(g) {
				n.GPUs = append(n.GPUs, GPU{Name: g, Vendor: vendorOf(g)})
			}
		}
		if n.OSName == "" {
			n.OSName = "Windows"
		}
	default:
		n.OSName, n.CPU = runtime.GOOS, runtime.GOARCH+" processor"
	}
	// NVIDIA cards: nvidia-smi gives real memory numbers and tells us the driver works.
	if nv := nvidiaGPUs(); len(nv) > 0 {
		var keep []GPU
		for _, g := range n.GPUs {
			if g.Vendor != "nvidia" {
				keep = append(keep, g)
			}
		}
		n.GPUs = append(nv, keep...)
	}
	n.refresh(dataDir)
	return n
}

func vendorOf(name string) string {
	l := strings.ToLower(name)
	switch {
	case strings.Contains(l, "nvidia"), strings.Contains(l, "geforce"), strings.Contains(l, "quadro"):
		return "nvidia"
	case strings.Contains(l, "amd"), strings.Contains(l, "radeon"):
		return "amd"
	case strings.Contains(l, "intel"):
		return "intel"
	case strings.Contains(l, "apple"):
		return "apple"
	}
	return "other"
}

func nvidiaGPUs() []GPU {
	if _, err := exec.LookPath("nvidia-smi"); err != nil {
		return nil
	}
	out := run(6*time.Second, "nvidia-smi", "--query-gpu=name,memory.total,memory.free", "--format=csv,noheader,nounits")
	var gpus []GPU
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, ",")
		if len(f) != 3 {
			continue
		}
		total, e1 := strconv.ParseUint(strings.TrimSpace(f[1]), 10, 64)
		free, e2 := strconv.ParseUint(strings.TrimSpace(f[2]), 10, 64)
		if e1 != nil || e2 != nil {
			continue
		}
		gpus = append(gpus, GPU{Name: "NVIDIA " + strings.TrimPrefix(strings.TrimSpace(f[0]), "NVIDIA "), Vendor: "nvidia", VRAMTotal: total << 20, VRAMFree: free << 20, Usable: true})
	}
	return gpus
}

// refresh updates the numbers that move: free memory, free video memory, free disk.
func (n *NodeInfo) refresh(dataDir string) {
	switch runtime.GOOS {
	case "linux":
		for _, line := range strings.Split(readFile("/proc/meminfo"), "\n") {
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			v, _ := strconv.ParseUint(f[1], 10, 64)
			switch f[0] {
			case "MemTotal:":
				n.RAMTotal = v << 10
			case "MemAvailable:":
				n.RAMAvail = v << 10
			}
		}
	case "darwin":
		out := run(3*time.Second, "vm_stat")
		page := uint64(4096)
		if m := regexp.MustCompile(`page size of (\d+) bytes`).FindStringSubmatch(out); m != nil {
			page, _ = strconv.ParseUint(m[1], 10, 64)
		}
		var pages uint64
		for _, key := range []string{"Pages free", "Pages inactive", "Pages speculative", "Pages purgeable"} {
			if m := regexp.MustCompile(key + `:\s+(\d+)`).FindStringSubmatch(out); m != nil {
				v, _ := strconv.ParseUint(m[1], 10, 64)
				pages += v
			}
		}
		n.RAMAvail = pages * page
		// macOS can reclaim far more than those page counts suggest (compressed and cached
		// memory); its own "free percentage" is the better guide when it is higher.
		if m := regexp.MustCompile(`free percentage: (\d+)%`).FindStringSubmatch(run(3*time.Second, "memory_pressure")); m != nil {
			pct, _ := strconv.ParseUint(m[1], 10, 64)
			if est := n.RAMTotal / 100 * pct; est > n.RAMAvail {
				n.RAMAvail = est
			}
		}
	default:
		if total, avail, ok := sysMem(); ok {
			n.RAMTotal, n.RAMAvail = total, avail
		}
	}
	hasNvidia := false
	for _, g := range n.GPUs {
		if g.Vendor == "nvidia" && g.Usable {
			hasNvidia = true
		}
	}
	if hasNvidia {
		if nv := nvidiaGPUs(); len(nv) > 0 {
			var keep []GPU
			for _, g := range n.GPUs {
				if !(g.Vendor == "nvidia" && g.Usable) {
					keep = append(keep, g)
				}
			}
			n.GPUs = append(nv, keep...)
		}
	}
	for i := range n.GPUs {
		if n.GPUs[i].Unified {
			n.GPUs[i].VRAMTotal, n.GPUs[i].VRAMFree = n.RAMTotal, n.RAMAvail
		}
	}
	n.DiskFree = diskFree(dataDir)
}

// usableGPU returns the first GPU llama.cpp can use on this computer, if any.
func (n *NodeInfo) usableGPU() *GPU {
	for i := range n.GPUs {
		if n.GPUs[i].Usable {
			return &n.GPUs[i]
		}
	}
	return nil
}
