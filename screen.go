package main

// Screen control: the agent looks at the screen and works any app with the mouse and keyboard,
// the way a person would. Looking needs a model that can see (one loaded with its image add-on);
// the picture it gets is scaled so that a point in it is the point to click.
//
// Each system's own tools, nothing to install on macOS or Windows:
//   macOS    screencapture; mouse and scroll through CoreGraphics from JavaScript for
//            Automation; typing and keys through System Events. macOS asks once for Screen
//            Recording and Accessibility (request_permissions asks for them up front).
//   Windows  PowerShell: the screen copied with .NET, the mouse and keys through user32.
//   Linux    xdotool and import/scrot/gnome-screenshot, when installed.

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func screenRun(ctx context.Context, name string, args ...string) (string, error) {
	c, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(c, name, args...)
	hideWindow(cmd)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	if err != nil {
		return out.String(), fmt.Errorf("%s: %v %s", name, err, strings.TrimSpace(out.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

func powershell(ctx context.Context, script string) (string, error) {
	return screenRun(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script)
}

const psUser32 = `Add-Type -Name U -Namespace P -MemberDefinition '[DllImport("user32.dll")] public static extern bool SetCursorPos(int x,int y); [DllImport("user32.dll")] public static extern void mouse_event(int f,int x,int y,int d,int e); [DllImport("user32.dll")] public static extern bool SetProcessDPIAware();'; [P.U]::SetProcessDPIAware() | Out-Null; `

// screenshot saves the screen as a PNG whose pixels are the screen's click coordinates, and
// returns its path with the screen's size.
func screenshot(ctx context.Context, dir string) (string, int, int, error) {
	_ = os.MkdirAll(dir, 0o700)
	path := filepath.Join(dir, "screen-"+time.Now().Format("150405")+".png")
	switch runtime.GOOS {
	case "darwin":
		if _, err := screenRun(ctx, "screencapture", "-x", "-C", "-t", "png", path); err != nil {
			return "", 0, 0, fmt.Errorf("could not take a screenshot (macOS asks for Screen Recording permission the first time): %w", err)
		}
		// The screen in points (what clicks use); a Retina screenshot has twice as many pixels.
		pts, err := screenRun(ctx, "osascript", "-l", "JavaScript", "-e", `ObjC.import("AppKit"); const f = $.NSScreen.mainScreen.frame; Math.round(f.size.width) + " " + Math.round(f.size.height)`)
		if err == nil {
			if f := strings.Fields(pts); len(f) == 2 {
				w, _ := strconv.Atoi(f[0])
				h, _ := strconv.Atoi(f[1])
				if w > 0 && h > 0 {
					_, _ = screenRun(ctx, "sips", "-z", strconv.Itoa(h), strconv.Itoa(w), path)
				}
			}
		}
	case "windows":
		ps := psUser32 + `Add-Type -AssemblyName System.Windows.Forms,System.Drawing; $b=[System.Windows.Forms.Screen]::PrimaryScreen.Bounds; $i=New-Object System.Drawing.Bitmap $b.Width,$b.Height; $g=[System.Drawing.Graphics]::FromImage($i); $g.CopyFromScreen($b.Location,[System.Drawing.Point]::Empty,$b.Size); $i.Save('` + path + `',[System.Drawing.Imaging.ImageFormat]::Png)`
		if _, err := powershell(ctx, ps); err != nil {
			return "", 0, 0, err
		}
	default:
		var err error
		for _, try := range [][]string{{"import", "-window", "root", path}, {"scrot", "-o", path}, {"gnome-screenshot", "-f", path}} {
			if _, e := exec.LookPath(try[0]); e != nil {
				continue
			}
			if _, err = screenRun(ctx, try[0], try[1:]...); err == nil {
				break
			}
		}
		if _, e := os.Stat(path); e != nil {
			return "", 0, 0, errors.New("no screenshot tool here: install ImageMagick (import), scrot or gnome-screenshot")
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return "", 0, 0, err
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return "", 0, 0, err
	}
	return path, cfg.Width, cfg.Height, nil
}

// jxaMouse posts mouse events at a point through CoreGraphics (macOS).
func jxaMouse(ctx context.Context, x, y int, kind string, clicks int) error {
	js := fmt.Sprintf(`ObjC.import("CoreGraphics"); const p = {x: %d, y: %d};
const post = (t, b) => $.CGEventPost($.kCGHIDEventTap, $.CGEventCreateMouseEvent(null, t, p, b));
const k = %q, n = %d;
if (k === "move") post($.kCGEventMouseMoved, 0);
else for (let i = 0; i < n; i++) {
  if (k === "right") { post($.kCGEventRightMouseDown, 1); post($.kCGEventRightMouseUp, 1); }
  else { const d = $.CGEventCreateMouseEvent(null, $.kCGEventLeftMouseDown, p, 0), u = $.CGEventCreateMouseEvent(null, $.kCGEventLeftMouseUp, p, 0);
    $.CGEventSetIntegerValueField(d, $.kCGMouseEventClickState, i + 1); $.CGEventSetIntegerValueField(u, $.kCGMouseEventClickState, i + 1);
    $.CGEventPost($.kCGHIDEventTap, d); $.CGEventPost($.kCGHIDEventTap, u); }
}
"ok"`, x, y, kind, clicks)
	_, err := screenRun(ctx, "osascript", "-l", "JavaScript", "-e", js)
	return err
}

var keyCombo = regexp.MustCompile(`^((?:cmd|command|ctrl|control|alt|option|shift|win)\+)*[A-Za-z0-9]+$|^((?:cmd|command|ctrl|control|alt|option|shift|win)\+)*(enter|return|tab|escape|esc|space|backspace|delete|up|down|left|right|home|end|pageup|pagedown|f\d{1,2})$`)

// screenTool runs one of the agent's screen tools.
func screenTool(ctx context.Context, data, name string, args map[string]any) (string, string, error) {
	num := func(k string) int { v, _ := args[k].(float64); return int(v) }
	str := func(k string) string { v, _ := args[k].(string); return v }
	x, y := num("x"), num("y")
	switch name {
	case "screen_look":
		path, w, h, err := screenshot(ctx, filepath.Join(data, "screen-shots"))
		if err != nil {
			return "", "", err
		}
		return fmt.Sprintf("Here is the screen, %d by %d points; click at the points you see.", w, h), path, nil
	case "screen_click":
		clicks := num("clicks")
		if clicks < 1 {
			clicks = 1
		}
		button := str("button")
		switch runtime.GOOS {
		case "darwin":
			kind := "left"
			if button == "right" {
				kind = "right"
			}
			if err := jxaMouse(ctx, x, y, kind, clicks); err != nil {
				return "", "", fmt.Errorf("could not click (macOS asks for Accessibility permission the first time): %w", err)
			}
		case "windows":
			down, up := 0x02, 0x04
			if button == "right" {
				down, up = 0x08, 0x10
			}
			ps := psUser32 + fmt.Sprintf(`[P.U]::SetCursorPos(%d,%d) | Out-Null; for($i=0;$i -lt %d;$i++){ [P.U]::mouse_event(%d,0,0,0,0); [P.U]::mouse_event(%d,0,0,0,0) }`, x, y, clicks, down, up)
			if _, err := powershell(ctx, ps); err != nil {
				return "", "", err
			}
		default:
			b := "1"
			if button == "right" {
				b = "3"
			}
			if _, err := screenRun(ctx, "xdotool", "mousemove", strconv.Itoa(x), strconv.Itoa(y), "click", "--repeat", strconv.Itoa(clicks), b); err != nil {
				return "", "", err
			}
		}
		time.Sleep(400 * time.Millisecond)
		return fmt.Sprintf("Clicked at %d, %d. Look at the screen again to see what happened.", x, y), "", nil
	case "screen_type":
		text := str("text")
		switch runtime.GOOS {
		case "darwin":
			if _, err := screenRun(ctx, "osascript", "-e", `on run argv`, "-e", `tell application "System Events" to keystroke (item 1 of argv)`, "-e", `end run`, text); err != nil {
				return "", "", err
			}
		case "windows":
			esc := regexp.MustCompile(`([+^%~(){}\[\]])`).ReplaceAllString(text, "{$1}")
			if _, err := powershell(ctx, `Add-Type -AssemblyName System.Windows.Forms; [System.Windows.Forms.SendKeys]::SendWait('`+strings.ReplaceAll(esc, "'", "''")+`')`); err != nil {
				return "", "", err
			}
		default:
			if _, err := screenRun(ctx, "xdotool", "type", "--", text); err != nil {
				return "", "", err
			}
		}
		return "Typed it.", "", nil
	case "screen_key":
		k := strings.ToLower(strings.ReplaceAll(str("keys"), " ", ""))
		if !keyCombo.MatchString(k) {
			return "", "", fmt.Errorf("give keys like enter, tab, cmd+s or ctrl+shift+t (not %q)", str("keys"))
		}
		parts := strings.Split(k, "+")
		key, mods := parts[len(parts)-1], parts[:len(parts)-1]
		switch runtime.GOOS {
		case "darwin":
			codes := map[string]int{"enter": 36, "return": 36, "tab": 48, "space": 49, "backspace": 51, "delete": 117, "escape": 53, "esc": 53, "left": 123, "right": 124, "down": 125, "up": 126, "home": 115, "end": 119, "pageup": 116, "pagedown": 121}
			var ms []string
			for _, m := range mods {
				ms = append(ms, map[string]string{"cmd": "command down", "command": "command down", "ctrl": "control down", "control": "control down", "alt": "option down", "option": "option down", "shift": "shift down"}[m])
			}
			using := ""
			if len(ms) > 0 {
				using = " using {" + strings.Join(ms, ", ") + "}"
			}
			cmd := `tell application "System Events" to keystroke "` + key + `"` + using
			if c, ok := codes[key]; ok {
				cmd = `tell application "System Events" to key code ` + strconv.Itoa(c) + using
			}
			if _, err := screenRun(ctx, "osascript", "-e", cmd); err != nil {
				return "", "", err
			}
		case "windows":
			names := map[string]string{"enter": "{ENTER}", "return": "{ENTER}", "tab": "{TAB}", "space": " ", "backspace": "{BACKSPACE}", "delete": "{DELETE}", "escape": "{ESC}", "esc": "{ESC}", "left": "{LEFT}", "right": "{RIGHT}", "up": "{UP}", "down": "{DOWN}", "home": "{HOME}", "end": "{END}", "pageup": "{PGUP}", "pagedown": "{PGDN}"}
			seq := key
			if n, ok := names[key]; ok {
				seq = n
			} else if strings.HasPrefix(key, "f") && len(key) > 1 {
				seq = "{" + strings.ToUpper(key) + "}"
			}
			for _, m := range mods {
				seq = map[string]string{"ctrl": "^", "control": "^", "alt": "%", "option": "%", "shift": "+", "cmd": "^", "command": "^", "win": "^{ESC}"}[m] + seq
			}
			if _, err := powershell(ctx, `Add-Type -AssemblyName System.Windows.Forms; [System.Windows.Forms.SendKeys]::SendWait('`+seq+`')`); err != nil {
				return "", "", err
			}
		default:
			if _, err := screenRun(ctx, "xdotool", "key", strings.ReplaceAll(strings.ReplaceAll(k, "cmd", "super"), "enter", "Return")); err != nil {
				return "", "", err
			}
		}
		return "Pressed " + str("keys") + ".", "", nil
	case "screen_scroll":
		amount := num("amount")
		if amount == 0 {
			amount = 5
		}
		switch runtime.GOOS {
		case "darwin":
			if err := jxaMouse(ctx, x, y, "move", 0); err != nil {
				return "", "", err
			}
			js := fmt.Sprintf(`ObjC.import("CoreGraphics"); $.CGEventPost($.kCGHIDEventTap, $.CGEventCreateScrollWheelEvent(null, 0, 1, %d)); "ok"`, -amount)
			if _, err := screenRun(ctx, "osascript", "-l", "JavaScript", "-e", js); err != nil {
				return "", "", err
			}
		case "windows":
			ps := psUser32 + fmt.Sprintf(`[P.U]::SetCursorPos(%d,%d) | Out-Null; [P.U]::mouse_event(0x0800,0,0,%d,0)`, x, y, -amount*120)
			if _, err := powershell(ctx, ps); err != nil {
				return "", "", err
			}
		default:
			b := "5"
			if amount < 0 {
				b, amount = "4", -amount
			}
			if _, err := screenRun(ctx, "xdotool", "mousemove", strconv.Itoa(x), strconv.Itoa(y), "click", "--repeat", strconv.Itoa(amount), b); err != nil {
				return "", "", err
			}
		}
		return "Scrolled.", "", nil
	}
	return "", "", fmt.Errorf("unknown screen tool %s", name)
}

// imageDataURI reads a picture as a data: address, for a model that can see.
func imageDataURI(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(b), nil
}
