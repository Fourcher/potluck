package main

import (
	"context"
	"errors"
	"fmt"
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

// QuickStart gets a new person from "installed" to "chatting" with one button: it starts a
// model that is already on one of the computers (found from LM Studio, Ollama or an earlier
// download) if one fits, and otherwise downloads the first of the recommended models that
// fits, in the size that suits these computers.
func (h *Host) QuickStart(ctx context.Context) (ModelRef, error) {
	if m, ok := h.quickLocal(ctx); ok {
		return m, h.Start(m, 0, "") // 0: the conversation memory is chosen to fit (autoCtx)
	}
	repos := []string{h.recommended()}
	if c := h.catalog(); c != nil {
		for _, p := range c.Picks {
			repos = append(repos, p.Repo)
		}
	}
	seen := map[string]bool{}
	reached, fitted := false, false
	var smallest int64
	var lastErr error // a lookup that failed for a model that would have fitted
	for _, repo := range repos {
		if seen[repo] {
			continue
		}
		seen[repo] = true
		if ctx.Err() != nil {
			break
		}
		files, err := hfFiles(repo)
		if err != nil {
			continue
		}
		reached = true
		h.judge(files)
		for _, f := range files {
			if smallest == 0 || f.Size < smallest {
				smallest = f.Size
			}
		}
		f, ok := pickQuick(files)
		if !ok {
			continue
		}
		fitted = true
		m := ModelRef{Source: "hf", Repo: repo, File: f.File}
		if err := resolveModel(ctx, &m); err != nil {
			lastErr = err
			continue
		}
		info, err := h.modelInfo(ctx, &m)
		if err != nil {
			lastErr = err
			continue
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
	if ctx.Err() != nil {
		return ModelRef{}, errors.New("looking for a model that fits took too long; try again")
	}
	if fitted && lastErr != nil {
		return ModelRef{}, fmt.Errorf("a model that fits was found, but could not be read: %w", lastErr)
	}
	if !reached {
		return ModelRef{}, errors.New("could not reach Hugging Face to fetch the model list. Check the internet connection and try again")
	}
	return ModelRef{}, fmt.Errorf("none of the recommended models fits the computers connected now (the smallest needs about %s of free memory). Add another computer under Computers, or close other apps and try again", humanBytes(smallest+smallest/5))
}

// quickLocal is a model already on one of the computers that runs well as it is: the last one
// used if it is still there, otherwise the largest that fits without the disk.
func (h *Host) quickLocal(ctx context.Context) (ModelRef, bool) {
	var best ModelRef
	var bestSize int64
	last, known := h.cfg.Get().LastModel, h.cfg.Get().Known
	for _, c := range h.chatModels() {
		if strings.HasPrefix(c.Node, "ext:") || c.Node == "" {
			continue
		}
		m := ModelRef{Source: "local", File: c.File, Node: c.Node, Title: c.Title, Size: c.Size}
		if k, ok := known[c.File]; ok {
			m = k // one Potluck downloaded: started the way it was first started (see chatSwitch)
		}
		info, err := h.modelInfo(ctx, &m)
		if err != nil {
			continue
		}
		info.PlanCtx = 8192
		p := h.planFor(info, "", c.Node)
		if !p.Fits || p.Overflow > 0 {
			continue
		}
		if last != nil && last.File == c.File {
			return m, true
		}
		if c.Size > bestSize {
			best, bestSize = m, c.Size
		}
	}
	return best, bestSize > 0
}
