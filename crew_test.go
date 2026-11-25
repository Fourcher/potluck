package main

import "testing"

// A crew member the person started is kept in the settings, so it comes back after Potluck
// restarts, and forgotten when the person stops it.
func TestCrewIsSavedAndForgotten(t *testing.T) {
	dir := t.TempDir()
	h := &Host{cfg: loadConfig(dir), id: &Identity{FP: "main"}, nodes: map[string]*Node{
		"pc": {Info: NodeInfo{ID: "pc", Name: "PC"}, Online: true},
	}}
	h.nodes["pc"].Info.Limits = Limits{Enabled: true, Pedal: 100}
	name, err := h.StartCrew("Worker One", "pc", ModelRef{Source: "hf", Repo: "a/b", File: "m.gguf"}, 8192)
	if err != nil {
		t.Fatal(err)
	}
	saved := loadConfig(dir).Get().Crew
	if len(saved) != 1 || saved[0].Name != name || saved[0].Node != "pc" || saved[0].Ctx != 8192 || saved[0].Model.File != "m.gguf" {
		t.Fatalf("saved %+v", saved)
	}
	_ = h.StopCrew(name)
	if c := loadConfig(dir).Get().Crew; len(c) != 0 {
		t.Fatalf("still saved after stop: %+v", c)
	}
}
