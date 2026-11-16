package main

import (
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// Platforms a setup kit carries a program for.
var platforms = []struct{ OS, Arch, Label string }{
	{"windows", "amd64", "Windows"},
	{"windows", "arm64", "Windows on Arm"},
	{"darwin", "arm64", "Mac (Apple Silicon)"},
	{"darwin", "amd64", "Mac (Intel)"},
	{"linux", "amd64", "Linux"},
	{"linux", "arm64", "Linux on Arm"},
}

func binName(goos, goarch string) string {
	n := "potluck-" + goos + "-" + goarch
	if goos == "windows" {
		n += ".exe"
	}
	return n
}

// findBinary locates the Potluck program for another kind of computer: next to this one,
// in a "dist" or "kit" folder beside it, or this program itself for its own platform.
func findBinary(goos, goarch string) string {
	exe, _ := os.Executable()
	dir := filepath.Dir(exe)
	for _, d := range []string{dir, filepath.Join(dir, "dist"), filepath.Join(dir, "kit")} {
		p := filepath.Join(d, binName(goos, goarch))
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	if goos == runtime.GOOS && goarch == runtime.GOARCH {
		return exe
	}
	return ""
}

// compactToken packs "where is the main computer and how do I prove I may join" into 48
// letters, short enough to ride in a file name.
func compactToken(ip string, fp, secret string) string {
	raw := make([]byte, 0, 30)
	raw = append(raw, net.ParseIP(ip).To4()...)
	raw = append(raw, byte(portLink>>8), byte(portLink&0xff))
	f, _ := hex.DecodeString(fp)
	raw = append(raw, f[:16]...)
	s, _ := hex.DecodeString(secret)
	raw = append(raw, s...)
	return strings.ToLower(b32.EncodeToString(raw))
}

var tokenInName = regexp.MustCompile(`join-([a-z2-7]{48})`)

func parseCompactToken(t string) (*JoinToken, error) {
	raw, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(t)))
	if err != nil || len(raw) != 30 {
		return nil, errors.New("that join code is not valid")
	}
	return &JoinToken{
		Addrs:  []string{net.IP(raw[:4]).String()},
		Port:   int(raw[4])<<8 | int(raw[5]),
		FP:     hex.EncodeToString(raw[6:22]),
		Secret: hex.EncodeToString(raw[22:30]),
	}, nil
}

// findToken looks for join details that came along with the program: in its own file name
// (downloaded from the main computer's join page) or in a file beside it (USB kit).
func findToken() *JoinToken {
	exe, _ := os.Executable()
	if m := tokenInName.FindStringSubmatch(strings.ToLower(filepath.Base(exe))); m != nil {
		if t, err := parseCompactToken(m[1]); err == nil {
			return t
		}
	}
	for _, p := range []string{filepath.Join(filepath.Dir(exe), "potluck-join.json"), "potluck-join.json"} {
		if t := readTokenFile(p); t != nil {
			return t
		}
	}
	return nil
}

func readTokenFile(path string) *JoinToken {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var t JoinToken
	if json.Unmarshal(b, &t) != nil || t.FP == "" || len(t.Addrs) == 0 {
		return nil
	}
	return &t
}

