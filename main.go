// Potluck: run one large AI model across the spare computers you already own.
package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var version = "0.1.0"

func defaultDataDir() string {
	if d := os.Getenv("POTLUCK_DATA"); d != "" {
		return d
	}
	if runtime.GOOS == "windows" {
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "Potluck")
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "potluck-data"
	}
	return filepath.Join(home, "Potluck")
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

// The desktop apps (desktop_windows.go) hook in here: desktopBefore installs the app and may
// end this process; desktopRun shows the app's own window instead of a browser tab;
// desktopShow opens that window when Potluck is already running.
var (
	desktopBefore func() bool
	desktopRun    func(ui string)
	desktopShow   func(ui string)
)

func main() {
	if desktopBefore != nil && desktopBefore() {
		return
	}
	role := ""
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		role, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet("potluck", flag.ExitOnError)
	dataDir := fs.String("data", defaultDataDir(), "folder for everything Potluck stores")
	noBrowser := fs.Bool("no-browser", false, "do not open the control panel in a browser")
	name := fs.String("name", "", "name shown for this computer")
	tokenStr := fs.String("token", "", "join code from the main computer's join page")
	tokenFile := fs.String("token-file", "", "join file from a USB kit")
	mainAddr := fs.String("main", "", "address of the main computer, when it is not found automatically")
	_ = fs.Parse(args)
	// Potluck's folder may have been moved (the helper's set_data_folder): follow the pointer.
	*dataDir = followMoved(*dataDir)

	switch role {
	case "version":
		fmt.Println("potluck", version)
		return
	case "bench":
		benchCommand(*dataDir)
		return
	case "", "main", "join", "helper":
	default:
		fmt.Println("Usage: potluck [main | join | bench] [--data DIR] [--name NAME] [--no-browser] [--token CODE] [--main ADDRESS]")
		os.Exit(2)
	}
	if role == "join" {
		role = "helper"
	}

	ui := "http://127.0.0.1:" + strconv.Itoa(portUI)
	// Already running? Just bring up its window.
	if c, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(portUI), 400*time.Millisecond); err == nil {
		c.Close()
		if res, err := (&http.Client{Timeout: 2 * time.Second}).Get(ui + "/api/state"); err == nil {
			res.Body.Close()
			fmt.Println("Potluck is already running. Opening its control panel.")
			if desktopShow != nil {
				desktopShow(ui)
				select {} // the window keeps this process; closing it ends it
			} else if !*noBrowser {
				openBrowser(ui)
			}
			return
		}
	}

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		fmt.Println("Cannot create the data folder:", err)
		os.Exit(1)
	}
	id, err := loadOrCreateIdentity(*dataDir)
	if err != nil {
		fmt.Println("Cannot create this computer's identity:", err)
		os.Exit(1)
	}
	cfg := loadConfig(*dataDir)
	if *name != "" {
		cfg.Update(func(c *Config) { c.Name = *name })
	}
	app := &App{dir: *dataDir, id: id, cfg: cfg}
	if *tokenStr != "" {
		if t, err := parseCompactToken(*tokenStr); err == nil {
			app.token = t
		} else {
			fmt.Println(err)
		}
	}
	if app.token == nil && *tokenFile != "" {
		app.token = readTokenFile(*tokenFile)
	}
	if app.token == nil {
		if t := readTokenFile(filepath.Join(*dataDir, "potluck-join.json")); t != nil {
			app.token = t
		} else {
			app.token = findToken()
		}
	}
	fmt.Println("Potluck", version, "- reading this computer's hardware...")
	app.agent = newAgent(*dataDir, id, cfg)

	if role == "" {
		role = cfg.Get().Role
	}
	if role == "" && app.token != nil {
		role = "helper" // started from a USB kit or a join download
	}
	if role != "" {
		if err := app.setRole(role); err != nil {
			fmt.Println("Could not start:", err)
			os.Exit(1)
		}
		if role == "helper" && *mainAddr != "" {
			app.helper.SetManual(*mainAddr)
		}
	}
	fmt.Println("Control panel:", ui)
	fmt.Println("Data folder:  ", *dataDir)
	fmt.Println("Leave this window open. Close it to stop Potluck on this computer.")
	if desktopRun != nil {
		go desktopRun(ui)
	} else if !*noBrowser {
		go func() { time.Sleep(600 * time.Millisecond); openBrowser(ui) }()
	}
	if err := app.serveUI(); err != nil {
		fmt.Println("Could not open the control panel:", err)
		os.Exit(1)
	}
}
