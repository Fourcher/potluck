package main

import (
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

// The engine's report as llama-fit-params prints it (colours included), Monolith, Inkling 2
// Q6_K with every expert in memory, 32k, batch 512.
const monolithReport = "\x1b[34m0.00.814.100\x1b[0m \x1b[32mI \x1b[0mcommon_memory_breakdown_print: | memory breakdown [MiB]  | total   free     self   model   context   compute    unaccounted |\r\n" +
	"\x1b[34m0.00.814.106\x1b[0m \x1b[32mI \x1b[0mcommon_memory_breakdown_print: |   - CUDA0 (GTX 1050 Ti) |  4095 = 3359 + ( 5634 =  1008 +    1267 +    3358) +       -4898 |\r\n" +
	"\x1b[34m0.00.814.107\x1b[0m \x1b[32mI \x1b[0mcommon_memory_breakdown_print: |   - Host                |                 21559 = 21381 +       0 +     177                |\r\n" +
	"0.00.877.879 I common_params_fit_impl: projected to use 5634 MiB of device memory vs. 3359 MiB of free device memory\n" +
	"0.00.900.000 I common_memory_breakdown_print: |   - CUDA0 (GTX 1050 Ti) |  4095 = 3359 + ( 1 = 1 + 0 + 0) + 0 |\n"

func TestParseMemReport(t *testing.T) {
	r, ok := parseMemReport(monolithReport)
	if !ok || len(r.Devices) != 1 {
		t.Fatalf("got %+v ok=%v", r, ok)
	}
	d := r.Devices[0]
	if d.Name != "CUDA0 (GTX 1050 Ti)" || d.Total != 4095 || d.Free != 3359 || d.Self != 5634 || d.Model != 1008 || d.Context != 1267 || d.Compute != 3358 {
		t.Fatalf("card %+v", d)
	}
	if r.Host.Self != 21559 || r.Host.Model != 21381 || r.Host.Compute != 177 {
		t.Fatalf("host %+v", r.Host)
	}
	if _, ok := parseMemReport("error: invalid argument: --host\n"); ok {
		t.Fatal("no report must not parse")
	}
}

// fakeMonolith answers like the measured card: model 1008 MiB; context and working memory
// grow with the conversation size and the batch (figures measured 2026-11-24).
func fakeMonolith(t *testing.T, calls *[]string) measurer {
	return func(node string, r StartHeadReq) (MeasureRes, error) {
		ub := r.UBatch
		if ub == 0 {
			ub = 512
		}
		ctxMiB := 240 + int64(r.Ctx)/32 // SWA part plus the full-attention layers
		compute := int64(r.Ctx) / 1024 * int64(ub) / 512 * 105
		*calls = append(*calls, strconv.Itoa(r.Ctx)+"@"+strconv.Itoa(ub))
		self := 1008 + ctxMiB + compute
		return MeasureRes{Devices: []DevMem{{Name: "CUDA0", Total: 4095, Free: 3359, Self: self}}}, nil
	}
}

func TestFitStartShrinksBatchThenContext(t *testing.T) {
	var calls []string
	req := StartHeadReq{Ctx: 262144, NGL: 999}
	got := fitStart(fakeMonolith(t, &calls), "m", req, true, 0, nil, nil)
	if !got.Measured {
		t.Fatalf("not measured: %+v", got)
	}
	// 32k needs 1008+1264+840 at batch 128 = 3112 + margin > 3359 free; 16k at 256 fits.
	if got.Ctx != 16384 || got.UBatch != 256 {
		t.Fatalf("got %+v after %v", got, calls)
	}
	if calls[0] != "262144@512" || !slices.Contains(calls, "32768@128") {
		t.Fatalf("order %v", calls)
	}
}

func TestFitStartKeepsChosenSize(t *testing.T) {
	var calls []string
	got := fitStart(fakeMonolith(t, &calls), "m", StartHeadReq{Ctx: 65536}, false, 0, nil, nil)
	// The person chose 64k: only the batch may change, and nothing fits, so it is not loaded.
	if got.Ctx != 65536 || got.UBatch != 0 || len(calls) != 3 || !got.Measured || got.Short <= 0 {
		t.Fatalf("got %+v after %v", got, calls)
	}
}

func TestFitStartVisionGoesToProcessorWhenShort(t *testing.T) {
	var calls []string
	got := fitStart(fakeMonolith(t, &calls), "m", StartHeadReq{Ctx: 8192}, true, 765, nil, nil)
	// 8k at 512: 1008+496+840 = 2344; with the add-on 3109 + margin > 3359.
	if got.Ctx != 8192 || got.UBatch != 0 || !got.VisionCPU {
		t.Fatalf("got %+v", got)
	}
}

func TestHeadArgsHaveNoServerOnlyFlags(t *testing.T) {
	a := &Agent{}
	args := a.headArgs(StartHeadReq{Ctx: 32768, NGL: 999, ExpertCPU: []int{0, 1}, UBatch: 128}, "m.gguf", "/rt/b1-win-cuda-12.4-x64", "CUDA0", nil, 8)
	for _, bad := range []string{"--host", "--port", "--alias", "--mmproj", "-ctxcp", "-cram"} {
		if slices.Contains(args, bad) {
			t.Fatalf("%s in %v", bad, args)
		}
	}
	if i := slices.Index(args, "-ub"); i < 0 || args[i+1] != "128" {
		t.Fatalf("no batch in %v", args)
	}
}

func TestFitStartMovesExpertsBackBeforeShrinkingBatch(t *testing.T) {
	var calls []string
	base := fakeMonolith(t, &calls)
	// The plan put two layers' experts on the card: 1752 MiB more there.
	m := func(node string, r StartHeadReq) (MeasureRes, error) {
		res, err := base(node, r)
		if len(r.ExpertCPU) < 24 {
			res.Devices[0].Self += 1752
		}
		return res, err
	}
	var all, plan []int
	for l := 0; l < 24; l++ {
		all = append(all, l)
		if l < 22 {
			plan = append(plan, l)
		}
	}
	got := fitStart(m, "m", StartHeadReq{Ctx: 16384, ExpertCPU: plan}, false, 0, all, nil)
	if got.Ctx != 16384 || got.UBatch != 256 || len(got.ExpertCPU) != 24 {
		t.Fatalf("got %+v after %v", got, calls)
	}
}

// A pooled start: the helper's engine reports all of its memory as free (16 GB on the MacBook),
// so its share is checked against what Potluck may use there. Figures from a dry run on LLMOS
// with the MacBook as helper (Inkling 2 IQ2_M, 16k): working memory 1772 MiB at batch 512,
// 442 at 128.
func TestFitStartPooledChecksHelperBudget(t *testing.T) {
	m := func(node string, r StartHeadReq) (MeasureRes, error) {
		compute := int64(1772)
		switch r.UBatch {
		case 256:
			compute = 886
		case 128:
			compute = 442
		}
		return MeasureRes{Devices: []DevMem{{Name: "RPC0 (127.0.0.1:5000)", Node: "mbp", Total: 16384, Free: 16384, Self: 8003 + 755 + compute}}}, nil
	}
	req := StartHeadReq{Ctx: 16384, Workers: []string{"mbp"}}
	got := fitStart(m, "llmos", req, false, 0, nil, map[string]int64{"mbp": 10000})
	// 10530 + margin > 10000; 9644 + 384 = 10028 > 10000; 9200 + 384 fits at 128.
	if !got.Measured || got.UBatch != 128 || got.Note != "Fits on every computer with reading prompts 128 words at a time" {
		t.Fatalf("%+v", got)
	}
	if got := fitStart(m, "llmos", req, false, 0, nil, map[string]int64{"mbp": 12000}); got.UBatch != 0 {
		t.Fatalf("room enough, batch must stay: %+v", got)
	}
}

func TestNameHelpers(t *testing.T) {
	r := MeasureRes{Devices: []DevMem{{Name: "CUDA0 (GTX 1050 Ti)"}, {Name: "RPC0 (127.0.0.1:5001)"}, {Name: "RPC1 (127.0.0.1:5002)"}}}
	r.nameHelpers([]string{"127.0.0.1:5001", "127.0.0.1:5002"}, []string{"a", "b"})
	if r.Devices[0].Node != "" || r.Devices[1].Node != "a" || r.Devices[2].Node != "b" {
		t.Fatalf("%+v", r.Devices)
	}
}

func TestFileErrSaysWhatToDo(t *testing.T) {
	cases := map[error]string{
		&os.PathError{Op: "write", Path: "/d/m.gguf.part", Err: syscall.ENOSPC}:   "is full",
		&os.PathError{Op: "truncate", Path: "/d/m.gguf.part", Err: syscall.EFBIG}: "over 4 GB",
		&os.PathError{Op: "open", Path: "/d/m.gguf.part", Err: os.ErrPermission}:  "may not write",
	}
	for in, want := range cases {
		if got := fileErr(in, "/d/m.gguf", 8<<30).Error(); !strings.Contains(got, want) {
			t.Errorf("%v: %q", in, got)
		}
	}
	if e := errors.New("Hugging Face answered 503"); fileErr(e, "/d/m.gguf", 1) != e {
		t.Fatal("a network error must pass through")
	}
}

// What the engine keeps in this computer's own memory counts against what it lends.
func TestCardShortCountsHostMemory(t *testing.T) {
	res := MeasureRes{Host: DevMem{Name: "Host", Self: 20000}}
	if s := cardShortMiB(res, 0, map[string]int64{hostBudget: 24000}); s != 0 {
		t.Fatalf("fits in 24000 MiB, short %d", s)
	}
	if s := cardShortMiB(res, 0, map[string]int64{hostBudget: 18000}); s != 20000-18000 {
		t.Fatalf("short %d", s)
	}
	if s := cardShortMiB(res, 0, nil); s != 0 {
		t.Fatalf("no host budget given, short %d", s)
	}
}
