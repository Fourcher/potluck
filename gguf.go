package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ggml tensor type id -> (elements per block, bytes per block). Needed to know exactly how
// many bytes each tensor occupies in the file.
var ggmlTypes = map[uint32][2]uint64{
	0: {1, 4}, 1: {1, 2}, 2: {32, 18}, 3: {32, 20}, 6: {32, 22}, 7: {32, 24}, 8: {32, 34}, 9: {32, 36},
	10: {256, 84}, 11: {256, 110}, 12: {256, 144}, 13: {256, 176}, 14: {256, 210}, 15: {256, 292},
	16: {256, 66}, 17: {256, 74}, 18: {256, 98}, 19: {256, 50}, 20: {32, 18}, 21: {256, 110},
	22: {256, 82}, 23: {256, 136}, 24: {1, 1}, 25: {1, 2}, 26: {1, 4}, 27: {1, 8}, 28: {1, 8},
	29: {256, 56}, 30: {1, 2}, 34: {256, 54}, 35: {256, 66},
}

// Tensor is one tensor's place in the model file.
type Tensor struct {
	Name  string `json:"name"`
	Layer int    `json:"layer"` // -1 for tensors outside the numbered layers
	Part  int    `json:"part"`  // which file of a multi-file model it is in (0 for the first or only one)
	Off   int64  `json:"off"`   // absolute offset in the file
	Size  int64  `json:"size"`
	OK    bool   `json:"ok"` // size confirmed against the next tensor's offset
}

// ModelInfo is what the header of a GGUF file tells us.
type ModelInfo struct {
	Name       string  `json:"name"`
	Arch       string  `json:"arch"`
	Layers     int     `json:"layers"`
	CtxTrain   int     `json:"ctxTrain"`
	FileSize   int64   `json:"fileSize"`
	LayerBytes []int64 `json:"layerBytes"`
	OtherBytes int64   `json:"otherBytes"` // embeddings, output head
	// Mixture-of-experts models: how much of each layer is expert weights. Only a few experts
	// are read for each word, while the rest of the layer (the "trunk") is read every time.
	ExpertBytes []int64  `json:"expertBytes,omitempty"`
	OutputBytes int64    `json:"outputBytes,omitempty"` // the output head alone
	FileType    int      `json:"fileType,omitempty"`    // general.file_type: how it is compressed
	Params      uint64   `json:"params,omitempty"`      // number of weights
	Tensors     []Tensor `json:"-"`

	// The attention shape, which sets how much memory the context takes (see ContextBytes).
	KVHeads   []int  `json:"kvHeads,omitempty"` // key/value heads per layer (0: the layer keeps no cache)
	KeyLen    int    `json:"keyLen,omitempty"`
	ValLen    int    `json:"valLen,omitempty"`
	SWAWindow int    `json:"swaWindow,omitempty"` // sliding-window layers only keep this many recent tokens
	SWALayers []bool `json:"swaLayers,omitempty"` // which layers use the sliding window
	LoopStart int    `json:"loopStart,omitempty"` // looped models run layers [LoopStart, LoopEnd)
	LoopEnd   int    `json:"loopEnd,omitempty"`   // LoopCount times, and keep a cache for every pass
	LoopCount int    `json:"loopCount,omitempty"`
	Vocab     int    `json:"vocab,omitempty"`
	PlanCtx   int    `json:"-"` // the context size a plan is being made for
	WorkSeen  int64  `json:"-"` // the engine's own report of its working memory for this file and size, if known
	KVKeyQ8   bool   `json:"-"` // plan with the cache's keys in 8 bits (llama.cpp -ctk q8_0), about a quarter smaller
}

// ubatch is the engine's default batch of tokens per step. A sliding-window layer keeps its
// window plus one batch (llama.cpp: "SWA KV cache, size = 1536 cells" for a 1024 window).
const ubatch = 512

// ContextBytes is the memory each layer's context cache takes for ctx tokens (16-bit keys and
// values, as the engine stores them by default). It follows the engine's own accounting:
// full-attention layers keep every token, sliding-window layers their window plus one batch,
// and layers of a looped model keep one cache per pass. Nil when the file does not say.
func (m *ModelInfo) ContextBytes(ctx int) []int64 {
	if ctx <= 0 || len(m.KVHeads) == 0 || m.KeyLen+m.ValLen == 0 {
		return nil
	}
	out := make([]int64, m.Layers)
	for i := range out {
		heads := m.KVHeads[0]
		if i < len(m.KVHeads) {
			heads = m.KVHeads[i]
		}
		tokens := ctx
		if m.SWAWindow > 0 && i < len(m.SWALayers) && m.SWALayers[i] && m.SWAWindow+ubatch < ctx {
			tokens = m.SWAWindow + ubatch
		}
		passes := 1
		if m.LoopCount > 1 && i >= m.LoopStart && i < m.LoopEnd {
			passes = m.LoopCount
		}
		// 16-bit keys and values; 8-bit keys (q8_0: 34 bytes per 32 values) when squeezed.
		perTok := int64(heads) * int64(m.KeyLen+m.ValLen) * 2
		if m.KVKeyQ8 {
			perTok = int64(heads) * (int64(m.KeyLen)*34/32 + int64(m.ValLen)*2)
		}
		out[i] = int64(passes) * int64(tokens) * perTok
	}
	return out
}

