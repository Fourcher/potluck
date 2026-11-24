package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBundleImport(t *testing.T) {
	data := t.TempDir()
	proj := t.TempDir()
	_ = os.WriteFile(filepath.Join(proj, "AGENTS.md"), []byte("Use tabs."), 0o644)
	raw := "Here it is:\n```json\n" + `{"potluckImport":1,"from":"ChatGPT","profile":"Runs a roofing business.","preferences":"Short answers.",
	 "memories":[{"title":"Dog","text":"Has a dog called Rex."},{"title":"Stack","text":"Uses Go.","project":"site"}],
	 "projects":[{"name":"site","path":"` + proj + `","notes":"The company site."}],
	 "skills":[{"name":"quote","description":"Write a roof quote","instructions":"Ask for the area first."}],
	 "connectors":[{"name":"files","kind":"mcp-stdio","command":"npx","args":["fs"]}]}` + "\n```"
	c, err := bundleFound([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := saveContinuity(data, []*contFound{c}); err != nil {
		t.Fatal(err)
	}
	ctx := loadContContext(data)
	if !strings.Contains(ctx.Profile, "roofing") || !strings.Contains(ctx.Profile, "Short answers") {
		t.Fatalf("profile: %q", ctx.Profile)
	}
	if !strings.Contains(ctx.Memory, "Rex") || strings.Contains(ctx.Memory, "Uses Go") {
		t.Fatalf("global memory should hold only global notes: %q", ctx.Memory)
	}
	if projectMemory(data, "site") != "Uses Go." {
		t.Fatal("project memory missing")
	}
	if len(ctx.Projects) != 1 || ctx.Projects[0].Instructions != "AGENTS.md" {
		t.Fatalf("projects: %+v", ctx.Projects)
	}
	if s, err := readSkill(data, "quote"); err != nil || !strings.Contains(s, "area first") {
		t.Fatalf("skill: %v %q", err, s)
	}
	// Importing the same thing again replaces it instead of piling up copies.
	c, _ = bundleFound([]byte(raw))
	_, _ = saveContinuity(data, []*contFound{c})
	if ctx := loadContContext(data); strings.Count(ctx.Profile, "roofing") != 1 || len(ctx.Projects) != 1 {
		t.Fatalf("second import duplicated things: %d profiles, %d projects", strings.Count(ctx.Profile, "roofing"), len(ctx.Projects))
	}
	if _, err := bundleFound([]byte("not json")); err == nil {
		t.Fatal("accepted nonsense")
	}
}

// Names from a bundle can never write outside the continuity folder.
func TestContNames(t *testing.T) {
	for _, bad := range []string{"../../etc/passwd", "..", "/abs/path", "a/b"} {
		if n := contName(bad); strings.Contains(n, "/") || n == ".." || n == "." {
			t.Fatalf("%q became %q", bad, n)
		}
	}
}

func TestCodexMCP(t *testing.T) {
	got := codexMCP("model = \"o4\"\n[mcp_servers.files]\ncommand = \"npx\"\nargs = [\"-y\", \"fs\"]\n[mcp_servers.web]\nurl = \"http://localhost:9\"\n[other]\ncommand = \"x\"\n")
	if len(got) != 2 || got[0].Command != "npx" || len(got[0].Args) != 2 || got[1].Kind != "mcp-http" {
		t.Fatalf("%+v", got)
	}
}

// A real setup on this computer (only when asked: it reads the person's own files).
func TestScanRealSetup(t *testing.T) {
	if os.Getenv("POTLUCK_TEST_SCAN") == "" {
		t.Skip("set POTLUCK_TEST_SCAN=1 to scan this computer's real AI setup")
	}
	home, _ := os.UserHomeDir()
	found := scanContinuity(home, strings.Split(os.Getenv("POTLUCK_TEST_SCAN_FOLDERS"), ","))
	data := t.TempDir()
	src, err := saveContinuity(data, found)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range src {
		t.Logf("%s: %d instruction files, %d memories, %d skills, %d projects, %d connectors, %d rules", s.Name, s.Profile, s.Memories, s.Skills, s.Projects, s.Conns, s.Rules)
	}
	ctx := loadContContext(data)
	t.Logf("profile %d chars, global memory %d chars, %d skills, %d projects", len(ctx.Profile), len(ctx.Memory), len(ctx.Skills), len(ctx.Projects))
	for i, p := range ctx.Projects {
		if i < 6 {
			t.Logf("  project %s (%s) instructions=%q memory=%q", p.Name, p.Path, p.Instructions, p.Memory)
		}
	}
}