func (h *Host) joinToken() JoinToken {
	return JoinToken{Addrs: localIPs(), Port: portLink, FP: h.id.FP, Secret: h.cfg.Get().JoinSecret, Name: h.selfName()}
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// MakeKit writes a "Potluck" folder onto a USB stick (or any folder): the program for every
// kind of computer, the join details, and one thing to double-click per system. Plug the
// stick into a spare computer, double-click, done. No typing, no accounts, no internet needed
// for the pairing itself.
func (h *Host) MakeKit(dest string) (string, []string, error) {
	dest = strings.TrimSpace(dest)
	if dest == "" {
		return "", nil, errors.New("choose a drive or folder first")
	}
	if st, err := os.Stat(dest); err != nil || !st.IsDir() {
		return "", nil, errors.New("that drive or folder does not exist")
	}
	dir := filepath.Join(dest, "Potluck")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, fmt.Errorf("could not write there: %w", err)
	}
	var included, missing []string
	for _, p := range platforms {
		src := findBinary(p.OS, p.Arch)
		if src == "" {
			missing = append(missing, p.Label)
			continue
		}
		if err := copyFile(src, filepath.Join(dir, binName(p.OS, p.Arch)), 0o755); err != nil {
			return "", nil, err
		}
		included = append(included, p.Label)
	}
	if len(included) == 0 {
		return "", nil, errors.New("the helper programs are missing next to this app; download the full Potluck package")
	}
	tok, _ := json.MarshalIndent(h.joinToken(), "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "potluck-join.json"), tok, 0o644); err != nil {
		return "", nil, err
	}
	files := map[string]string{
		"Join on Windows.bat": "@echo off\r\nrem Potluck: add this computer to your setup.\r\nset \"DEST=%LOCALAPPDATA%\\Potluck\"\r\nif not exist \"%DEST%\" mkdir \"%DEST%\"\r\n" +
			"set \"BIN=potluck-windows-amd64.exe\"\r\nif /i \"%PROCESSOR_ARCHITECTURE%\"==\"ARM64\" set \"BIN=potluck-windows-arm64.exe\"\r\n" +
			"copy /y \"%~dp0%BIN%\" \"%DEST%\\potluck.exe\" >nul\r\ncopy /y \"%~dp0potluck-join.json\" \"%DEST%\\potluck-join.json\" >nul\r\n" +
			"start \"Potluck\" \"%DEST%\\potluck.exe\" join\r\n",
		"Join on Mac.command": "#!/bin/sh\n# Potluck: add this computer to your setup.\nHERE=\"$(cd \"$(dirname \"$0\")\" && pwd)\"\nDEST=\"$HOME/Potluck\"\nmkdir -p \"$DEST\"\n" +
			"case \"$(uname -m)\" in arm64) BIN=potluck-darwin-arm64 ;; *) BIN=potluck-darwin-amd64 ;; esac\n" +
			"cp \"$HERE/$BIN\" \"$DEST/potluck\" && chmod +x \"$DEST/potluck\"\ncp \"$HERE/potluck-join.json\" \"$DEST/potluck-join.json\"\n" +
			"xattr -d com.apple.quarantine \"$DEST/potluck\" 2>/dev/null\nexec \"$DEST/potluck\" join\n",
		"join-linux.sh": "#!/bin/sh\n# Potluck: add this computer to your setup.  Run:  sh join-linux.sh\nHERE=\"$(cd \"$(dirname \"$0\")\" && pwd)\"\nDEST=\"$HOME/Potluck\"\nmkdir -p \"$DEST\"\n" +
			"case \"$(uname -m)\" in aarch64|arm64) BIN=potluck-linux-arm64 ;; *) BIN=potluck-linux-amd64 ;; esac\n" +
			"cp \"$HERE/$BIN\" \"$DEST/potluck\" && chmod +x \"$DEST/potluck\"\ncp \"$HERE/potluck-join.json\" \"$DEST/potluck-join.json\"\nexec \"$DEST/potluck\" join\n",
		"READ ME.txt": "Potluck setup kit\r\n=================\r\n\r\nPlug this stick into a spare computer on the same network as your main computer (" + h.selfName() + "), then:\r\n\r\n" +
			"  Windows:  double-click \"Join on Windows\"\r\n  Mac:      double-click \"Join on Mac\"\r\n  Linux:    open a terminal in this folder and run:  sh join-linux.sh\r\n\r\n" +
			"The computer copies Potluck to itself and shows up in the control panel on your main computer within a few seconds.\r\nNothing to type, no account, no password.\r\n\r\n" +
			"Keep this stick private: anyone who has it can add a computer to your setup.\r\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			return "", nil, err
		}
	}
	return dir, missing, nil
}