// ContextTotal is the whole context cache for ctx tokens.
func (m *ModelInfo) ContextTotal(ctx int) int64 {
	var t int64
	for _, b := range m.ContextBytes(ctx) {
		t += b
	}
	return t
}

// WorkBytes is the engine's working memory on each device beyond weights and cache: mostly
// the scores for every word of the vocabulary for a batch of tokens. Never less than the
// 600 MB measured on small models.
func (m *ModelInfo) WorkBytes() int64 {
	if m.WorkSeen > 0 { // measured on an earlier load: far better than any estimate
		return max(m.WorkSeen+m.WorkSeen/10, perDeviceOverhead)
	}
	w := int64(m.Vocab) * ubatch * 4 * 2
	if w < perDeviceOverhead {
		return perDeviceOverhead
	}
	return w
}

// isExpertTensor recognises the expert weights of a mixture-of-experts layer by name
// (blk.N.ffn_up_exps.weight and its siblings).
func isExpertTensor(name string) bool {
	return strings.Contains(name, ".ffn_") && strings.Contains(name, "exps.")
}

// HasExperts says whether this is a mixture-of-experts model.
func (m *ModelInfo) HasExperts() bool {
	for _, b := range m.ExpertBytes {
		if b > 0 {
			return true
		}
	}
	return false
}

// trunkBytes is the part of a layer that is not expert weights.
func (m *ModelInfo) trunkBytes(l int) int64 {
	if l < len(m.ExpertBytes) {
		return m.LayerBytes[l] - m.ExpertBytes[l]
	}
	return m.LayerBytes[l]
}

// denseBytes is the part of the model every word uses: everything but the expert weights.
func (m *ModelInfo) denseBytes() int64 {
	d := m.TotalBytes()
	for _, e := range m.ExpertBytes {
		d -= e
	}
	return d
}

func (m *ModelInfo) expertBytes(l int) int64 {
	if l < len(m.ExpertBytes) {
		return m.ExpertBytes[l]
	}
	return 0
}

type ggufReader struct {
	b []byte
	o int
}

var errShort = errors.New("need more header bytes")

func (r *ggufReader) need(n int) error {
	if n < 0 || r.o+n > len(r.b) {
		return errShort
	}
	return nil
}
func (r *ggufReader) u32() (uint32, error) {
	if err := r.need(4); err != nil {
		return 0, err
	}
	v := binary.LittleEndian.Uint32(r.b[r.o:])
	r.o += 4
	return v, nil
}
func (r *ggufReader) u64() (uint64, error) {
	if err := r.need(8); err != nil {
		return 0, err
	}
	v := binary.LittleEndian.Uint64(r.b[r.o:])
	r.o += 8
	return v, nil
}
func (r *ggufReader) str() (string, error) {
	n, err := r.u64()
	if err != nil {
		return "", err
	}
	if n > 1<<30 {
		return "", errors.New("corrupt GGUF string")
	}
	if err := r.need(int(n)); err != nil {
		return "", err
	}
	s := string(r.b[r.o : r.o+int(n)])
	r.o += int(n)
	return s, nil
}

var ggufScalarSize = map[uint32]int{0: 1, 1: 1, 2: 2, 3: 2, 4: 4, 5: 4, 6: 4, 7: 1, 10: 8, 11: 8, 12: 8}

