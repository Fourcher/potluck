package main

// Measuring a computer, and estimating how fast a model will run on it.
//
// Writing a reply is limited by how fast a computer can read the model's weights from
// memory: every word reads the part of the model it uses once. So the benchmark measures
// memory read speed and, where a big model file is already on the computer, how fast its
// drive reads. It takes a few seconds and downloads nothing.
//
// The estimate is generation ≈ efficiency × read speed ÷ bytes read per word, with a cap for
// quantisations whose arithmetic, not memory, is the limit. The efficiency factors are fitted
// to speeds measured on real computers (see estimateCalibration). Real runs are better still:
// every speed Potluck measures is saved, and shown in place of the estimate.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	mrand "math/rand"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"
)

// Profile is what Potluck knows about one computer's speed.
type Profile struct {
	When      time.Time `json:"when"`
	Version   string    `json:"version"`
	CPU       string    `json:"cpu"`
	Threads   int       `json:"threads"`
	RAMTotal  uint64    `json:"ramTotal"`
	GPU       string    `json:"gpu,omitempty"`
	MemGBs    float64   `json:"memGBs"`            // memory read speed, all threads
	MemGBs1   float64   `json:"memGBs1"`           // one thread
	DiskMBs   float64   `json:"diskMBs,omitempty"` // random 4 MiB reads from the models folder's drive
	DiskNote  string    `json:"diskNote,omitempty"`
	Seconds   float64   `json:"seconds"` // how long the measurement took
	Signature string    `json:"signature"`
}

// profileSignature changes when the hardware a profile describes changes.
func profileSignature(info NodeInfo) string {
	g := ""
	if u := info.usableGPU(); u != nil {
		g = u.Name
	}
	return fmt.Sprintf("%s|%d|%d|%s", info.CPU, info.Threads, info.RAMTotal/(1<<30), g)
}

// Stale says whether a profile no longer describes this computer.
func (p *Profile) Stale(info NodeInfo) bool {
	return p == nil || p.Signature != profileSignature(info)
}

// runBenchmark measures this computer. dataDir is where its models are.
func runBenchmark(info NodeInfo, dataDir string) Profile {
	start := time.Now()
	p := Profile{When: start, Version: version, CPU: info.CPU, Threads: runtime.NumCPU(), RAMTotal: info.RAMTotal, Signature: profileSignature(info)}
	if g := info.usableGPU(); g != nil {
		p.GPU = g.Name
	}
	p.MemGBs1 = memReadSpeed(1, info.RAMTotal, 1500*time.Millisecond)
	p.MemGBs = memReadSpeed(runtime.NumCPU(), info.RAMTotal, 2500*time.Millisecond)
	p.DiskMBs, p.DiskNote = diskReadSpeed(filepath.Join(dataDir, "models"), info.RAMAvail, 4*time.Second)
	p.Seconds = math.Round(time.Since(start).Seconds()*10) / 10
	return p
}

var benchSink atomic.Uint64

// memReadSpeed reads a buffer over and over with the given number of threads and returns
// gigabytes per second. The buffer is far bigger than any processor cache.
func memReadSpeed(threads int, ramTotal uint64, d time.Duration) float64 {
	size := uint64(1 << 30)
	if ramTotal > 0 && ramTotal/8 < size {
		size = ramTotal / 8
	}
	if size < 256<<20 {
		size = 256 << 20
	}
	buf := make([]uint64, size/8)
	for i := range buf {
		buf[i] = uint64(i)
	}
	chunk := len(buf) / threads
	var total atomic.Int64
	deadline := time.Now().Add(d)
	t0 := time.Now()
	var wg sync.WaitGroup
	for t := 0; t < threads; t++ {
		wg.Add(1)
		go func(part []uint64) {
			defer wg.Done()
			var s0, s1, s2, s3 uint64
			for time.Now().Before(deadline) {
				n := len(part) &^ 7
				for i := 0; i < n; i += 8 {
					s0 += part[i] ^ part[i+4]
					s1 += part[i+1] ^ part[i+5]
					s2 += part[i+2] ^ part[i+6]
					s3 += part[i+3] ^ part[i+7]
				}
				total.Add(int64(n) * 8)
			}
			benchSink.Add(s0 + s1 + s2 + s3)
		}(buf[t*chunk : (t+1)*chunk])
	}
	wg.Wait()
	gbs := float64(total.Load()) / time.Since(t0).Seconds() / 1e9
	return math.Round(gbs*10) / 10
}

