package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// On this computer's real screen, only when asked: a screenshot scaled to click points, and
// the mouse moved to where it already is (nothing is clicked or typed).
func TestScreenReal(t *testing.T) {
	if os.Getenv("POTLUCK_TEST_SCREEN") == "" {
		t.Skip("set POTLUCK_TEST_SCREEN=1 to use this computer's screen")
	}
	ctx := context.Background()
	text, pic, err := screenTool(ctx, t.TempDir(), "screen_look", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s (%s)", text, pic)
	if !strings.Contains(text, "points") {
		t.Fatal(text)
	}
	where, err := exec.Command("osascript", "-l", "JavaScript", "-e", `ObjC.import("CoreGraphics"); const p = $.CGEventGetLocation($.CGEventCreate(null)); Math.round(p.x) + " " + Math.round(p.y)`).Output()
	if err != nil {
		t.Fatal(err)
	}
	var x, y int
	if _, err := fmtSscan(strings.TrimSpace(string(where)), &x, &y); err != nil {
		t.Fatal(err)
	}
	if err := jxaMouse(ctx, x, y, "move", 0); err != nil {
		t.Fatalf("posting a mouse event failed (Accessibility?): %v", err)
	}
	if _, _, err := screenTool(ctx, t.TempDir(), "screen_key", map[string]any{"keys": "rm -rf /"}); err == nil {
		t.Fatal("a key name with odd characters must be refused")
	}
}

func fmtSscan(s string, a ...any) (int, error) { return fmt.Sscan(s, a...) }