// value reads one metadata value. Strings and small integers are returned; everything else is skipped.
func (r *ggufReader) value(t uint32) (any, error) {
	if sz, ok := ggufScalarSize[t]; ok {
		if err := r.need(sz); err != nil {
			return nil, err
		}
		var v uint64
		for i := 0; i < sz; i++ {
			v |= uint64(r.b[r.o+i]) << (8 * i)
		}
		r.o += sz
		if t == 6 || t == 12 {
			return nil, nil // floats are not needed
		}
		return v, nil
	}
	switch t {
	case 8:
		return r.str()
	case 9:
		et, err := r.u32()
		if err != nil {
			return nil, err
		}
		n, err := r.u64()
		if err != nil {
			return nil, err
		}
		if sz, ok := ggufScalarSize[et]; ok {
			if n > 1<<40 {
				return nil, errors.New("corrupt GGUF array")
			}
			if err := r.need(int(n) * sz); err != nil {
				return nil, err
			}
			// Short arrays of whole numbers or yes/no values describe the layers (head counts per
			// layer, which layers use a sliding window): keep them. Floats and the tokenizer's
			// long lists are skipped.
			if n <= 4096 && et != 6 && et != 12 {
				vals := make([]uint64, n)
				for i := range vals {
					for j := 0; j < sz; j++ {
						vals[i] |= uint64(r.b[r.o+i*sz+j]) << (8 * j)
					}
				}
				r.o += int(n) * sz
				return vals, nil
			}
			r.o += int(n) * sz
			return nil, nil
		}
		for i := uint64(0); i < n; i++ {
			if _, err := r.value(et); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}
	return nil, fmt.Errorf("unknown GGUF value type %d", t)
}

// parseGGUF reads a GGUF header from the first bytes of a file. It returns errShort when buf
// does not hold the whole header yet.
func parseGGUF(buf []byte, fileSize int64) (*ModelInfo, error) {
	return parseGGUFPart(buf, fileSize, 0)
}

// parseGGUFPart also reads the later files of a model that is split across several files.
// Those carry tensors but not the model's description, so the layer count is passed in.
func parseGGUFPart(buf []byte, fileSize int64, layersHint int) (*ModelInfo, error) {
	if len(buf) < 24 {
		return nil, errShort
	}
	if string(buf[:4]) != "GGUF" {
		return nil, errors.New("not a GGUF model file")
	}
	r := &ggufReader{b: buf, o: 8}
	nTensors, err := r.u64()
	if err != nil {
		return nil, err
	}
	nKV, err := r.u64()
	if err != nil {
		return nil, err
	}
	if nTensors > 1<<24 || nKV > 1<<24 {
		return nil, errors.New("corrupt GGUF header")
	}
	kv := map[string]any{}
	for i := uint64(0); i < nKV; i++ {
		k, err := r.str()
		if err != nil {
			return nil, err
		}
		t, err := r.u32()
		if err != nil {
			return nil, err
		}
		v, err := r.value(t)
		if err != nil {
			return nil, err
		}
		if v != nil {
			kv[k] = v
		}
	}
	type raw struct {
		name string
		n    uint64
		typ  uint32
		rel  uint64
	}
	raws := make([]raw, 0, nTensors)
	for i := uint64(0); i < nTensors; i++ {
		name, err := r.str()
		if err != nil {
			return nil, err
		}
		nd, err := r.u32()
		if err != nil {
			return nil, err
		}
		if nd > 8 {
			return nil, errors.New("corrupt GGUF tensor")
		}
		n := uint64(1)
		for d := uint32(0); d < nd; d++ {
			dim, err := r.u64()
			if err != nil {
				return nil, err
			}
			n *= dim
		}
		typ, err := r.u32()
		if err != nil {
			return nil, err
		}
		rel, err := r.u64()
		if err != nil {
			return nil, err
		}
		raws = append(raws, raw{name, n, typ, rel})
	}
	align := uint64(32)
	if a, ok := kv["general.alignment"].(uint64); ok && a > 0 {
		align = a
	}
	base := (uint64(r.o) + align - 1) / align * align
	sort.Slice(raws, func(i, j int) bool { return raws[i].rel < raws[j].rel })

	m := &ModelInfo{FileSize: fileSize}
	m.Name, _ = kv["general.name"].(string)
	m.Arch, _ = kv["general.architecture"].(string)
	if v, ok := kv[m.Arch+".block_count"].(uint64); ok {
		m.Layers = int(v)
	}
	if v, ok := kv[m.Arch+".context_length"].(uint64); ok {
		m.CtxTrain = int(v)
	}
	if v, ok := kv["general.file_type"].(uint64); ok {
		m.FileType = int(v)
	}
	ints := func(key string) []int {
		switch v := kv[m.Arch+"."+key].(type) {
		case uint64:
			return []int{int(v)}
		case []uint64:
			out := make([]int, len(v))
			for i, x := range v {
				out[i] = int(x)
			}
			return out
		}
		return nil
	}
	one := func(key string) int {
		if v := ints(key); len(v) > 0 {
			return v[0]
		}
		return 0
	}
	m.KVHeads = ints("attention.head_count_kv")
	if len(m.KVHeads) == 0 {
		m.KVHeads = ints("attention.head_count")
	}
	m.KeyLen, m.ValLen = one("attention.key_length"), one("attention.value_length")
	if m.KeyLen == 0 {
		if heads, embd := one("attention.head_count"), one("embedding_length"); heads > 0 {
			m.KeyLen, m.ValLen = embd/heads, embd/heads
		}
	}
	m.SWAWindow = one("attention.sliding_window")
	if pat := ints("attention.sliding_window_pattern"); m.SWAWindow > 0 && m.Layers > 0 {
		m.SWALayers = make([]bool, m.Layers)
		for i := range m.SWALayers {
			switch {
			case len(pat) == m.Layers:
				m.SWALayers[i] = pat[i] != 0
			case len(pat) == 1 && pat[0] > 0:
				m.SWALayers[i] = (i+1)%pat[0] != 0 // every n-th layer sees everything
			}
		}
	}
	m.LoopStart, m.LoopEnd, m.LoopCount = one("loop.start"), one("loop.end"), one("loop.count")
	m.Vocab = one("vocab_size")
	for _, t := range raws {
		m.Params += t.n
	}
	if m.Layers <= 0 {
		m.Layers = layersHint
	}
	if m.Layers <= 0 {
		return nil, errors.New("model header has no layer count")
	}
	m.LayerBytes = make([]int64, m.Layers)
	m.ExpertBytes = make([]int64, m.Layers)
	for i, t := range raws {
		ten := Tensor{Name: t.name, Layer: -1, Off: int64(base + t.rel)}
		var gap uint64
		if i+1 < len(raws) {
			gap = raws[i+1].rel - t.rel
		} else if fileSize > 0 {
			gap = uint64(fileSize) - (base + t.rel)
		}
		if bt, ok := ggmlTypes[t.typ]; ok {
			size := t.n * bt[1] / bt[0]
			ten.Size = int64(size)
			ten.OK = gap == 0 || (size <= gap && gap-size < align)
		} else {
			ten.Size = int64(gap) // unknown type: good enough for planning, not for cache seeding
		}
		if strings.HasPrefix(t.name, "blk.") {
			if p := strings.SplitN(t.name, ".", 3); len(p) == 3 {
				if l, err := strconv.Atoi(p[1]); err == nil && l >= 0 && l < m.Layers {
					ten.Layer = l
				}
			}
		}
		if ten.Layer >= 0 {
			m.LayerBytes[ten.Layer] += ten.Size
			if isExpertTensor(t.name) {
				m.ExpertBytes[ten.Layer] += ten.Size
			}
		} else {
			m.OtherBytes += ten.Size
			if t.name == "output.weight" {
				m.OutputBytes += ten.Size
			}
		}
		m.Tensors = append(m.Tensors, ten)
	}
	return m, nil
}

// TotalBytes is the size of all tensor data.
func (m *ModelInfo) TotalBytes() int64 {
	t := m.OtherBytes
	for _, b := range m.LayerBytes {
		t += b
	}
	return t
}

// ---------- planning ----------

// PlanNode is one computer's offer to the planner.
type PlanNode struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	CPUBytes int64   `json:"cpuBytes"` // RAM offered for layers
	GPUBytes int64   `json:"gpuBytes"` // video memory offered (head only, or unified memory)
	Unified  bool    `json:"unified"`  // GPU shares RAM: all local layers go to the GPU
	TwoDev   bool    `json:"twoDev"`   // as a helper it offers two pools: its graphics card, then its RAM
	HasGPU   bool    `json:"hasGPU"`   // the engine on this computer lists a GPU device, even when its memory is full
	Cores    int     `json:"cores"`
	RAMTotal int64   `json:"ramTotal"`
	LinkMs   float64 `json:"linkMs"` // typical round trip to this computer, 0 when not measured
	// For streaming from disk (InferSSD): free space where the models go, and which package
	// suits this computer ("" with the reason when none does).
	DiskFree      int64  `json:"diskFree"`
	StreamVariant string `json:"streamVariant,omitempty"`
	StreamWhy     string `json:"streamWhy,omitempty"`
}

