//go:build windows && desktop

// The Windows app: Potluck-windows.exe installs itself for this person (no administrator
// needed), adds Start menu and desktop shortcuts and an entry in Settings > Apps, then runs
// with an icon by the clock and its own window. Built with:  -tags desktop -H windowsgui

package main

import (
	_ "embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"fyne.io/systray"
	webview2 "github.com/jchv/go-webview2"
	"golang.org/x/sys/windows/registry"
)

//go:embed winres/potluck.ico
var trayIcon []byte

const uninstallKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\Potluck`

func init() {
	desktopBefore = winBefore
	desktopRun = winRun
	desktopShow = winShow
}

func installDir() string {
	return filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "Potluck")
}

// winBefore installs or uninstalls when needed. It returns true when this process is done.
func winBefore() bool {
	if len(os.Args) > 1 && os.Args[1] == "uninstall" {
		winUninstall()
		return true
	}
	self, _ := os.Executable()
	self, _ = filepath.EvalSymlinks(self)
	dest := filepath.Join(installDir(), "Potluck.exe")
	if !strings.EqualFold(filepath.Clean(self), filepath.Clean(dest)) && os.Getenv("POTLUCK_NO_INSTALL") == "" {
		if err := winInstall(self, dest); err != nil {
			winMessage("Potluck could not install itself", err.Error()+"\n\nIt will run from where it is instead.")
			return false
		}
		cmd := exec.Command(dest, os.Args[1:]...)
		cmd.Dir = installDir()
		if err := cmd.Start(); err != nil {
			winMessage("Potluck could not start", err.Error())
		}
		return true
	}
	// No console in the Windows app: what Potluck prints goes to its log.
	if dir := defaultDataDir(); dir != "" {
		_ = os.MkdirAll(filepath.Join(dir, "logs"), 0o755)
		if f, err := os.OpenFile(filepath.Join(dir, "logs", "potluck-app.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			os.Stdout, os.Stderr = f, f
		}
	}
	return false
}

func winInstall(self, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	// An older Potluck may be running from there: ask it to quit so the file can be replaced.
	_ = exec.Command("taskkill", "/IM", "Potluck.exe", "/F").Run()
	time.Sleep(500 * time.Millisecond)
	data, err := os.ReadFile(self)
	if err != nil {
		return err
	}
	tmp := dest + ".new"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return err
	}
	_ = os.Remove(dest)
	if err := os.Rename(tmp, dest); err != nil {
		return err
	}
	// Shortcuts in the Start menu and on the desktop.
	start := filepath.Join(os.Getenv("APPDATA"), `Microsoft\Windows\Start Menu\Programs\Potluck.lnk`)
	desk := filepath.Join(os.Getenv("USERPROFILE"), `Desktop\Potluck.lnk`)
	for _, l := range []string{start, desk} {
		ps := fmt.Sprintf(`$s=(New-Object -ComObject WScript.Shell).CreateShortcut('%s');$s.TargetPath='%s';$s.WorkingDirectory='%s';$s.IconLocation='%s,0';$s.Description='Private AI on the computers you already own';$s.Save()`,
			psq(l), psq(dest), psq(filepath.Dir(dest)), psq(dest))
		c := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", ps)
		c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		_ = c.Run()
	}
	// An entry in Settings > Apps, so Potluck can be removed like any other app.
	k, _, err := registry.CreateKey(registry.CURRENT_USER, uninstallKey, registry.SET_VALUE)
	if err == nil {
		_ = k.SetStringValue("DisplayName", "Potluck")
		_ = k.SetStringValue("DisplayVersion", version)
		_ = k.SetStringValue("Publisher", "Potluck")
		_ = k.SetStringValue("DisplayIcon", dest)
		_ = k.SetStringValue("InstallLocation", filepath.Dir(dest))
		_ = k.SetStringValue("UninstallString", `"`+dest+`" uninstall`)
		_ = k.SetDWordValue("NoModify", 1)
		_ = k.SetDWordValue("NoRepair", 1)
		k.Close()
	}
	return nil
}

func psq(s string) string { return strings.ReplaceAll(s, "'", "''") }

func winUninstall() {
	if !winAsk("Remove Potluck?", "Potluck and its shortcuts will be removed. Your models and settings stay in "+defaultDataDir()+" until you delete that folder.") {
		return
	}
	_ = os.Remove(filepath.Join(os.Getenv("APPDATA"), `Microsoft\Windows\Start Menu\Programs\Potluck.lnk`))
	_ = os.Remove(filepath.Join(os.Getenv("USERPROFILE"), `Desktop\Potluck.lnk`))
	_ = registry.DeleteKey(registry.CURRENT_USER, uninstallKey)
	// The program can't delete itself while it runs: a moment after it quits, cmd does it.
	c := exec.Command("cmd", "/C", "ping -n 4 127.0.0.1 >NUL & rmdir /S /Q \""+installDir()+"\"")
	// Detached and out of any job, so it outlives this process and whatever started it.
	c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x01000000 | 0x00000008}
	if c.Start() != nil { // a job that forbids leaving it: start it plainly instead
		c = exec.Command(c.Path, c.Args[1:]...)
		c.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x00000008}
		_ = c.Start()
	}
	_ = exec.Command("taskkill", "/IM", "Potluck.exe", "/F").Start()
}

var (
	winMu   sync.Mutex
	winOpen bool
)

// winRun puts Potluck's icon by the clock and opens its window.
func winRun(ui string) {
	go func() {
		runtime.LockOSThread()
		systray.Run(func() {
			systray.SetIcon(trayIcon)
			systray.SetTooltip("Potluck")
			open := systray.AddMenuItem("Open Potluck", "Show the Potluck window")
			systray.AddSeparator()
			quit := systray.AddMenuItem("Quit Potluck", "Stop Potluck on this computer")
			go func() {
				for {
					select {
					case <-open.ClickedCh:
						winShow(ui)
					case <-quit.ClickedCh:
						systray.Quit()
						os.Exit(0)
					}
				}
			}()
		}, nil)
	}()
	time.Sleep(700 * time.Millisecond)
	winShow(ui)
}

// winShow opens Potluck's window (one at a time). Without the WebView2 runtime, which comes
// with Windows 10 and 11, it falls back to the browser.
func winShow(ui string) {
	winMu.Lock()
	if winOpen {
		winMu.Unlock()
		return
	}
	winOpen = true
	winMu.Unlock()
	go func() {
		runtime.LockOSThread()
		defer func() { winMu.Lock(); winOpen = false; winMu.Unlock() }()
		w := webview2.NewWithOptions(webview2.WebViewOptions{
			AutoFocus: true,
			DataPath:  filepath.Join(defaultDataDir(), "window"),
			WindowOptions: webview2.WindowOptions{
				Title: "Potluck", Width: 1180, Height: 820, IconId: 1, Center: true,
			},
		})
		if w == nil {
			openBrowser(ui)
			return
		}
		defer w.Destroy()
		w.Navigate(ui)
		w.Run()
	}()
}

var (
	user32     = syscall.NewLazyDLL("user32.dll")
	messageBox = user32.NewProc("MessageBoxW")
)

func winMessage(title, text string) {
	t, _ := syscall.UTF16PtrFromString(title)
	m, _ := syscall.UTF16PtrFromString(text)
	messageBox.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), 0x40)
}

func winAsk(title, text string) bool {
	if os.Getenv("POTLUCK_ASSUME_YES") != "" { // for scripted removal
		return true
	}
	t, _ := syscall.UTF16PtrFromString(title)
	m, _ := syscall.UTF16PtrFromString(text)
	r, _, _ := messageBox.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), 0x24) // yes/no, question
	return r == 6                                                                              // IDYES
}
