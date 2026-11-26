package main

import (
	"encoding/binary"
	"errors"
	"testing"
)

// A crafted header must fail cleanly: no crash, no huge allocation.
func TestGGUFCraftedHeaders(t *testing.T) {
	hdr := func(kvs ...[]byte) []byte {
		b := []byte("GGUF")
		b = binary.LittleEndian.AppendUint32(b, 3)
		b = binary.LittleEndian.AppendUint64(b, 1) // tensors
		b = binary.LittleEndian.AppendUint64(b, uint64(len(kvs)))
		for _, kv := range kvs {
			b = append(b, kv...)
		}
		return b
	}
	key := func(k string, typ uint32) []byte {
		b := binary.LittleEndian.AppendUint64(nil, uint64(len(k)))
		b = append(b, k...)
		return binary.LittleEndian.AppendUint32(b, typ)
	}
	// Arrays nested a million deep, 12 bytes a level.
	nest := key("x", 9)
	for i := 0; i < 1<<20; i++ {
		nest = binary.LittleEndian.AppendUint32(nest, 9)
		nest = binary.LittleEndian.AppendUint64(nest, 1)
	}
	b := hdr(nest)
	if _, err := parseGGUF(b, int64(len(b))); err == nil || errors.Is(err, errShort) {
		t.Fatalf("nested arrays: %v", err)
	}
	// A string array claiming 2^37 strings in a small file.
	sa := key("y", 9)
	sa = binary.LittleEndian.AppendUint32(sa, 8)
	sa = binary.LittleEndian.AppendUint64(sa, 1<<37)
	b = hdr(sa)
	if _, err := parseGGUF(b, 1<<30); err == nil || errors.Is(err, errShort) {
		t.Fatalf("huge array: %v (errShort would make the reader ask for ever more of the file)", err)
	}
	// A layer count past any model.
	arch := key("general.architecture", 8)
	arch = binary.LittleEndian.AppendUint64(arch, 5)
	arch = append(arch, "llama"...)
	bc := key("llama.block_count", 10)
	bc = binary.LittleEndian.AppendUint64(bc, 1<<36)
	b = hdr(arch, bc)
	b = append(b, make([]byte, 64)...)
	if _, err := parseGGUF(b, int64(len(b))); err == nil {
		t.Fatal("a 2^36-layer model was accepted")
	}
}
