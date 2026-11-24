package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The Potluck helper's tools: Potluck's own controls, a few high-level ones (small models
// lose their way among many fine-grained tools). Anything big (gigabytes of download,
// stopping the running model, moving Potluck's folder) is confirmed with the person first.

const helperSystem = `You are the Potluck helper, built into Potluck: a free app that runs AI models privately on the person's own computers, one alone or several pooled together. You set Potluck up for them and answer their questions about it.
Use the tools to look things up and to act: never guess what their computers have, and never make up a folder, drive or address they didn't give you or a tool didn't return.
For anything big (downloading a model, stopping the running model, moving Potluck's folder) the tool itself asks the person to confirm, so just call it.
If you need to know something only the person can tell you, use ask_user. Keep answers short, friendly and plain, with no jargon. When a tool did something, say what happened and what comes next.`

func helperTools() []toolSpec {
	return []toolSpec{
		spec("get_status", "The person's computers in Potluck (how many, each one's memory and graphics, whether it lends) and what Potluck is doing now: the running model, its speed, downloads. Use it for any question about their computers or memory.", `{"type":"object","properties":{}}`),
		spec("list_drives", "The disk drives of this one computer (storage, not memory), their free space, and whether Potluck can store models there (a CD or DVD can't). Use it before moving Potluck to another drive.", `{"type":"object","properties":{}}`),
		spec("set_data_folder", "Move Potluck's models and settings to another drive or folder on this computer, then restart Potluck. Use a drive list_drives returned. Drive letters like D: exist only on Windows; on a Mac, other drives are under /Volumes.",
			`{"type":"object","properties":{"path":{"type":"string","description":"Full folder path, e.g. D:\\Potluck or /Volumes/Big/Potluck"}},"required":["path"]}`),
		spec("recommend_model", "Find models that run on these computers: the best quality that runs, the fastest, or the best for a kind of work. Says how fast each would be.",
			`{"type":"object","properties":{"goal":{"type":"string","enum":["best_quality","fastest","balanced"]},"task":{"type":"string","description":"Optional kind of work: chat, code, writing, pictures, agents"}},"required":["goal"]}`),
		spec("install_model", "Download a model from Hugging Face and start it on these computers (the person confirms first).",
			`{"type":"object","properties":{"repo":{"type":"string","description":"Hugging Face repository, e.g. unsloth/Qwen3.8-27B-GGUF; from recommend_model when you can"},"size":{"type":"string","description":"Optional size such as Q4_K_M; the best fit if left out"}},"required":["repo"]}`),
		spec("stop_model", "Stop the running model and free its memory (the person confirms first).", `{"type":"object","properties":{}}`),
		spec("add_computer_steps", "How to add another computer to Potluck.",
			`{"type":"object","properties":{"how":{"type":"string","enum":["same_network","usb_stick","other_network"],"description":"same_network: it is on the same home or office network as this computer. other_network: it is somewhere else (another building, another office) and joins over the internet. usb_stick: set it up from a USB stick."}}}`),
		spec("pair_phone", "Make the code that pairs an iPhone with Potluck.", `{"type":"object","properties":{}}`),
		spec("add_ai_connection", "Use the models of another AI app or service in Potluck's chat.",
			`{"type":"object","properties":{"app":{"type":"string","enum":["lm_studio","ollama","jan","openrouter","other"]},"base_url":{"type":"string","description":"Only for other: the server's address"},"key":{"type":"string","description":"Only if the service needs a key"}},"required":["app"]}`),
		spec("ask_user", "Ask the person something only they can answer. They reply Yes, No, or in their own words.",
			`{"type":"object","properties":{"question":{"type":"string"}},"required":["question"]}`),
	}
}

// confirm asks the person a yes/no question (in the window, on their phone).
func (st *sessionStore) confirm(ctx context.Context, s *Session, title, detail string) (bool, string, error) {
	s.mu.Lock()
	all := s.Policy.All
	s.mu.Unlock()
	if all {
		return true, "", nil
	}
	ans, err := st.ask(ctx, s, "ask", title, detail)
	if err != nil {
		return false, "", err
	}
	a := strings.ToLower(strings.TrimSpace(ans))
	if a == "yes" || a == "y" || a == "ok" || a == "sure" {
		return true, "", nil
	}
	if a == "no" || a == "n" {
		return false, "", nil
	}
	return false, ans, nil // their own words: the model reads them
}

func (st *sessionStore) runHelperTool(ctx context.Context, s *Session, name string, args map[string]any) (string, error) {
	h := st.host
	str := func(k string) string { v, _ := args[k].(string); return strings.TrimSpace(v) }
	switch name {
	case "get_status":
		return h.helperStatusText(), nil

	case "list_drives":
		return listDrivesText(), nil

	case "set_data_folder":
		path := str("path")
		if path == "" || !filepath.IsAbs(path) {
			return "", errors.New("give a full folder path, e.g. D:\\Potluck or /Volumes/Big/Potluck")
		}
		// The drive itself must already exist: a made-up /Volumes/Big or E:\ must not quietly
		// become a folder on the wrong disk.
		if err := driveExists(path); err != nil {
			return "", fmt.Errorf("%v. The drives here are:\n%s", err, listDrivesText())
		}
		if err := checkWritable(path); err != nil {
			return "", err
		}
		used := dirSize(h.a.dir)
		if free := int64(diskFree(nearestExisting(path))); free < used+(1<<30) {
			return "", fmt.Errorf("that drive has %s free, and Potluck's folder needs %s", humanBytes(free), humanBytes(used))
		}
		ok, words, err := st.confirm(ctx, s, "Move Potluck to "+path+"?",
			fmt.Sprintf("Potluck's models and settings (%s) move from %s to %s. A running model stops, and Potluck restarts there.", humanBytes(used), h.a.dir, path))
		if err != nil {
			return "", err
		}
		if !ok {
			return "Not moved. The person said: " + orNo(words), nil
		}
		if err := h.moveDataFolder(path); err != nil {
			return "", err
		}
		return "Moved. Potluck is restarting from " + path + "; it will be back in a few seconds.", nil

	case "recommend_model":
		return h.recommendText(ctx, str("goal"), str("task"))

	case "install_model":
		repo := str("repo")
		if repo == "" || !strings.Contains(repo, "/") {
			return "", errors.New("name a Hugging Face repository like unsloth/Qwen3.8-27B-GGUF (recommend_model gives them)")
		}
		files, err := hfFiles(repo)
		if err != nil || len(files) == 0 {
			return "", fmt.Errorf("could not find %s on Hugging Face", repo)
		}
		h.judge(files)
		var f StoreFile
		found := false
		if q := str("size"); q != "" {
			for _, x := range files {
				if strings.EqualFold(x.Quant, q) && x.Verdict != "no" {
					f, found = x, true
				}
			}
		}
		if !found {
			f, found = pickQuick(files)
		}
		if !found {
			return "", fmt.Errorf("no size of %s fits these computers", repo)
		}
		how := map[string]string{"one": "on one computer", "pool": "pooled across your computers", "stream": "streaming from disk (slow)"}[f.Verdict]
		ok, words, err := st.confirm(ctx, s, fmt.Sprintf("Download %s (%s) and start it?", repo, humanBytes(f.Size)),
			fmt.Sprintf("The %s size, %s, runs %s. The model that is running now stops first.", f.Quant, humanBytes(f.Size), how))
		if err != nil {
			return "", err
		}
		if !ok {
			return "Not installed. The person said: " + orNo(words), nil
		}
		m := ModelRef{Source: "hf", Repo: repo, File: f.File}
		if err := resolveModel(ctx, &m); err != nil {
			return "", err
		}
		info, err := h.modelInfo(ctx, &m)
		if err != nil {
			return "", err
		}
		info.PlanCtx = 8192
		solo := ""
		if f.Verdict == "one" {
			if p := h.bestSolo(info, ""); p != nil {
				solo = p.Head
			}
		}
		if err := h.Start(m, 8192, solo); err != nil {
			return "", err
		}
		return fmt.Sprintf("Started: downloading %s %s (%s), then loading it %s. This takes a while; get_status shows the progress.", repo, f.Quant, humanBytes(f.Size), how), nil

	case "stop_model":
		h.mu.Lock()
		phase, m := h.cluster.Phase, h.cluster.Model
		h.mu.Unlock()
		if phase == "idle" || m == nil {
			return "No model is running.", nil
		}
		ok, words, err := st.confirm(ctx, s, "Stop "+m.Title+"?", "Its memory is freed. You can start it again any time.")
		if err != nil {
			return "", err
		}
		if !ok {
			return "Left running. The person said: " + orNo(words), nil
		}
		h.Stop()
		return "Stopped " + m.Title + ".", nil

	case "add_computer_steps":
		return h.addComputerText(str("how")), nil

	case "pair_phone":
		r, err := h.PairingCode()
		if err != nil {
			return "", err
		}
		link, _ := r["link"].(string)
		return "Pairing code ready for 10 minutes. On the iPhone, open the Potluck app and scan the code shown in Potluck's control panel (Computers, Your iPhone), or open this link on the phone: " + link, nil

	case "add_ai_connection":
		app := str("app")
		presets := map[string][2]string{"lm_studio": {"LM Studio", "http://127.0.0.1:1234/v1"}, "ollama": {"Ollama", "http://127.0.0.1:11434/v1"},
			"jan": {"Jan", "http://127.0.0.1:1337/v1"}, "openrouter": {"OpenRouter", "https://openrouter.ai/api/v1"}}
		name, base := "", str("base_url")
		if p, ok := presets[app]; ok {
			name = p[0]
			if base == "" {
				base = p[1]
			}
		}
		if base == "" {
			return "", errors.New("ask the person for the server's address")
		}
		c, err := h.AddConnection(name, base, str("key"))
		if err != nil {
			// A plain next step for the person, not an error to retry.
			switch app {
			case "lm_studio":
				return "LM Studio isn't answering on this computer. Tell the person: open LM Studio, go to its Developer tab and switch on Start Server, then ask again. Nothing else to do now.", nil
			case "ollama":
				return "Ollama isn't answering on this computer. Tell the person: open the Ollama app (or run 'ollama serve'), then ask again. Nothing else to do now.", nil
			case "jan":
				return "Jan isn't answering on this computer. Tell the person: open Jan, go to Settings, Local API Server, and start it, then ask again. Nothing else to do now.", nil
			case "openrouter":
				if str("key") == "" {
					return "OpenRouter needs the person's API key (from openrouter.ai, Keys). Ask them for it with ask_user, then call this again with the key.", nil
				}
			}
			return "", err
		}
		ms, _ := connList(c, false)
		return fmt.Sprintf("Connected %s: %d models, now in the chat's model menu.", c.Name, len(ms)), nil
	}
	return "", fmt.Errorf("unknown tool %s", name)
}

func orNo(words string) string {
	if words == "" {
		return "no"
	}
	return words
}

// helperStatusText describes the setup for the helper, in plain words.
func (h *Host) helperStatusText() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var b strings.Builder
	now := time.Now()
	var total int64
	ids := make([]string, 0, len(h.nodes))
	for id := range h.nodes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	fmt.Fprintf(&b, "Computers (%d):\n", len(ids))
	for _, id := range ids {
		n := h.nodes[id]
		i := n.Info
		lend := "lends its memory"
		if !i.Limits.Enabled {
			lend = "does not lend (coordinates only)"
		} else if !i.Limits.ActiveNow(now) {
			lend = "lends only at its set hours, not now"
		}
		gpu := "no usable graphics card"
		if g := i.usableGPU(); g != nil {
			gpu = fmt.Sprintf("%s with %s", g.Name, humanBytes(int64(g.VRAMTotal)))
		}
		state := "online"
		if !n.Online {
			state = "offline"
		}
		main := ""
		if id == h.id.FP {
			main = " (this computer, the main one)"
		}
		cpu, gpuB, _ := offer(i, 0)
		can := cpu + gpuB
		lends := ""
		if i.Limits.Enabled {
			lends = ", can lend " + humanBytes(can) + " to a model"
		}
		fmt.Fprintf(&b, "- %s%s: %s, %s, %s memory (%s free now), %s, %s%s\n", i.Name, main, state, osPretty(i.OS), humanBytes(int64(i.RAMTotal)), humanBytes(int64(i.RAMAvail)), gpu, lend, lends)
		if n.Online && i.Limits.ActiveNow(now) {
			total += can
		}
	}
	fmt.Fprintf(&b, "Memory all the lending computers can give a model together: %s.\n", humanBytes(total))
	c := h.cluster
	switch {
	case c.Model != nil && c.Phase == "running":
		fmt.Fprintf(&b, "Running: %s, %.1f tokens a second.\n", c.Model.Title, c.TPS)
	case c.Model != nil && c.Phase == "starting":
		step := ""
		if len(c.Steps) > 0 {
			step = c.Steps[len(c.Steps)-1].Text
		}
		fmt.Fprintf(&b, "Starting %s: %s.\n", c.Model.Title, step)
	case c.Phase == "error":
		fmt.Fprintf(&b, "The last model stopped with an error: %s\n", c.Error)
	default:
		b.WriteString("No model is running.\n")
	}
	fmt.Fprintf(&b, "Potluck keeps its models in %s.\n", h.a.dir)
	return b.String()
}