// alignedBuf returns a buffer whose start is aligned to 4 KiB, as uncached reads require.
func alignedBuf(n int) []byte {
	const align = 4096
	raw := make([]byte, n+align)
	off := int(uintptr(unsafe.Pointer(&raw[0])) & (align - 1))
	if off != 0 {
		off = align - off
	}
	return raw[off : off+n]
}

const benchBlock = 4 << 20

// diskReadSpeed measures how fast the drive holding dir reads random 4 MiB blocks, the way a
// model is read from disk. It reads past the operating system's cache, so it measures the
// drive and not the memory: from the biggest model file there, or, when there is none of at
// least 1 GiB, from a 1 GiB file it writes for the purpose and deletes again.
func diskReadSpeed(dir string, ramAvail uint64, d time.Duration) (float64, string) {
	_ = os.MkdirAll(dir, 0o755)
	var best string
	var bestSize int64
	_ = filepath.WalkDir(dir, func(p string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() && strings.HasSuffix(strings.ToLower(e.Name()), ".gguf") {
			if fi, err := e.Info(); err == nil && fi.Size() > bestSize {
				best, bestSize = p, fi.Size()
			}
		}
		return nil
	})
	if bestSize < 1<<30 {
		if diskFree(dir) < 3<<30 {
			return 0, "Less than 3 GB free on the drive, so it was not measured."
		}
		tmp := filepath.Join(dir, ".potluck-drive-test")
		defer os.Remove(tmp)
		if err := writeTestFile(tmp, 1<<30); err != nil {
			return 0, "Could not write a test file to measure the drive."
		}
		best, bestSize = tmp, 1<<30
	}
	if mbs, err := readRandomBlocks(best, bestSize, d, true); err == nil {
		return mbs, ""
	}
	// Uncached reads are not possible here: only a file bigger than the free memory then
	// gives an honest number.
	if best != "" && uint64(bestSize) > ramAvail && !strings.HasSuffix(best, ".potluck-drive-test") {
		if mbs, err := readRandomBlocks(best, bestSize, d, false); err == nil {
			return mbs, ""
		}
	}
	return 0, "This drive could not be measured without the computer's cache getting in the way."
}

