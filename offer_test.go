package main

import "testing"

// A Mac's plan never puts more on the graphics chip than macOS lets it hold.
func TestOfferMetalLimit(t *testing.T) {
	const gib = int64(1 << 30)
	ram := uint64(16 * gib)
	limit := uint64(12124 << 20) // a 16 GB M4
	info := NodeInfo{RAMTotal: ram, RAMAvail: ram * 9 / 10, Limits: Limits{Pedal: 100, Enabled: true},
		GPUs: []GPU{{Unified: true, Usable: true, Backend: "metal", VRAMTotal: limit, VRAMFree: limit}}}
	_, gpu, uni := offer(info, 0)
	if !uni || gpu > int64(limit)-(500<<20) || gpu <= 0 {
		t.Fatalf("offered %d MiB on a chip that may hold %d MiB", gpu>>20, limit>>20)
	}
	// With little free memory the RAM, not the limit, is what counts.
	info.RAMAvail = uint64(6 * gib)
	if _, gpu, _ = offer(info, 0); gpu != 6*gib-2*gib {
		t.Fatalf("offer with 6 GiB free: %d MiB", gpu>>20)
	}
	// A separate card is unchanged.
	info.GPUs = []GPU{{Usable: true, Backend: "cuda", VRAMTotal: uint64(8 * gib), VRAMFree: uint64(8 * gib)}}
	if _, gpu, uni = offer(info, 0); uni || gpu != 8*gib-(500<<20) {
		t.Fatalf("card offer %d MiB", gpu>>20)
	}
}
