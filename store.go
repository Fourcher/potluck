package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ModelRef names one downloadable model file.
type ModelRef struct {
	Source string      `json:"source"` // hf | url | local
	Repo   string      `json:"repo"`
	File   string      `json:"file"`
	URL    string      `json:"url"`
	Size   int64       `json:"size"`
	SHA256 string      `json:"sha256"`
	Title  string      `json:"title"`
	Parts  []ModelPart `json:"parts,omitempty"` // every file, when the model comes as several
	Node   string      `json:"node,omitempty"`  // local: the computer whose disk holds the file
	// Vision is the model's image add-on (an "mmproj" file from the same repository), loaded
	// with it on the computer running the model so it can see pictures and screenshots.
	Vision *ModelPart `json:"vision,omitempty"`
}

// ModelPart is one file of a model that is split across several.
type ModelPart struct {
	File   string `json:"file"`
	URL    string `json:"url"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// StoreFile is one quantisation of a model, with a verdict for this set of computers.
type StoreFile struct {
	File    string `json:"file"`
	Quant   string `json:"quant"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	Parts   int    `json:"parts"`   // >1: the model comes as several files
	Verdict string `json:"verdict"` // one | pool | stream | no
	// Estimates: expected speed for each way it can run, from the computers' benchmarks.
	// Measured: the speed seen when it actually ran here, when it has.
	Estimates []Estimate `json:"estimates,omitempty"`
	Measured  *Measured  `json:"measured,omitempty"`
	Note      string     `json:"note"`
}

// StoreModel is one entry in the model store.
type StoreModel struct {
	Source    string      `json:"source"` // hf | pirateface
	Repo      string      `json:"repo"`
	Downloads int64       `json:"downloads"`
	Likes     int64       `json:"likes"`
	Blurb     string      `json:"blurb,omitempty"`
	SizeNote  string      `json:"sizeNote,omitempty"`
	Seeds     int         `json:"seeds,omitempty"`
	Magnet    string      `json:"magnet,omitempty"`
	Files     []StoreFile `json:"files,omitempty"`
	// Recommended marks the model Potluck suggests trying first.
	Recommended bool `json:"recommended,omitempty"`
	// Section groups the starting shelf: pick | trending | new | popular.
	Section string   `json:"section,omitempty"`
	GoodFor []string `json:"goodFor,omitempty"`
	Sources []string `json:"sources,omitempty"`
	Created string   `json:"created,omitempty"`
}

// recommended is the model Potluck suggests first: the daily catalog's pick when it has one.
func (h *Host) recommended() string {
	if c := h.catalog(); c != nil && len(c.Picks) > 0 {
		return c.Picks[0].Repo
	}
	return recommendedRepo
}

// recommendedRepo is the model Potluck suggests first (starred in the model store).
const recommendedRepo = "AtomicChat/Inkling-2-Mini-Uncensored-GGUF"

// featured is the starting shelf: the recommended model, then popular open models from small
// to very large.
var featured = []struct{ Repo, Blurb string }{
	{recommendedRepo, "Our pick to start with: Inkling 2 Mini, 26 billion parameters, whose smallest size runs on a 12 GB laptop. This version has had its refusals removed: it answers what it is asked."},
	{"unsloth/gemma-4-12B-it-GGUF", "Small and quick. Runs on almost anything."},
	{"unsloth/Qwen3.8-27B-GGUF", "Strong all-rounder."},
	{"unsloth/gemma-4-26B-A4B-it-GGUF", "Fast for its size: only part of it runs per word."},
	{"unsloth/Qwen3-30B-A3B-Instruct-2507-GGUF", "Fast mixture-of-experts model, good first test."},
	{"unsloth/Qwen3.6-35B-A3B-GGUF", "Good at tools and agent work."},
	{"NousResearch/Hermes-4.3-36B-GGUF", "Large general model."},
	{"unsloth/Qwen3.5-122B-A10B-GGUF", "Very large. Needs several computers pooled together."},
}

var (
	treeCache   = map[string][]hfEntry{}
	treeCacheMu sync.Mutex
)

type hfEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	LFS  *struct {
		OID string `json:"oid"`
	} `json:"lfs"`
}

func hfTree(repo string) ([]hfEntry, error) {
	treeCacheMu.Lock()
	if t, ok := treeCache[repo]; ok {
		treeCacheMu.Unlock()
		return t, nil
	}
	treeCacheMu.Unlock()
	var t []hfEntry
	if err := httpGetJSON("https://huggingface.co/api/models/"+repo+"/tree/main?recursive=true", &t); err != nil {
		return nil, err
	}
	treeCacheMu.Lock()
	treeCache[repo] = t
	treeCacheMu.Unlock()
	return t, nil
}

var (
	splitRe = regexp.MustCompile(`-(\d{5})-of-(\d{5})\.gguf$`)
	quantRe = regexp.MustCompile(`(?i)[-.]((?:UD-)?(?:IQ\d[_A-Z0-9]*|Q\d[_A-Z0-9]*|BF16|F16|F32|MXFP4[_A-Z0-9]*))\.gguf$`)
)

// hfFiles lists the model files of a Hugging Face repository, smallest first.
func hfFiles(repo string) ([]StoreFile, error) {
	tree, err := hfTree(repo)
	if err != nil {
		return nil, err
	}
	byKey := map[string]*StoreFile{}
	var order []string
	for _, e := range tree {
		low := strings.ToLower(e.Path)
		base := low[strings.LastIndex(low, "/")+1:]
		if !strings.HasSuffix(low, ".gguf") || strings.Contains(low, "mmproj") || strings.Contains(low, "imatrix") || strings.HasPrefix(base, "mtp-") || strings.Contains(low, "lora") {
			continue // helper files (vision projector, calibration data, speculative head, adapter), not the model itself
		}
		key, parts := e.Path, 1
		if m := splitRe.FindStringSubmatch(e.Path); m != nil {
			key = splitRe.ReplaceAllString(e.Path, ".gguf")
			parts, _ = strconv.Atoi(m[2])
		}
		f := byKey[key]
		if f == nil {
			f = &StoreFile{File: e.Path, Parts: parts}
			if m := quantRe.FindStringSubmatch(key); m != nil {
				f.Quant = strings.ToUpper(m[1])
			} else {
				f.Quant = strings.TrimSuffix(key[strings.LastIndex(key, "/")+1:], ".gguf")
			}
			if e.LFS != nil {
				f.SHA256 = e.LFS.OID
			}
			byKey[key] = f
			order = append(order, key)
		}
		f.Size += e.Size
	}
	var out []StoreFile
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Size < out[j].Size })
	// Drop stray small files that sit beside the real model (anything under a tenth of the biggest).
	if n := len(out); n > 1 {
		kept := out[:0]
		for _, f := range out {
			if f.Size*10 >= out[n-1].Size || f.Size > 2<<30 {
				kept = append(kept, f)
			}
		}
		out = kept
	}
	return out, nil
}

// judge marks every file with what this set of computers can do with it.
func (h *Host) judge(files []StoreFile) {
	nodes := h.planNodes()
	cfg := h.cfg.Get()
	profiles, measured := cfg.Profiles, cfg.Measured
	var sum, biggest int64
	for _, n := range nodes {
		c := n.CPUBytes + n.GPUBytes
		if n.Unified {
			c = n.GPUBytes
		}
		sum += c
		if c > biggest {
			biggest = c
		}
	}
	// The biggest model a computer could stream from its disk, for the families InferSSD knows.
	var streamName string
	var streamDisk int64
	for _, n := range nodes {
		if n.StreamVariant != "" && n.DiskFree > streamDisk {
			streamName, streamDisk = n.Name, n.DiskFree
		}
	}
	for i := range files {
		f := &files[i]
		need := f.Size + f.Size/25 + perDeviceOverhead // a little room for working memory
		switch {
		case need <= biggest:
			f.Verdict, f.Note = "one", "Fits on one of your computers."
		case need+int64(len(nodes))*perDeviceOverhead <= sum:
			f.Verdict, f.Note = "pool", "Too big for any one computer. Runs across your computers together."
		case streamName != "" && streamNamePattern.MatchString(f.File) && f.Size+(4<<30) <= streamDisk:
			f.Verdict, f.Note = "stream", fmt.Sprintf("Too big for your computers' memory, but %s may run it by streaming from its disk. Checked when you set it up.", streamName)
		default:
			f.Verdict, f.Note = "no", fmt.Sprintf("Needs about %s more memory than your computers offer.", humanBytes(need-sum))
		}
		if f.Verdict != "no" {
			f.Estimates = estimateFile(*f, nodes, profiles)
			for i := range measured {
				if measured[i].File == f.File {
					f.Measured = &measured[i]
					break
				}
			}
		}
	}
}