// PlanShare is what one computer ends up holding.
type PlanShare struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Role      string `json:"role"` // head | worker
	CPULayers int    `json:"cpuLayers"`
	GPULayers int    `json:"gpuLayers"`
	Bytes     int64  `json:"bytes"`
	Offered   int64  `json:"offered"`
	First     int    `json:"first"` // first layer index held (worker / GPU part)
}

// Plan is a complete layout of a model across computers.
type Plan struct {
	Fits        bool        `json:"fits"`
	KVKeyQ8     bool        `json:"kvKeyQ8,omitempty"` // fits only with the cache's keys in 8 bits
	Reason      string      `json:"reason,omitempty"`
	Head        string      `json:"head"`
	Shares      []PlanShare `json:"shares"`
	NGL         int         `json:"ngl"`
	TensorSplit string      `json:"tensorSplit"`
	WorkerOrder []string    `json:"workerOrder"`
	DevOwner    []string    `json:"devOwner"`  // which computer each device in the split belongs to
	DevLayers   []int       `json:"devLayers"` // layers on each of those devices
	// CardTrunk: the head's graphics card holds the part of its layers that is read for every
	// word, and the expert weights of those layers stay in the head's memory. ExpertCPU lists
	// those layers.
	CardTrunk  bool  `json:"cardTrunk,omitempty"`
	ExpertCPU  []int `json:"expertCPU,omitempty"`
	Solo       bool  `json:"solo"`     // one computer alone, letting its disk stand in for missing memory
	Stream     bool  `json:"stream"`   // that computer runs InferSSD, streaming experts from its disk
	Overflow   int64 `json:"overflow"` // bytes of the model that do not fit in that computer's memory
	ModelBytes int64 `json:"modelBytes"`
	OfferedSum int64 `json:"offeredSum"`
	BiggestOne int64 `json:"biggestOne"` // most any single computer offers
	NeedsPool  bool  `json:"needsPool"`  // too big for any one computer here
	ShortBy    int64 `json:"shortBy"`    // bytes missing when it does not fit
	Machines   int   `json:"machines"`   // computers that hold part of the model
	Layers     int   `json:"layers"`
	LayerBytes int64 `json:"layerBytes"` // largest layer, with its share of the context cache
	// ContextBytes is the context cache for the planned context size, on top of ModelBytes.
	ContextBytes int64 `json:"contextBytes,omitempty"`
}

// overhead kept free on every device for the context cache and working buffers.
const perDeviceOverhead = 600 << 20

// trunkOverhead is the same allowance for a graphics card that holds only the trunk of its
// layers. Measured on a 4 GB card with 36 layers' trunks: 762 MiB beyond the weights, of
// which the offer has already set 500 MB aside.
const trunkOverhead = 500 << 20

