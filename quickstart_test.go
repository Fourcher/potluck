package main

import "testing"

func TestPickQuick(t *testing.T) {
	files := []StoreFile{
		{File: "m-Q8_0.gguf", Quant: "Q8_0", Size: 28, Verdict: "pool"},
		{File: "m-Q6_K.gguf", Quant: "Q6_K", Size: 23, Verdict: "one"},
		{File: "m-Q4_K_M.gguf", Quant: "Q4_K_M", Size: 16, Verdict: "one"},
		{File: "m-IQ2_M.gguf", Quant: "IQ2_M", Size: 9, Verdict: "one"},
	}
	if f, ok := pickQuick(files); !ok || f.Quant != "Q4_K_M" {
		t.Fatalf("want Q4_K_M, got %v %v", f.Quant, ok)
	}
	// A small computer: only the smallest fits alone.
	small := []StoreFile{{Quant: "Q4_K_M", Size: 16, Verdict: "pool"}, {Quant: "IQ2_M", Size: 9, Verdict: "one"}}
	if f, _ := pickQuick(small); f.Quant != "IQ2_M" {
		t.Fatalf("want IQ2_M, got %v", f.Quant)
	}
	// Nothing fits alone: the smallest pooled size.
	pooled := []StoreFile{{Quant: "Q8_0", Size: 28, Verdict: "pool"}, {Quant: "Q6_K", Size: 23, Verdict: "pool"}, {Quant: "BF16", Size: 50, Verdict: "no"}}
	if f, _ := pickQuick(pooled); f.Quant != "Q6_K" {
		t.Fatalf("want Q6_K, got %v", f.Quant)
	}
	if _, ok := pickQuick([]StoreFile{{Quant: "BF16", Verdict: "no"}}); ok {
		t.Fatal("nothing fits, want false")
	}
}
