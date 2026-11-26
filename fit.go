package main

import (
	"fmt"
	"strings"
	"time"
)

// cardMarginMiB is room left free on a graphics card after what the engine says it will
// allocate: the engine also takes small scratch buffers as it runs, and the desktop may
// grow. llama.cpp's own fitting keeps 1024 MiB; this is what a 4 GB card can spare.
// Measured on Monolith (GTX 1050 Ti, Inkling 2 Q6_K, 2026-11-24): a start reported at 3075 MiB
// against 3359 free (284 to spare) crashed under load, so less than ~300 is not enough. A start
// that spilled past the card into system memory ran 7.7x slower reading prompts and 3x slower
// writing than a smaller one that truly fit (16k at batch 256: 15.2 / 4.4 tokens a second), which
// is why a smaller size that fits always beats a larger one that does not.
const cardMarginMiB = 384

// measurer asks a computer's engine what a start would need (an interface so tests can
// stand in for the engine).
type measurer func(node string, req StartHeadReq) (MeasureRes, error)

func (h *Host) measureOn(node string, req StartHeadReq) (MeasureRes, error) {
	var res MeasureRes
	err := h.call(node, "measure_head", req, &res, 4*time.Minute)
	return res, err
}

// fitOutcome is what fitStart settled on.
type fitOutcome struct {
	Ctx       int
	UBatch    int
	VisionCPU bool
	ExpertCPU []int
	Measured  bool   // false: the engine could not be asked; the plan's estimate stands
	Short     int64  // MiB missing at the closest size tried, when nothing fits
	Note      string // for the progress list
}

// hostBudget is the key in fitStart's budget for this computer's own memory (not a card):
// what it lends, in MiB. Left out when the plan reads part of the model from the disk on
// purpose, or the memory is the graphics chip's (a Mac).
const hostBudget = "@host"

// fitStart checks a one-computer start against the engine's own report and, if it does not
// fit, finds the closest start that does: first a smaller prompt batch (a little slower
// reading, same conversation size), then, when the size was chosen automatically, a smaller
// conversation. The image add-on goes on the card only when there is room left for it.
// When nothing fits, the outcome says by how much (Short) and the start is not loaded: past
// a card's or a computer's memory an engine runs out, and on a Mac that froze the computer.
//
// allExperts lists every layer with experts: when the plan put some experts on the card, the
// card can have them back in memory (slower writing) before the prompt batch shrinks.
//
// budget is, for each helper in the start, the memory (MiB) Potluck may use there: a helper's
// engine reports its whole memory as free, so its share is checked against what it lends.
func fitStart(measure measurer, node string, req StartHeadReq, auto bool, visionMiB int64, allExperts []int, budget map[string]int64) fitOutcome {
	sizes := []int{req.Ctx}
	if auto {
		for c := req.Ctx / 2; c >= 8192; c /= 2 {
			sizes = append(sizes, c)
		}
	}
	placements := [][]int{req.ExpertCPU}
	if len(req.ExpertCPU) > 0 && len(allExperts) > len(req.ExpertCPU) {
		placements = append(placements, allExperts)
	}
	keep := fitOutcome{Ctx: req.Ctx, UBatch: req.UBatch, ExpertCPU: req.ExpertCPU}
	first := true
	var least int64 // the smallest shortfall seen
	for _, c := range sizes {
		for _, experts := range placements {
			for _, ub := range []int{0, 256, 128} {
				r := req
				r.Ctx, r.UBatch, r.ExpertCPU = c, ub, experts
				res, err := measure(node, r)
				if err != nil {
					if first {
						keep.Note = "Could not measure it first (" + err.Error() + "); going by the estimate"
						return keep
					}
					continue
				}
				first = false
				short := cardShortMiB(res, 0, budget)
				if short > 0 {
					if least == 0 || short < least {
						least = short
					}
					continue
				}
				out := fitOutcome{Ctx: c, UBatch: ub, ExpertCPU: experts, Measured: true}
				if visionMiB > 0 && cardShortMiB(res, visionMiB, budget) > 0 {
					out.VisionCPU = true
				}
				var bits []string
				if c != req.Ctx {
					bits = append(bits, fmt.Sprintf("conversation memory %dk words instead of %dk", c/1024, req.Ctx/1024))
				}
				if len(experts) > len(req.ExpertCPU) {
					bits = append(bits, "every expert in memory")
				}
				if ub != 0 {
					bits = append(bits, fmt.Sprintf("reading prompts %d words at a time", ub))
				}
				if out.VisionCPU {
					bits = append(bits, "the image add-on on the processor")
				}
				out.Note = "Fits on the graphics card"
				if len(req.Workers) > 0 {
					out.Note = "Fits on every computer"
				}
				if len(bits) > 0 {
					out.Note += " with " + strings.Join(bits, ", ")
				}
				return out
			}
		}
	}
	keep.Measured = !first
	keep.Short = least
	keep.Note = fmt.Sprintf("The engine reports it will not fit at any size tried (%s short)", humanBytes(least<<20))
	return keep
}

// cardShortMiB: how much more memory the start needs than there is (after the margin), with
// extra MiB on this computer's first graphics card; 0 when it fits. This computer's cards are
// checked against what they have free; a helper's devices against what it lends (budget),
// and each of its devices against what it has free when that is a real figure (a card).
func cardShortMiB(res MeasureRes, extra int64, budget map[string]int64) int64 {
	var short int64
	first := true
	perNode := map[string]int64{}
	for _, d := range res.Devices {
		if d.Node != "" {
			perNode[d.Node] += d.Self
			if d.Total <= 0 || d.Free >= d.Total {
				continue // its engine reports all its memory as free: only the budget says
			}
		} else if d.Total <= 0 {
			continue
		}
		need := d.Self + cardMarginMiB
		if d.Node == "" && first {
			need += extra
			first = false
		}
		if need > d.Free {
			short = max(short, need-d.Free)
		}
	}
	for n, self := range perNode {
		if b := budget[n]; b > 0 && self+cardMarginMiB > b {
			short = max(short, self+cardMarginMiB-b)
		}
	}
	// This computer's own memory: what the engine keeps there (the layers and experts left
	// off the card, the image add-on on the processor) against what it lends. What it lends
	// already leaves a reserve for the system and other apps (offer), so no card margin is
	// added here: with one, Inkling 2 Q6_K on Monolith, which runs well, was refused 46 MB short.
	if b := budget[hostBudget]; b > 0 && res.Host.Self > b {
		short = max(short, res.Host.Self-b)
	}
	return short
}
