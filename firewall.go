package main

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const macFirewall = "/usr/libexec/ApplicationFirewall/socketfilterfw"

// firewallState says whether this computer's firewall lets other computers reach Potluck:
// "ok", "blocked" or "unknown". Only the main computer needs this; helpers connect outwards.
func firewallState() string {
	exe, _ := os.Executable()
	switch runtime.GOOS {
	case "darwin":
		if !strings.Contains(run(3*time.Second, macFirewall, "--getglobalstate"), "enabled") {
			return "ok"
		}
		// The firewall silently drops connections to apps that are not on its allow list
		// (and its own "is this app blocked?" answer says "permitted" for unlisted apps).
		list := run(4*time.Second, macFirewall, "--listapps")
		if i := strings.Index(list, exe); i >= 0 {
			rest := list[i+len(exe):]
			if len(rest) > 80 {
				rest = rest[:80]
			}
			if strings.Contains(rest, "Allow") {
				return "ok"
			}
		}
		return "blocked"
	case "windows":
		out := run(8*time.Second, "netsh", "advfirewall", "firewall", "show", "rule", "name=Potluck")
		if strings.Contains(out, "Potluck") {
			return "ok"
		}
		return "unknown"
	}
	return "unknown"
}

// allowThroughFirewall asks the operating system, with its own password or permission
// dialog, to let other computers connect to Potluck.
func allowThroughFirewall() error {
	exe, _ := os.Executable()
	switch runtime.GOOS {
	case "darwin":
		q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
		sh := macFirewall + " --add " + q(exe) + " && " + macFirewall + " --unblockapp " + q(exe)
		script := `do shell script "` + strings.ReplaceAll(strings.ReplaceAll(sh, `\`, `\\`), `"`, `\"`) +
			`" with prompt "Potluck needs your permission so your other computers can connect to this one." with administrator privileges`
		out, err := exec.Command("osascript", "-e", script).CombinedOutput()
		if err != nil {
			if strings.Contains(string(out), "-128") {
				return errors.New("you cancelled the password dialog")
			}
			return errors.New("macOS did not allow it: " + strings.TrimSpace(string(out)))
		}
		return nil
	case "windows":
		rule := `netsh advfirewall firewall add rule name=Potluck dir=in action=allow enable=yes profile=private,domain program="` + exe + `"`
		ps := `Start-Process -FilePath cmd.exe -ArgumentList '/c ` + strings.ReplaceAll(rule, "'", "''") + `' -Verb RunAs -WindowStyle Hidden -Wait`
		cmd := exec.Command("powershell", "-NoProfile", "-Command", ps)
		hideWindow(cmd)
		if out, err := cmd.CombinedOutput(); err != nil {
			return errors.New("Windows did not allow it: " + strings.TrimSpace(string(out)))
		}
		return nil
	}
	return errors.New("on this system, open ports 7715 and 7717 in your firewall by hand")
}
