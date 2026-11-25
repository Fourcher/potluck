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

// CatalogList is a ranked list someone else made (catalog/lists.json), shown with its age.
type CatalogList struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	By      string `json:"by"`
	AsOf    string `json:"asOf"`
	Added   string `json:"added"`   // the day Potluck took it in (YYYY-MM-DD)
	Expires string `json:"expires"` // after this day it is shown as out of date
	About   string `json:"about"`
	Tiers   []struct {
		Tier  string `json:"tier"`
		Items []struct {
			Name   string `json:"name"`
			Maker  string `json:"maker,omitempty"`
			Note   string `json:"note,omitempty"`
			Repo   string `json:"repo,omitempty"`   // opens the model's sizes
			Search string `json:"search,omitempty"` // or searches for it, when no one repository is meant
		} `json:"items"`
	} `json:"tiers"`
}

type Catalog struct {
	Updated  string         `json:"updated"`
	Source   string         `json:"source"`
	Picks    []CatalogPick  `json:"picks"`
	Trending []CatalogEntry `json:"trending"`
	New      []CatalogEntry `json:"new"`
	Popular  []CatalogEntry `json:"popular"`
	Lists    []CatalogList  `json:"lists,omitempty"`
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
		// A newer Potluck carries a newer catalog than the copy fetched before the update.
		var built Catalog
		_ = json.Unmarshal(builtinCatalog, &built)
		if catCurrent == nil || built.Updated > catCurrent.Updated {
			catCurrent = &built
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
			if c.Updated >= catCurrent.Updated {
				catCurrent = &c
			}
			catMu.Unlock()
		}()
	}
	return catCurrent
}
