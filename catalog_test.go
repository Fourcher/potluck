package main

import (
	"encoding/json"
	"testing"
)

// A pick that needs a newer engine than this Potluck installs is left out; the others stay.
func TestCatalogUsable(t *testing.T) {
	have := tagBuild(engineTag)
	c := &Catalog{Picks: []CatalogPick{{Repo: "a"}, {Repo: "b", NeedsEngine: have}, {Repo: "c", NeedsEngine: have + 1}}}
	u := c.usable()
	if len(u.Picks) != 2 || u.Picks[0].Repo != "a" || u.Picks[1].Repo != "b" {
		t.Fatalf("got %+v", u.Picks)
	}
	if len(c.Picks) != 3 {
		t.Fatal("usable changed the catalog it was given")
	}
}

// The built-in catalog parses and every pick it carries runs on this Potluck's engine.
func TestBuiltinCatalog(t *testing.T) {
	var c Catalog
	if err := json.Unmarshal(builtinCatalog, &c); err != nil || len(c.Picks) == 0 {
		t.Fatalf("built-in catalog: %v (%d picks)", err, len(c.Picks))
	}
	if len(c.usable().Picks) != len(c.Picks) {
		t.Fatal("the built-in catalog has picks this engine cannot run")
	}
}