// planLayout spreads the layers over the computers.
//
// llama.cpp's rule: with -ngl N the first (layers+1-N) layers stay on the head's CPU, the
// remaining N (the output layer counts as one) are shared between "devices" in proportion
// to --tensor-split; the devices are the workers in --rpc order followed by the head's GPU.
// So the order of layers through the machines is: head CPU, worker 1, worker 2, ..., head GPU.
//
// Measured on a real cluster (November 2026): every extra helper in that chain costs two
// network hand-offs per word, and all the computers needed about the same time per layer. So
// the plan keeps as much as it can on the head and brings in as few helpers as possible.
func planLayout(m *ModelInfo, nodes []PlanNode, headID string) *Plan {
	p := planLayoutAll(m, nodes, headID)
	// Looped models: each repeat pass runs with the weights of its layer but keeps its
	// context cache on the last device (its slot comes after every file layer). Unless the
	// whole loop core sits on that last device, the engine finds a layer's attention on
	// another device than its cache, turns flash attention off for the whole model, and its
	// working memory grows many times over (measured: 14.3 GB on a 4 GB card at 32k).
	if m.loopSlots() > 0 && p.Fits && len(p.DevLayers) > 0 {
		layer := p.LayerBytes
		if first := m.Layers - p.DevLayers[len(p.DevLayers)-1]; first > m.LoopStart {
			p.Fits = false
			p.ShortBy = int64(first-m.LoopStart) * layer
			p.Reason = fmt.Sprintf("This model repeats its layers %d to %d, and they all have to run on one computer: it needs room for %d more of its layers on the computer running it. Try a smaller size of this model, or a shorter conversation memory.", m.LoopStart, m.LoopEnd-1, first-m.LoopStart)
		}
	}
	return p
}

