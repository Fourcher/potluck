package main

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The model catalog: hand-picked models plus Hugging Face's trending, new and popular lists,
// refreshed daily in the repository by tools/catalog.py. Potluck carries the copy it was
// built with and fetches the newest one once a day.

//go:embed catalog/models.json
var builtinCatalog []byte

const catalogURL = "https://raw.githubusercontent.com/Fourcher/potluck/main/catalog/models.json"

type CatalogPick struct {
	Repo    string   `json:"repo"`
	Blurb   string   `json:"blurb"`
	GoodFor []string `json:"good_for"`
	Sources []string `json:"sources"`
}

type CatalogEntry struct {
	Repo      string `json:"repo"`
	Downloads int64  `json:"downloads"`
	Likes     int64  `json:"likes"`
	Created   string `json:"created"`
}

type Catalog struct {
	Updated  string         `json:"updated"`
	Source   string         `json:"source"`
	Picks    []CatalogPick  `json:"picks"`
	Trending []CatalogEntry `json:"trending"`
	New      []CatalogEntry `json:"new"`
	Popular  []CatalogEntry `json:"popular"`
}

var (
	catMu      sync.Mutex
	catCurrent *Catalog
	catChecked time.Time
)

// catalog returns the newest catalog this computer has, refreshing it at most once a day.
func (h *Host) catalog() *Catalog {
	catMu.Lock()
	defer catMu.Unlock()
	cache := filepath.Join(h.a.dir, "catalog.json")
	if catCurrent == nil {
		if b, err := os.ReadFile(cache); err == nil {
			var c Catalog
			if json.Unmarshal(b, &c) == nil && len(c.Picks) > 0 {
				catCurrent = &c
			}
		}
		if catCurrent == nil {
			var c Catalog
			_ = json.Unmarshal(builtinCatalog, &c)
			catCurrent = &c
		}
		if st, err := os.Stat(cache); err == nil {
			catChecked = st.ModTime()
		}
	}
	if time.Since(catChecked) > 24*time.Hour {
		catChecked = time.Now()
		go func() {
			req, _ := http.NewRequest("GET", catalogURL, nil)
			req.Header.Set("User-Agent", "potluck")
			res, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
			if err != nil {
				return
			}
			defer res.Body.Close()
			var c Catalog
			if res.StatusCode != 200 || json.NewDecoder(res.Body).Decode(&c) != nil || len(c.Picks) == 0 {
				return
			}
			if b, err := json.MarshalIndent(c, "", " "); err == nil {
				_ = os.WriteFile(cache, b, 0o644)
			}
			catMu.Lock()
			catCurrent = &c
			catMu.Unlock()
		}()
	}
	return catCurrent
}