func osPretty(os string) string {
	return map[string]string{"darwin": "Mac", "windows": "Windows", "linux": "Linux"}[os]
}

// recommendText: the catalog's picks that run here, best first for the goal.
func (h *Host) recommendText(ctx context.Context, goal, task string) (string, error) {
	cat := h.catalog()
	type opt struct {
		repo  string
		f     StoreFile
		blurb string
	}
	var opts []opt
	for _, p := range cat.Picks {
		if task != "" && !containsFold(p.GoodFor, task) && !strings.Contains(strings.ToLower(p.Blurb), strings.ToLower(task)) {
			continue
		}
		files, err := hfFiles(p.Repo)
		if err != nil {
			continue
		}
		h.judge(files)
		var best StoreFile
		ok := false
		switch goal {
		case "fastest":
			for _, f := range files {
				if f.Verdict == "one" && (!ok || f.Size < best.Size) {
					best, ok = f, true
				}
			}
		case "best_quality":
			// The biggest that runs in memory (pooled counts), up to Q8: past that the gain is tiny.
			for _, f := range files {
				if (f.Verdict == "one" || f.Verdict == "pool") && !strings.Contains(strings.ToUpper(f.Quant), "F16") && !strings.Contains(strings.ToUpper(f.Quant), "BF16") && (!ok || f.Size > best.Size) {
					best, ok = f, true
				}
			}
		default:
			best, ok = pickQuick(files)
		}
		if ok {
			opts = append(opts, opt{p.Repo, best, p.Blurb})
		}
	}
	if len(opts) == 0 {
		return "None of Potluck's picks runs on these computers right now" + map[bool]string{true: " for " + task, false: ""}[task != ""] + ". Adding another computer, or a model search in the control panel, may help.", nil
	}
	if goal == "best_quality" {
		sort.SliceStable(opts, func(i, j int) bool { return opts[i].f.Size > opts[j].f.Size })
	} else if goal == "fastest" {
		sort.SliceStable(opts, func(i, j int) bool { return opts[i].f.Size < opts[j].f.Size })
	}
	var b strings.Builder
	for i, o := range opts {
		if i >= 3 {
			break
		}
		how := map[string]string{"one": "on one computer", "pool": "pooled across computers"}[o.f.Verdict]
		speed := ""
		if o.f.Measured != nil {
			speed = fmt.Sprintf(", measured %.1f tokens/s here", o.f.Measured.Gen)
		} else if len(o.f.Estimates) > 0 {
			e := o.f.Estimates[0]
			speed = fmt.Sprintf(", about %.1f to %.1f tokens/s expected", e.Low, e.High)
		}
		fmt.Fprintf(&b, "%d. %s, size %s (%s), runs %s%s. %s\n", i+1, o.repo, o.f.Quant, humanBytes(o.f.Size), how, speed, o.blurb)
	}
	return b.String(), nil
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if strings.EqualFold(x, s) || strings.Contains(strings.ToLower(s), strings.ToLower(x)) {
			return true
		}
	}
	return false
}