func planLayoutAll(m *ModelInfo, nodes []PlanNode, headID string) *Plan {
	p := &Plan{ModelBytes: m.TotalBytes(), Layers: m.Layers}
	// Each layer brings its share of the context cache to whichever computer holds it, so a
	// layer's cost is its weights plus that cache at the context size being planned for.
	kv := m.ContextBytes(m.PlanCtx)
	p.ContextBytes = m.ContextTotal(m.PlanCtx)
	var layer int64
	for i, b := range m.LayerBytes {
		if i < len(kv) {
			b += kv[i]
		}
		if b > layer {
			layer = b
		}
	}
	p.LayerBytes = layer
	perDeviceOverhead := m.WorkBytes()
	if len(nodes) == 0 {
		p.Reason = "No computers are available right now."
		return p
	}
	// As the head a computer can use its RAM and its GPU. As a worker it offers one pool:
	// its RAM (or, with unified memory, the GPU pool that shares that RAM).
	cap := func(n PlanNode) int64 {
		if n.Unified {
			return n.GPUBytes
		}
		return n.CPUBytes + n.GPUBytes
	}
	capWorker := func(n PlanNode) int64 {
		if n.Unified {
			return n.GPUBytes
		}
		if n.TwoDev {
			return n.CPUBytes + n.GPUBytes
		}
		return n.CPUBytes
	}
	for _, n := range nodes {
		if cap(n) > p.BiggestOne {
			p.BiggestOne = cap(n)
		}
	}
	need := p.ModelBytes + p.ContextBytes + perDeviceOverhead
	p.NeedsPool = need > p.BiggestOne

	// The head holds the model file and talks to everyone: pick the computer offering the most.
	head := nodes[0]
	for _, n := range nodes {
		if headID != "" && n.ID == headID {
			head = n
			break
		}
		if headID == "" && cap(n) > cap(head) {
			head = n
		}
	}
	p.Head = head.ID
	var workers []PlanNode
	for _, n := range nodes {
		if n.ID != head.ID {
			workers = append(workers, n)
		}
	}
	sort.SliceStable(workers, func(i, j int) bool { return capWorker(workers[i]) > capWorker(workers[j]) })
	p.OfferedSum = cap(head)
	for _, w := range workers {
		p.OfferedSum += capWorker(w)
	}

	layersIn := func(bytes, extra int64) int {
		bytes -= perDeviceOverhead + extra
		if bytes < layer || layer == 0 {
			return 0
		}
		return int(bytes / layer)
	}
	// Tensors outside the layers: the token embeddings always stay in the head's RAM and the
	// output layer rides on the last device. Each is roughly half of OtherBytes.
	edge := m.OtherBytes * 6 / 10

	// How many layers a helper can take. A helper with a usable graphics card offers two
	// pools: the card (first) and its RAM.
	wcapGPU := func(w PlanNode, workerEdge int64) int {
		if w.TwoDev {
			return layersIn(w.GPUBytes, workerEdge)
		}
		return 0
	}
	wcap := func(w PlanNode, workerEdge int64) int {
		if w.Unified {
			return layersIn(w.GPUBytes, workerEdge)
		}
		return wcapGPU(w, workerEdge) + layersIn(w.CPUBytes, workerEdge)
	}
	// assign hands "left" layers to helpers, taking them in the given order and stopping as
	// soon as every layer has a home. It reports the layers per helper and how many are left.
	assign := func(order []int, left int, workerEdge int64) ([]int, int) {
		wl := make([]int, len(workers))
		for _, i := range order {
			if left == 0 {
				break
			}
			n := wcap(workers[i], workerEdge)
			if n > left {
				n = left
			}
			wl[i] = n
			left -= n
		}
		return wl, left
	}
	used := func(wl []int) int {
		n := 0
		for _, l := range wl {
			if l > 0 {
				n++
			}
		}
		return n
	}
	// Which helpers to use. Measured on the real cluster: one helper was faster than two
	// whenever one was enough (3.6 to 4.1 words/s against 3.3), because every helper in the
	// chain adds two network hand-offs per word. So: the fewest helpers; when two choices
	// need equally many, the better-connected helpers.
	bySize := make([]int, len(workers))
	for i := range bySize {
		bySize[i] = i
	}
	byLink := append([]int(nil), bySize...)
	sort.SliceStable(byLink, func(a, b int) bool {
		la, lb := workers[byLink[a]].LinkMs, workers[byLink[b]].LinkMs
		if la <= 0 || lb <= 0 {
			return false // not timed yet: keep the size order
		}
		return la < lb
	})
	pick := func(left int, workerEdge int64) ([]int, int) {
		wlL, leftL := assign(byLink, left, workerEdge)
		wlS, leftS := assign(bySize, left, workerEdge)
		if leftL == 0 && (leftS > 0 || used(wlL) <= used(wlS)) {
			return wlL, leftL
		}
		return wlS, leftS
	}

	// finish turns layer counts into the engine's settings: device list, -ngl, --tensor-split.
	finish := func(headCPU, headGPU int, headBytes int64, wl []int) {
		type dev struct {
			layers int
			owner  string
		}
		var devs []dev
		first := headCPU
		p.Shares = append(p.Shares, PlanShare{ID: head.ID, Name: head.Name, Role: "head", CPULayers: headCPU, GPULayers: headGPU,
			Bytes: headBytes, Offered: cap(head)})
		workerEdge := edge
		if headGPU > 0 {
			workerEdge = 0
		}
		for i, w := range workers {
			if wl[i] == 0 {
				continue
			}
			p.WorkerOrder = append(p.WorkerOrder, w.ID)
			g := 0
			if w.TwoDev {
				if g = wcapGPU(w, workerEdge); g > wl[i] {
					g = wl[i]
				}
				devs = append(devs, dev{g, w.ID}) // listed even when empty: the helper's engine always offers it
			}
			devs = append(devs, dev{wl[i] - g, w.ID})
			p.Shares = append(p.Shares, PlanShare{ID: w.ID, Name: w.Name, Role: "worker", CPULayers: wl[i] - g, GPULayers: g,
				Bytes: int64(wl[i]) * layer, Offered: capWorker(w), First: first})
			first += wl[i]
		}
		if head.GPUBytes > 0 || head.HasGPU {
			devs = append(devs, dev{headGPU, head.ID}) // listed even when empty: llama.cpp always appends the local GPU
			p.Shares[0].First = first
		}
		for _, d := range devs {
			p.DevOwner = append(p.DevOwner, d.owner)
			p.DevLayers = append(p.DevLayers, d.layers)
		}
		// The output layer counts as one more "layer" and rides on the last device that holds anything.
		offloaded := 0
		for _, d := range devs {
			offloaded += d.layers
		}
		if offloaded > 0 {
			firstDev, lastDev := -1, -1
			for i, d := range devs {
				if d.layers > 0 {
					if firstDev < 0 {
						firstDev = i
					}
					lastDev = i
				}
			}
			// Looped models (Inkling 2): the engine gives every pass of the loop a slot of its own
			// (layers 6-17 three times: 48 slots plus the output, for 24 layers in the file), places
			// a file layer's weights with the slot of the same number, and splits all the slots by
			// the ratio. So the ratio and the count are given in slots: the file layers land on the
			// devices planned, and the repeat slots (their context cache only) go with the last one.
			extra := m.loopSlots()
			p.NGL = offloaded + 1 + extra
			// Split values are layer counts, nudged by half a layer at the two ends so that every
			// boundary falls halfway between two layers and rounding can never move a layer.
			parts := make([]string, len(devs))
			for i, d := range devs {
				v := float64(d.layers)
				if i == lastDev {
					v += 1 + float64(extra) // the output layer, and a looped model's repeat slots
				}
				if firstDev != lastDev {
					if i == firstDev {
						v -= 0.5
					}
					if i == lastDev {
						v += 0.5
					}
				}
				parts[i] = strconv.FormatFloat(v, 'f', -1, 64)
			}
			p.TensorSplit = strings.Join(parts, ",")
		}
		for _, s := range p.Shares {
			if s.CPULayers+s.GPULayers > 0 {
				p.Machines++
			}
		}
		p.Fits = true
	}

	// A. Mixture-of-experts model and a graphics card on the head that cannot hold whole
	// layers for the entire model: let the card hold the trunk of the head's layers (read for
	// every word) and keep their experts (a few read per word) in the head's memory.
	// Measured: 3.8 words/s against 1.2 to 2.1 for whole layers spread by memory.
	if m.HasExperts() && !head.Unified && head.GPUBytes > 0 && layersIn(head.GPUBytes, edge) < m.Layers {
		if t := planTrunk(m, head); t.card > 0 {
			// Whole layers in the head's spare memory go first in the chain. They are the
			// slowest place for a layer, so use them only when that spares a helper, and then
			// as few as possible.
			best, front, left := -1, 0, 1
			var wl []int
			for f := 0; f <= t.front; f++ {
				w2, l2 := pick(m.Layers-t.card-f, 0)
				if l2 == 0 && (best < 0 || used(w2) < best) {
					best, front, wl, left = used(w2), f, w2, 0
				}
			}
			if left == 0 {
				p.CardTrunk = true
				firstCard := m.Layers - t.card
				var bytes int64
				for l := 0; l < front; l++ {
					bytes += m.LayerBytes[l]
				}
				for l := firstCard; l < m.Layers; l++ {
					bytes += m.LayerBytes[l]
					if l < m.Layers-t.whole {
						p.ExpertCPU = append(p.ExpertCPU, l)
					}
				}
				finish(front, t.card, bytes, wl)
				return p
			}
		}
	}

	// B. Whole layers: the head's card, then the head's memory, then helpers.
	left := m.Layers
	take := func(n int) int {
		if n > left {
			n = left
		}
		left -= n
		return n
	}
	headGPU, headCPU := 0, 0
	if head.GPUBytes > 0 {
		headGPU = take(layersIn(head.GPUBytes, edge))
	}
	workerEdge := edge // without a GPU on the head, the last helper carries the output layer
	if headGPU > 0 {
		workerEdge = 0
	}
	if !head.Unified {
		headCPU = take(layersIn(head.CPUBytes, edge))
	}
	wl, left := pick(left, workerEdge)
	if left > 0 {
		p.ShortBy = int64(left)*layer + perDeviceOverhead
		p.Reason = fmt.Sprintf("This model needs about %s more memory than your computers offer right now.", humanBytes(p.ShortBy))
		return p
	}
	finish(headCPU, headGPU, int64(headCPU+headGPU)*layer, wl)
	return p
}