// writeTestFile writes size bytes of varied data and makes sure they reach the drive.
func writeTestFile(path string, size int64) error {
	f, err := openUncached(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC)
	if err != nil {
		if f, err = os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644); err != nil {
			return err
		}
	}
	buf := alignedBuf(benchBlock)
	r := mrand.New(mrand.NewSource(1))
	for i := range buf {
		buf[i] = byte(r.Intn(256))
	}
	for written := int64(0); written < size; written += benchBlock {
		buf[0], buf[1] = byte(written>>22), byte(written>>30)
		if _, err := f.Write(buf); err != nil {
			f.Close()
			return err
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// readRandomBlocks reads random aligned 4 MiB blocks for d and returns MB/s.
func readRandomBlocks(path string, size int64, d time.Duration, uncached bool) (float64, error) {
	var f *os.File
	var err error
	if uncached {
		f, err = openUncached(path, os.O_RDONLY)
	} else {
		f, err = os.Open(path)
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	buf := alignedBuf(benchBlock)
	blocks := size / benchBlock
	if blocks < 1 {
		return 0, errors.New("file too small")
	}
	r := mrand.New(mrand.NewSource(time.Now().UnixNano()))
	var read int64
	deadline := time.Now().Add(d)
	t0 := time.Now()
	for time.Now().Before(deadline) {
		n, err := f.ReadAt(buf, r.Int63n(blocks)*benchBlock)
		read += int64(n)
		if err != nil && !errors.Is(err, io.EOF) {
			return 0, err
		}
	}
	if read == 0 {
		return 0, errors.New("nothing read")
	}
	return math.Round(float64(read) / time.Since(t0).Seconds() / 1e6), nil
}

// ---------- estimates ----------

// estimateCalibration holds the factors fitted to measured runs (November 2026):
//   - generation reaches about 60% of the measured memory read speed on processors, before
//     arithmetic limits (Qwen3-30B-A3B Q4_K_M on a Ryzen 5 2400G reading 32 GB/s: 8.0 to 9.8
//     words a second measured, 9.1 estimated);
//   - i-quants (IQ1 to IQ3) are limited by arithmetic: a few gigabytes of weights per second
//     per thread-group, measured 18 GB/s on a 12-thread laptop processor;
//   - streaming from a drive (InferSSD), the experts a word needs make up about 30% of what it
//     reads, and about 60% of the measured random-read speed reaches the model (the
//     122B-A10B model on that PC's SATA SSD at 443 MB/s: 1.0 words a second measured, 0.9
//     estimated).
var estimateCalibration = struct {
	memEfficiency, iquantGBsPerThread, diskEfficiency float64
}{0.6, 1.5, 0.6}

// Estimate is a speed range for running a model one way.
type Estimate struct {
	Route   string  `json:"route"` // one | pool | stream
	Low     float64 `json:"low"`   // words (tokens) per second
	High    float64 `json:"high"`  // words (tokens) per second
	Basis   string  `json:"basis"` // what the estimate rests on
	Machine string  `json:"machine,omitempty"`
}

var activeRatio = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)b-a(\d+(?:\.\d+)?)b`)

// bytesPerWord is how much of a model one word reads: all of it for an ordinary model; for a
// mixture-of-experts model named like "122B-A10B", roughly the active share plus a part that
// every word uses.
func bytesPerWord(file string, size int64) int64 {
	if m := activeRatio.FindStringSubmatch(file); m != nil {
		total, _ := strconv.ParseFloat(m[1], 64)
		active, _ := strconv.ParseFloat(m[2], 64)
		if total > 0 && active > 0 && active < total {
			return int64(float64(size) * active / total * 1.15)
		}
	}
	return size
}

func isIQuant(file string) bool { return regexp.MustCompile(`(?i)[-.]IQ[123]`).MatchString(file) }

// cardSaving is the share of each word's time a computer's own graphics card saves by
// holding the part of a mixture-of-experts model that every word uses (Potluck's layout for
// such models). Measured on a GTX 1050 Ti with Qwen3-30B-A3B: 9.7 words/s without the card,
// 12.7 with it, so about 24% less time per word. Ordinary (dense) models are not covered yet:
// no measurement backs a factor for them.
func cardSaving(n PlanNode, file string) float64 {
	if n.Unified || n.GPUBytes < 1<<30 || !activeRatio.MatchString(file) {
		return 0
	}
	return 0.24 * math.Min(1, float64(n.GPUBytes)/float64(2<<30))
}

// genSpeed is the estimated writing speed for reading bpw bytes per word at memGBs.
func genSpeed(bpw int64, memGBs float64, threads int, iquant bool) float64 {
	if bpw <= 0 || memGBs <= 0 {
		return 0
	}
	rate := memGBs * estimateCalibration.memEfficiency
	if iquant {
		if c := estimateCalibration.iquantGBsPerThread * float64(threads); c < rate {
			rate = c
		}
	}
	return rate * 1e9 / float64(bpw)
}

// estimateFile gives speed ranges for a store file, given the computers' profiles and what
// the planner says about where it fits.
func estimateFile(f StoreFile, nodes []PlanNode, profiles map[string]*Profile) []Estimate {
	bpw := bytesPerWord(f.File, f.Size)
	iq := isIQuant(f.File)
	rng := func(v float64) (float64, float64) { return round1(v * 0.6), round1(v * 1.3) }
	var out []Estimate
	// One computer: the best-profiled one that can hold it.
	var best *Estimate
	for _, n := range nodes {
		p := profiles[n.ID]
		if p == nil || p.MemGBs <= 0 {
			continue
		}
		hold := n.CPUBytes + n.GPUBytes
		if n.Unified {
			hold = n.GPUBytes
		}
		if f.Size+f.Size/25+perDeviceOverhead > hold {
			continue
		}
		v := genSpeed(bpw, p.MemGBs, p.Threads, iq) / (1 - cardSaving(n, f.File))
		if best == nil || v > best.High/1.3 {
			lo, hi := rng(v)
			best = &Estimate{Route: "one", Low: lo, High: hi, Machine: n.Name, Basis: fmt.Sprintf("%s reads memory at %.0f GB/s", n.Name, p.MemGBs)}
		}
	}
	if best != nil {
		out = append(out, *best)
	}
	// Pooled: each computer reads its share in turn, plus two network hops per helper.
	if f.Verdict == "pool" {
		type m struct {
			bytes int64
			p     *Profile
			link  float64
			save  float64
		}
		var ms []m
		var held int64
		sorted := append([]PlanNode(nil), nodes...)
		sort.Slice(sorted, func(i, j int) bool {
			return sorted[i].CPUBytes+sorted[i].GPUBytes > sorted[j].CPUBytes+sorted[j].GPUBytes
		})
		ok := true
		for _, n := range sorted {
			if held >= f.Size {
				break
			}
			p := profiles[n.ID]
			if p == nil || p.MemGBs <= 0 {
				ok = false
				break
			}
			take := n.CPUBytes + n.GPUBytes - perDeviceOverhead
			if take > f.Size-held {
				take = f.Size - held
			}
			if take <= 0 {
				continue
			}
			ms = append(ms, m{take, p, n.LinkMs, cardSaving(n, f.File)})
			held += take
		}
		if ok && held >= f.Size && len(ms) > 0 {
			var sec float64
			for i, x := range ms {
				share := int64(float64(bpw) * float64(x.bytes) / float64(f.Size))
				if v := genSpeed(share, x.p.MemGBs, x.p.Threads, iq); v > 0 {
					sec += (1 - x.save) / v
				}
				if i > 0 {
					link := x.link
					if link <= 0 {
						link = 15
					}
					sec += 2 * link / 1000
				}
			}
			if sec > 0 {
				// Wider than one computer's range: the network moves it more.
				lo, hi := round1(0.6/sec), round1(1.3/sec)
				out = append(out, Estimate{Route: "pool", Low: lo, High: hi, Basis: fmt.Sprintf("%d computers, each reading its share, plus the network between them", len(ms))})
			}
		}
	}
	// Streaming from disk: the experts a word needs come from the drive.
	if f.Verdict == "stream" {
		for _, n := range nodes {
			p := profiles[n.ID]
			if n.StreamVariant == "" || p == nil {
				continue
			}
			if p.DiskMBs <= 0 {
				out = append(out, Estimate{Route: "stream", Machine: n.Name, Basis: n.Name + "'s drive has not been measured, so no speed is estimated."})
				continue
			}
			// The part of each word's reads that misses memory comes from the drive.
			mem := float64(n.CPUBytes)
			miss := 1 - mem/float64(f.Size)
			if miss < 0.1 {
				miss = 0.1
			}
			disk := p.DiskMBs * 1e6 * estimateCalibration.diskEfficiency / (float64(bpw) * 0.3 * miss)
			v := math.Min(disk, genSpeed(bpw, p.MemGBs, p.Threads, iq))
			lo, hi := rng(v)
			out = append(out, Estimate{Route: "stream", Low: lo, High: hi, Machine: n.Name, Basis: fmt.Sprintf("%s's drive reads %.0f MB/s", n.Name, p.DiskMBs)})
		}
	}
	return out
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// ---------- learned speeds ----------

// Measured is a speed Potluck saw on a real run.
type Measured struct {
	File   string    `json:"file"`
	Layout string    `json:"layout"` // e.g. "pooled: Monolith, LLMOS" or "Monolith alone"
	Gen    float64   `json:"gen"`    // tokens per second, writing
	Prompt float64   `json:"prompt"` // tokens per second, reading
	When   time.Time `json:"when"`
}

// ---------- the command-line benchmark ----------

// benchCommand is "potluck bench": measure this computer and print the result. It needs
// nothing else running.
func benchCommand(dataDir string) {
	info := staticInfo(dataDir)
	info.refresh(dataDir)
	p := runBenchmark(info, dataDir)
	b, _ := json.MarshalIndent(p, "", "  ")
	fmt.Println(string(b))
}