func (h *Host) addComputerText(how string) string {
	ips := localIPs()
	join := ""
	if len(ips) > 0 {
		join = ips[0] + ":" + strconv.Itoa(portJoin)
	}
	var tsIP string
	for _, ip := range ips {
		if strings.HasPrefix(ip, "100.") {
			tsIP = ip
		}
	}
	switch how {
	case "usb_stick":
		return "Plug a USB stick into this computer, then in Potluck's control panel go to Computers, Add a computer, and press Make setup kit. Plug the stick into the other computer and double-click Join there. It appears here by itself; confirm it when asked."
	case "other_network":
		s := "For a computer somewhere else: install Tailscale (free) on both computers and sign in to the same account. Then install Potluck on the other computer and choose \"This is a spare computer\"."
		if tsIP != "" {
			s += " If it doesn't find this one by itself, give it this computer's Tailscale address: " + tsIP + "."
		} else {
			s += " This computer isn't on Tailscale yet."
		}
		return s
	default:
		s := "On the other computer (same network): install Potluck and choose \"This is a spare computer\"; it finds this one by itself and shows a 4-digit number to confirm here."
		if join != "" {
			s += " Or, without installing first: open a web browser there and go to " + join + ", then press the download button and open what it downloads."
		}
		return s
	}
}