// trunkFit is how a head with a graphics card can hold layers of a mixture-of-experts model.
type trunkFit struct {
	card  int // last layers whose trunk is on the card
	whole int // of those, the last layers that are on the card entirely, experts included
	front int // whole layers that still fit in memory beside the experts
}

// planTrunk sizes the head's part of a mixture-of-experts model: as many layers as possible
// with their trunk on the card and their experts in memory.
func planTrunk(m *ModelInfo, head PlanNode) trunkFit { return planTrunkOpt(m, head, true) }

// planTrunkOpt is planTrunk; with ramBound false the experts may exceed the head's memory
// (one computer alone, its disk covering the difference).
func planTrunkOpt(m *ModelInfo, head PlanNode, ramBound bool) trunkFit {
	var t trunkFit
	card := head.GPUBytes - trunkOverhead - m.OutputBytes
	ram := head.CPUBytes - perDeviceOverhead - (m.OtherBytes - m.OutputBytes)
	for l := m.Layers - 1; l >= 0; l-- {
		if m.trunkBytes(l) > card || (ramBound && m.expertBytes(l) > ram) {
			break
		}
		card -= m.trunkBytes(l)
		ram -= m.expertBytes(l)
		t.card++
	}
	// Card memory left over: move the experts of the last layers onto it too.
	for l := m.Layers - 1; l >= m.Layers-t.card; l-- {
		if m.expertBytes(l) > card {
			break
		}
		card -= m.expertBytes(l)
		ram += m.expertBytes(l)
		t.whole++
	}
	for l := 0; l < m.Layers-t.card; l++ {
		if m.LayerBytes[l] > ram {
			break
		}
		ram -= m.LayerBytes[l]
		t.front++
	}
	return t
}