// storeSearch looks for models on Hugging Face or PirateFace.
func (h *Host) storeSearch(source, q string) ([]StoreModel, error) {
	q = strings.TrimSpace(q)
	if source == "pirateface" {
		return pirateSearch(q)
	}
	if q == "" {
		// The starting shelf: the catalog's picks (checked against these computers), then
		// what is trending, new and popular on Hugging Face (sizes checked when opened).
		cat := h.catalog()
		rec := h.recommended()
		var out []StoreModel
		for _, p := range cat.Picks {
			out = append(out, StoreModel{Source: "hf", Repo: p.Repo, Blurb: p.Blurb, GoodFor: p.GoodFor, Sources: p.Sources, Section: "pick", Recommended: p.Repo == rec})
		}
		if len(out) == 0 {
			for _, f := range featured {
				out = append(out, StoreModel{Source: "hf", Repo: f.Repo, Blurb: f.Blurb, Section: "pick", Recommended: f.Repo == rec})
			}
		}
		var wg sync.WaitGroup
		for i := range out {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				if files, err := hfFiles(out[i].Repo); err == nil {
					h.judge(files)
					out[i].Files = files
				}
			}(i)
		}
		wg.Wait()
		for _, l := range []struct {
			name string
			list []CatalogEntry
		}{{"trending", cat.Trending}, {"new", cat.New}, {"popular", cat.Popular}} {
			for _, e := range l.list {
				out = append(out, StoreModel{Source: "hf", Repo: e.Repo, Downloads: e.Downloads, Likes: e.Likes, Created: e.Created, Section: l.name})
			}
		}
		return out, nil
	}
	var res []struct {
		ID        string `json:"id"`
		Downloads int64  `json:"downloads"`
		Likes     int64  `json:"likes"`
	}
	u := "https://huggingface.co/api/models?filter=gguf&sort=downloads&direction=-1&limit=24&search=" + url.QueryEscape(q)
	if err := httpGetJSON(u, &res); err != nil {
		return nil, fmt.Errorf("could not reach Hugging Face: %w", err)
	}
	out := make([]StoreModel, 0, len(res))
	for _, r := range res {
		out = append(out, StoreModel{Source: "hf", Repo: r.ID, Downloads: r.Downloads, Likes: r.Likes, Recommended: r.ID == recommendedRepo})
	}
	return out, nil
}

var pirateLine = regexp.MustCompile(`^(\S+/\S+)\s+([\d.]+\s*[KMGT]B)\s+(\d+)\s+seeds?\s+(magnet:\S+)`)

// pirateSearch reads PirateFace's plain-text search. PirateFace is an index of community
// torrents of Hugging Face models; nothing from its site is executed, only the listing is read.
func pirateSearch(q string) ([]StoreModel, error) {
	if q == "" {
		q = "gguf"
	}
	req, _ := http.NewRequest("GET", "https://pirateface.co/s/"+url.PathEscape(q), nil)
	req.Header.Set("User-Agent", userAgent)
	res, err := apiClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("could not reach PirateFace: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("PirateFace answered %s", res.Status)
	}
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	var out []StoreModel
	for _, line := range strings.Split(string(body), "\n") {
		m := pirateLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		seeds, _ := strconv.Atoi(m[3])
		out = append(out, StoreModel{Source: "pirateface", Repo: m[1], SizeNote: m[2], Seeds: seeds, Magnet: m[4]})
		if len(out) >= 40 {
			break
		}
	}
	return out, nil
}