// listDrivesText: the drives on this computer, their free space, and whether they can be written.
func listDrivesText() string {
	var paths []string
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "windows":
		for c := 'C'; c <= 'Z'; c++ {
			p := string(c) + `:\`
			if _, err := os.Stat(p); err == nil {
				paths = append(paths, p)
			}
		}
	case "darwin":
		paths = append(paths, home)
		paths = append(paths, kitDrives()...)
	default:
		paths = append(paths, home)
		paths = append(paths, kitDrives()...)
	}
	var b strings.Builder
	for _, p := range paths {
		w := "can store models"
		if checkWritable(filepath.Join(p, ".potluck-check")) != nil {
			w = "read-only (can't store models)"
		}
		fmt.Fprintf(&b, "- %s: %s free, %s\n", p, humanBytes(int64(diskFree(p))), w)
	}
	if b.Len() == 0 {
		return "No drives found."
	}
	return b.String()
}

// checkWritable makes sure a folder can be created and written to.
func checkWritable(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("Potluck can't create %s there (a CD or DVD, or a locked drive, is read-only): %v", dir, err)
	}
	f, err := os.CreateTemp(dir, ".potluck-write-*")
	if err != nil {
		return fmt.Errorf("Potluck can't write to %s (a CD or DVD, or a locked drive, is read-only)", dir)
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	if filepath.Base(dir) == ".potluck-check" {
		os.Remove(dir)
	}
	return nil
}

func nearestExisting(p string) string {
	for p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			break
		}
		p = parent
	}
	return p
}

// movedPointer is the file left in Potluck's old folder that says where it went. Every way of
// starting Potluck (the Mac app, the Windows app, the command line) reads it.
const movedPointer = "potluck-moved-to.txt"

// followMoved returns where Potluck's folder really is, following a move.
func followMoved(dir string) string {
	for i := 0; i < 4; i++ {
		b, err := os.ReadFile(filepath.Join(dir, movedPointer))
		if err != nil {
			return dir
		}
		next := strings.TrimSpace(string(b))
		if next == "" || next == dir {
			return dir
		}
		dir = next
	}
	return dir
}

// moveDataFolder copies Potluck's folder to dst, leaves a pointer in the old one, removes the
// old copy, and restarts Potluck from the new place.
func (h *Host) moveDataFolder(dst string) error {
	src := h.a.dir
	if filepath.Clean(dst) == filepath.Clean(src) {
		return errors.New("Potluck is already there")
	}
	h.Stop()
	h.StopHelper()
	time.Sleep(time.Second)
	if err := copyDataTree(src, dst); err != nil {
		return fmt.Errorf("the copy failed, nothing was moved: %w", err)
	}
	if dirSize(dst) < dirSize(src) {
		return errors.New("the copy came out smaller than the original, so nothing was moved")
	}
	if err := os.WriteFile(filepath.Join(src, movedPointer), []byte(dst+"\n"), 0o644); err != nil {
		return err
	}
	ents, _ := os.ReadDir(src)
	for _, e := range ents {
		if e.Name() != movedPointer && e.Name() != "logs" {
			_ = os.RemoveAll(filepath.Join(src, e.Name()))
		}
	}
	go func() { time.Sleep(2 * time.Second); restartSelf(dst) }()
	return nil
}

func copyDataTree(src, dst string) error {
	return filepath.Walk(src, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if rel == "logs" || strings.HasPrefix(rel, "logs"+string(filepath.Separator)) {
			return nil
		}
		t := filepath.Join(dst, rel)
		if fi.IsDir() {
			return os.MkdirAll(t, 0o755)
		}
		if !fi.Mode().IsRegular() {
			return nil
		}
		in, err := os.Open(p)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(t, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, fi.Mode().Perm())
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, in); err != nil {
			out.Close()
			return err
		}
		return out.Close()
	})
}

// restartSelf starts Potluck again with its folder at dir, replacing this process.
func restartSelf(dir string) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	var args []string
	skip := false
	for _, a := range os.Args[1:] {
		if skip {
			skip = false
			continue
		}
		if a == "--data" || a == "-data" {
			skip = true
			continue
		}
		if strings.HasPrefix(a, "--data=") || strings.HasPrefix(a, "-data=") {
			continue
		}
		args = append(args, a)
	}
	args = append(args, "--data", dir)
	execSelf(exe, args)
}

// driveExists checks that the drive a path is on is there (a Mac volume under /Volumes, a
// Windows drive letter, or any existing folder above it on Linux).
func driveExists(path string) error {
	clean := filepath.Clean(path)
	switch runtime.GOOS {
	case "windows":
		vol := filepath.VolumeName(clean)
		if vol == "" {
			return errors.New("give a path with a drive letter, like D:\\Potluck")
		}
		if _, err := os.Stat(vol + `\`); err != nil {
			return fmt.Errorf("there is no drive %s on this computer (list_drives shows the drives)", vol)
		}
	case "darwin":
		if rest, ok := strings.CutPrefix(clean, "/Volumes/"); ok {
			vol := "/Volumes/" + strings.SplitN(rest, "/", 2)[0]
			if _, err := os.Stat(vol); err != nil {
				return fmt.Errorf("there is no drive %s connected (list_drives shows the drives)", vol)
			}
		}
	}
	if runtime.GOOS != "windows" && strings.Contains(clean, ":") {
		return errors.New("drive letters like D: exist only on Windows; this computer's drives are in list_drives")
	}
	// Only the last folder is created: the one above it must be there already, so a made-up
	// path can't turn into folders on the wrong disk.
	if parent := filepath.Dir(clean); parent != clean {
		if fi, err := os.Stat(parent); err != nil || !fi.IsDir() {
			return fmt.Errorf("%s doesn't exist on this computer (list_drives shows the drives)", parent)
		}
	}
	return nil
}