// planSolo lays a model out on one computer alone, even when it is bigger than that
// computer's memory. The engine maps the model file instead of loading it, so the operating
// system keeps as much of it in memory as fits and reads the rest from disk when needed.
// Measured on a 30 GB PC with a SATA SSD and a 34 GB mixture-of-experts model: about as fast
// as pooling three computers over Wi-Fi. It degrades quickly as the overflow grows, on dense
// models, and on spinning disks.
func planSolo(m *ModelInfo, n PlanNode) *Plan {
	p := &Plan{ModelBytes: m.TotalBytes(), Layers: m.Layers, Head: n.ID, Solo: true}
	kv := m.ContextBytes(m.PlanCtx)
	p.ContextBytes = m.ContextTotal(m.PlanCtx)
	for i, b := range m.LayerBytes {
		if i < len(kv) {
			b += kv[i]
		}
		if b > p.LayerBytes {
			p.LayerBytes = b
		}
	}
	layer := p.LayerBytes
	perDeviceOverhead := m.WorkBytes()
	edge := m.OtherBytes * 6 / 10
	gpu := 0
	if n.GPUBytes > perDeviceOverhead+edge+layer && layer > 0 {
		gpu = int((n.GPUBytes - perDeviceOverhead - edge) / layer)
	}
	if gpu > m.Layers {
		gpu = m.Layers
	}
	cpu := m.Layers - gpu
	if n.Unified {
		gpu, cpu = m.Layers, 0 // everything is one pool on these machines
	}
	inMemory := n.CPUBytes
	if n.Unified {
		inMemory = n.GPUBytes
	}
	need := m.OtherBytes + perDeviceOverhead
	for l := 0; l < cpu && l < m.Layers; l++ { // each layer's own size and cache, not the largest one's
		need += m.LayerBytes[l]
		if l < len(kv) {
			need += kv[l]
		}
	}
	if n.Unified {
		need = p.ModelBytes + p.ContextBytes + perDeviceOverhead
	}
	// Mixture-of-experts model on a card too small for whole layers: the card holds the trunk
	// of as many layers as it can, their experts stay in memory (or on disk). Measured on a
	// 4 GB card: 2.1 to 2.55 words/s against 1.5 to 2.3 with whole layers.
	if m.HasExperts() && !n.Unified && n.GPUBytes > 0 && gpu < m.Layers {
		if t := planTrunkOpt(m, n, false); t.card > gpu {
			gpu, cpu = t.card, m.Layers-t.card
			p.CardTrunk = true
			need = m.OtherBytes - m.OutputBytes + perDeviceOverhead
			for l := 0; l < m.Layers; l++ {
				switch {
				case l < cpu:
					need += m.LayerBytes[l]
				case l < m.Layers-t.whole:
					need += m.expertBytes(l)
					p.ExpertCPU = append(p.ExpertCPU, l)
				}
				if l < len(kv) && l < cpu {
					need += kv[l] // the context cache of layers kept in memory
				}
			}
		}
	}
	// Looped models: the card's layers must include the whole loop core, or the repeat passes
	// (their slots come last, on the card) run apart from their weights and the engine turns
	// flash attention off. Otherwise keep the model on the processor.
	if extra := m.loopSlots(); extra > 0 && gpu > 0 && m.Layers-gpu > m.LoopStart {
		gpu, cpu = 0, m.Layers
		p.CardTrunk, p.ExpertCPU = false, nil
		need = m.OtherBytes + perDeviceOverhead
		for l := 0; l < m.Layers; l++ {
			need += m.LayerBytes[l]
			if l < len(kv) {
				need += kv[l]
			}
		}
	}
	if need > inMemory {
		p.Overflow = need - inMemory
	}
	p.OfferedSum, p.BiggestOne = inMemory+n.GPUBytes, inMemory+n.GPUBytes
	// Past roughly twice its memory a computer spends nearly all its time reading the disk.
	if n.RAMTotal > 0 && p.ModelBytes > 2*n.RAMTotal {
		p.Reason = fmt.Sprintf("%s has %s of memory and this model is %s. Reading that much from disk for every word would be unusably slow.", n.Name, humanBytes(n.RAMTotal), humanBytes(p.ModelBytes))
		return p
	}
	p.Shares = []PlanShare{{ID: n.ID, Name: n.Name, Role: "head", CPULayers: cpu, GPULayers: gpu, Bytes: p.ModelBytes, Offered: inMemory + n.GPUBytes}}
	if n.GPUBytes > 0 || n.HasGPU {
		p.DevOwner, p.DevLayers = []string{n.ID}, []int{gpu}
	}
	if gpu > 0 {
		p.NGL = gpu + 1 + m.loopSlots() // in the engine's slots (see planLayout)
	}
	p.Machines, p.Fits = 1, true
	return p
}

// deviceOfLayer mirrors llama.cpp: which device index (in --tensor-split order) holds layer il,
// or -1 for the head's CPU.
// loopSlots is how many slots a looped model adds beyond its file layers (0 for others).
func (m *ModelInfo) loopSlots() int {
	if m.LoopCount > 1 && m.LoopEnd > m.LoopStart {
		return (m.LoopCount - 1) * (m.LoopEnd - m.LoopStart)
	}
	return 0
}

// EngineLayers is how many layers the engine counts: the file's, plus a looped model's repeat slots.
func (m *ModelInfo) EngineLayers() int { return m.Layers + m.loopSlots() }

func deviceOfLayer(il, nLayers, ngl int, split []float64) int {
	start := nLayers + 1 - ngl
	if start < 0 {
		start = 0
	}
	if il < start || ngl <= 0 {
		return -1
	}
	act := ngl
	if act > nLayers+1 {
		act = nLayers + 1
	}
	var sum float64
	for _, s := range split {
		sum += s
	}
	frac := float32(il-start) / float32(act)
	var acc float64
	for i, s := range split {
		acc += s
		if float32(acc/sum) > frac {
			return i
		}
	}
	return len(split) - 1
}

func parseSplit(s string) []float64 {
	var out []float64
	for _, p := range strings.Split(s, ",") {
		if v, err := strconv.ParseFloat(strings.TrimSpace(p), 64); err == nil {
			out = append(out, v)
		}
	}
	return out
}

func humanBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(b)/float64(1<<20))
	}
	return fmt.Sprintf("%d bytes", b)
}

// mergeParts combines the headers of a model that comes as several files into one picture.
func mergeParts(parts []*ModelInfo) *ModelInfo {
	m := *parts[0]
	m.LayerBytes = append([]int64(nil), parts[0].LayerBytes...)
	m.ExpertBytes = append([]int64(nil), parts[0].ExpertBytes...)
	m.Tensors = append([]Tensor(nil), parts[0].Tensors...)
	for i, p := range parts[1:] {
		for l, b := range p.LayerBytes {
			if l < len(m.LayerBytes) {
				m.LayerBytes[l] += b
			}
		}
		for l, b := range p.ExpertBytes {
			if l < len(m.ExpertBytes) {
				m.ExpertBytes[l] += b
			}
		}
		m.OtherBytes += p.OtherBytes
		m.OutputBytes += p.OutputBytes
		m.FileSize += p.FileSize
		m.Params += p.Params
		for _, t := range p.Tensors {
			t.Part = i + 1
			m.Tensors = append(m.Tensors, t)
		}
	}
	return &m
}
