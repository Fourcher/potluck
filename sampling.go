package main

// Sampling settings: how the engine picks each next word. Model makers publish the settings
// their model was trained and tested with, and running it on generic defaults instead is a
// common cause of answers that wander or repeat themselves (Qwen: greedy decoding "can lead
// to performance degradation and endless repetitions"; DeepSeek-R1: 0.6 "to prevent endless
// repetitions").
//
// The engine already uses any settings stored in the model file (general.sampling.*, read
// by llama.cpp since early 2026). Most files carry only some or none, so Potluck fills in
// the maker's published values for the keys a file leaves out, at engine start. A request
// that names its own settings still wins.

import (
	"sort"
	"strconv"
	"strings"
)

// makerSampling is the model makers' published settings, by model family (the file's
// general.architecture). Only settings with a published source are listed.
var makerSampling = map[string]map[string]float64{
	// Inkling 2 (Thinking Machines): evaluated at temperature 1.0, top_p 1.0, top_k 0, min_p 0
	// (generation_config.json: temperature 1.0; Unsloth's Inkling 2 guide gives the full set).
	"inkling2":      {"temp": 1.0, "top_p": 1.0, "top_k": 0, "min_p": 0},
	"inkling2_loop": {"temp": 1.0, "top_p": 1.0, "top_k": 0, "min_p": 0},
	// Qwen3 (model card, thinking mode): temperature 0.6, top_p 0.95, top_k 20, min_p 0.
	"qwen3":    {"temp": 0.6, "top_p": 0.95, "top_k": 20, "min_p": 0},
	"qwen3moe": {"temp": 0.6, "top_p": 0.95, "top_k": 20, "min_p": 0},
	// DeepSeek-R1 (model card): temperature 0.6, top_p 0.95.
	"deepseek2": {"temp": 0.6, "top_p": 0.95},
}

var samplingFlag = map[string]string{"temp": "--temp", "top_p": "--top-p", "top_k": "--top-k", "min_p": "--min-p"}

// samplingArgs are the engine flags for the maker's settings the file does not already carry.
func samplingArgs(m *ModelInfo) []string {
	set := makerSampling[m.Arch]
	keys := make([]string, 0, len(set))
	for k := range set {
		if !m.FileSampling[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var args []string
	for _, k := range keys {
		args = append(args, samplingFlag[k], strconv.FormatFloat(set[k], 'f', -1, 64))
	}
	return args
}

// fileSampling lists the general.sampling.* settings a model file carries ("temp", "top_k"...).
func fileSampling(kv map[string]any) map[string]bool {
	out := map[string]bool{}
	for k := range kv {
		if rest, ok := strings.CutPrefix(k, "general.sampling."); ok {
			out[rest] = true
		}
	}
	return out
}

// safeSampling passes on only sampling flags with a number, as a helper computer must not run
// whatever arguments a request carries.
func safeSampling(in []string) []string {
	var out []string
	for i := 0; i+1 < len(in); i += 2 {
		ok := false
		for _, f := range samplingFlag {
			ok = ok || in[i] == f
		}
		if _, err := strconv.ParseFloat(in[i+1], 64); ok && err == nil {
			out = append(out, in[i], in[i+1])
		}
	}
	return out
}