// resolveModel fills in the download address, size and checksum.
func resolveModel(ctx context.Context, m *ModelRef) error {
	if m.Source == "local" {
		if m.Node == "" || m.File == "" {
			return errors.New("pick a model file first")
		}
		m.File = filepath.Base(m.File)
		if m.Title == "" {
			m.Title = strings.TrimSuffix(splitRe.ReplaceAllString(m.File, ".gguf"), ".gguf")
		}
		return nil
	}
	if m.URL == "" && m.Repo != "" {
		if m.File == "" {
			return errors.New("pick a model file first")
		}
		m.Source = "hf"
		m.URL = "https://huggingface.co/" + m.Repo + "/resolve/main/" + (&url.URL{Path: m.File}).EscapedPath()
		hfURL := func(path string) string {
			return "https://huggingface.co/" + m.Repo + "/resolve/main/" + (&url.URL{Path: path}).EscapedPath()
		}
		if tree, err := hfTree(m.Repo); err == nil {
			m.Vision = nil
			for _, pref := range []string{"f16", "bf16", ""} { // full precision first: it is small
				for _, e := range tree {
					low := strings.ToLower(e.Path)
					if m.Vision == nil && strings.Contains(low, "mmproj") && strings.HasSuffix(low, pref+".gguf") {
						sha := ""
						if e.LFS != nil {
							sha = e.LFS.OID
						}
						m.Vision = &ModelPart{File: e.Path[strings.LastIndex(e.Path, "/")+1:], URL: hfURL(e.Path), Size: e.Size, SHA256: sha}
					}
				}
			}
			key := splitRe.ReplaceAllString(m.File, ".gguf")
			split := key != m.File
			m.Parts, m.Size = nil, 0
			for _, e := range tree {
				sha := ""
				if e.LFS != nil {
					sha = e.LFS.OID
				}
				switch {
				case split && splitRe.MatchString(e.Path) && splitRe.ReplaceAllString(e.Path, ".gguf") == key:
					// One of the files of a model that comes in several pieces.
					m.Parts = append(m.Parts, ModelPart{File: e.Path[strings.LastIndex(e.Path, "/")+1:], URL: hfURL(e.Path), Size: e.Size, SHA256: sha})
					m.Size += e.Size
				case !split && e.Path == m.File:
					m.Size, m.SHA256 = e.Size, sha
				}
			}
			if split {
				if len(m.Parts) == 0 {
					return errors.New("could not find that model's files on Hugging Face")
				}
				sort.Slice(m.Parts, func(i, j int) bool { return m.Parts[i].File < m.Parts[j].File })
				m.URL, m.SHA256 = m.Parts[0].URL, ""
				if m.Title == "" {
					m.Title = strings.TrimSuffix(key[strings.LastIndex(key, "/")+1:], ".gguf")
				}
				m.File = m.Parts[0].File
			}
		}
	}
	if m.URL == "" {
		return errors.New("no download address for this model")
	}
	if u, err := url.Parse(m.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("the model address must start with https://")
	}
	if m.File == "" {
		u, _ := url.Parse(m.URL)
		m.File = u.Path[strings.LastIndex(u.Path, "/")+1:]
	}
	if !strings.HasSuffix(strings.ToLower(m.File), ".gguf") {
		return errors.New("only .gguf model files are supported")
	}
	if m.Size == 0 {
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		size, err := remoteSize(c, m.URL)
		if err != nil {
			return fmt.Errorf("could not reach the model file: %w", err)
		}
		m.Size = size
	}
	if m.Title == "" {
		m.Title = strings.TrimSuffix(m.File[strings.LastIndex(m.File, "/")+1:], ".gguf")
	}
	m.File = m.File[strings.LastIndex(m.File, "/")+1:]
	return nil
}
