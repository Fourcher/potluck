package main

import (
	"context"
	"errors"
	"strings"
)

// quickPrefs is the order in which a first model's size is chosen: good quality that still
// answers quickly first, then smaller ones, then the bigger, slower ones.
var quickPrefs = []string{"Q4_K_M", "Q4_K", "Q5_K_M", "Q5_K", "Q4_K_S", "IQ4_XS", "IQ4_NL", "Q4_0", "Q6_K", "Q3_K_M", "Q3_K_L", "IQ3_M", "IQ3_XS", "Q3_K_S", "IQ2_M", "Q2_K", "IQ2_S", "IQ2_XS", "Q5_K_S", "Q8_0"}

// pickQuick chooses the file to start with: the preferred size that fits on one computer,
// or failing that the smallest that runs pooled.
func pickQuick(files []StoreFile) (StoreFile, bool) {
	rank := func(f StoreFile) int {
		for i, q := range quickPrefs {
			if strings.EqualFold(f.Quant, q) {
				return i
			}
		}
		return len(quickPrefs)
	}
	var best StoreFile
	found := false
	for _, f := range files {
		if f.Verdict == "one" && (!found || rank(f) < rank(best)) {
			best, found = f, true
		}
	}
	if found {
		return best, true
	}
	for _, f := range files {
		if f.Verdict == "pool" && (!found || f.Size < best.Size) {
			best, found = f, true
		}
	}
	return best, found
}

// QuickStart downloads and starts the recommended model in the size that suits these
// computers, so a new person gets from "installed" to "chatting" with one button.
func (h *Host) QuickStart(ctx context.Context) (ModelRef, error) {
	repo := h.recommended()
	files, err := hfFiles(repo)
	if err != nil {
		return ModelRef{}, errors.New("could not reach Hugging Face to fetch the model list. Check the internet connection and try again")
	}
	h.judge(files)
	f, ok := pickQuick(files)
	if !ok {
		return ModelRef{}, errors.New("none of this model's sizes fits your computers yet. Add another computer, or pick a smaller model under Models")
	}
	m := ModelRef{Source: "hf", Repo: repo, File: f.File}
	if err := resolveModel(ctx, &m); err != nil {
		return ModelRef{}, err
	}
	info, err := h.modelInfo(ctx, &m)
	if err != nil {
		return ModelRef{}, errors.New("could not read that model: " + err.Error())
	}
	info.PlanCtx = 8192 // judged at the usual size; the start picks the size that fits
	solo := ""
	if f.Verdict == "one" {
		if p := h.bestSolo(info, ""); p != nil {
			solo = p.Head
		}
	}
	return m, h.Start(m, 0, solo) // 0: the conversation memory is chosen to fit (autoCtx)
}