// kitDrives guesses where USB sticks are mounted.
func kitDrives() []string {
	var out []string
	switch runtime.GOOS {
	case "darwin":
		ents, _ := os.ReadDir("/Volumes")
		for _, e := range ents {
			p := filepath.Join("/Volumes", e.Name())
			if real, err := filepath.EvalSymlinks(p); err == nil && real != "/" {
				out = append(out, p)
			}
		}
	case "windows":
		for c := 'D'; c <= 'Z'; c++ {
			p := string(c) + `:\`
			if _, err := os.Stat(p); err == nil {
				out = append(out, p)
			}
		}
	default:
		for _, pat := range []string{"/media/*/*", "/run/media/*/*", "/mnt/*"} {
			m, _ := filepath.Glob(pat)
			out = append(out, m...)
		}
	}
	return out
}

// serveJoinPage runs the tiny "add this computer" web page for the local network. On the
// spare computer you open http://<main computer's address>:7715 and click one button; the
// download carries the join details in its file name.
func (h *Host) serveJoinPage() {
	mux := http.NewServeMux()
	localIP := func(r *http.Request) string {
		if a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
			if host, _, err := net.SplitHostPort(a.String()); err == nil && net.ParseIP(host).To4() != nil {
				return host
			}
		}
		if ips := localIPs(); len(ips) > 0 {
			return ips[0]
		}
		return "127.0.0.1"
	}
	token := func(r *http.Request) string { return compactToken(localIP(r), h.id.FP, h.cfg.Get().JoinSecret) }
	mux.HandleFunc("/get/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/get/"), "-")
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		src := findBinary(parts[0], parts[1])
		if src == "" {
			http.Error(w, "The program for that kind of computer is not on the main computer. Use the full Potluck package.", http.StatusNotFound)
			return
		}
		name := "potluck-join-" + token(r)
		if parts[0] == "windows" {
			name += ".exe"
		}
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeFile(w, r, src)
	})
	mux.HandleFunc("/sh", func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + net.JoinHostPort(localIP(r), strconv.Itoa(portJoin))
		fmt.Fprintf(w, "#!/bin/sh\n# Potluck: add this computer to the setup run by %q.\nset -e\nOS=$(uname -s | tr A-Z a-z)\ncase \"$(uname -m)\" in arm64|aarch64) ARCH=arm64 ;; *) ARCH=amd64 ;; esac\n"+
			"DEST=\"$HOME/Potluck\"\nmkdir -p \"$DEST\"\n(curl -fsSL \"%s/get/$OS-$ARCH\" -o \"$DEST/potluck\" || wget -qO \"$DEST/potluck\" \"%s/get/$OS-$ARCH\")\nchmod +x \"$DEST/potluck\"\n"+
			"exec \"$DEST/potluck\" join --token %s\n", h.selfName(), base, base, token(r))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		base := "http://" + net.JoinHostPort(localIP(r), strconv.Itoa(portJoin))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, joinPageHTML, htmlEscape(h.selfName()), base)
	})
	srv := &http.Server{Addr: ":" + strconv.Itoa(portJoin), Handler: mux}
	_ = srv.ListenAndServe()
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

const joinPageHTML = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Join Potluck</title><style>
body{font:17px/1.5 system-ui,-apple-system,"Segoe UI",sans-serif;margin:0;background:#f6f3ee;color:#221f1a;display:grid;place-items:center;min-height:100vh}
main{max-width:560px;padding:32px 20px}h1{font-size:28px;margin:0 0 8px}p{margin:8px 0 18px;color:#5b554b}
a.btn{display:block;background:#c2410c;color:#fff;text-decoration:none;font-weight:600;padding:16px 20px;border-radius:12px;text-align:center;margin:10px 0}
a.alt{display:block;text-align:center;color:#8a4a1c;margin:6px 0}code{display:block;background:#221f1a;color:#fde9d9;padding:12px 14px;border-radius:10px;font-size:14px;overflow-wrap:anywhere}
.s{font-size:14px;color:#766f63}@media(prefers-color-scheme:dark){body{background:#191613;color:#f3ede4}p,.s{color:#b3ab9e}code{background:#000}}
</style></head><body><main>
<h1>Add this computer to %[1]s</h1>
<p>One download, one double-click. It finds your main computer by itself.</p>
<a class="btn" id="go" href="#">Download Potluck for this computer</a>
<div id="others"></div>
<p class="s" id="hint"></p>
<p class="s">Mac or Linux and comfortable with a terminal? This does the same thing:</p>
<code>curl -fsSL %[2]s/sh | sh</code>
<script>
var ua=navigator.userAgent,plat=(navigator.userAgentData&&navigator.userAgentData.platform)||navigator.platform||"";
var all=[["windows-amd64","Windows"],["darwin-arm64","Mac with Apple Silicon (M1 or newer)"],["darwin-amd64","Mac with Intel"],["linux-amd64","Linux"],["linux-arm64","Linux on Arm"],["windows-arm64","Windows on Arm"]];
var pick="linux-amd64",hint="After it downloads, open a terminal and run it, or use the command below.";
if(/Win/i.test(plat)||/Windows/i.test(ua)){pick=/ARM64/i.test(ua)?"windows-arm64":"windows-amd64";hint="Open the downloaded file. If Windows shows a blue warning, choose More info, then Run anyway.";}
else if(/Mac/i.test(plat)){pick="darwin-arm64";hint="Not sure which Mac? Apple menu, About This Mac: 'Chip: Apple' means Apple Silicon. Because the app is not signed by Apple yet, the terminal command below is the smoothest way on a Mac.";}
else if(/aarch64|arm64/i.test(ua)){pick="linux-arm64";}
document.getElementById("go").href="/get/"+pick;document.getElementById("hint").textContent=hint;
document.getElementById("others").innerHTML=all.filter(function(a){return a[0]!==pick}).map(function(a){return '<a class="alt" href="/get/'+a[0]+'">'+a[1]+'</a>'}).join("");
</script></main></body></html>`
