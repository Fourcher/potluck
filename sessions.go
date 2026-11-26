package main

// Agent sessions: tasks that run inside Potluck, so they keep going with the browser closed
// and can be watched and answered from anywhere (the chat page, the iPhone app, a
// notification). Local models are slow; a task should be something you set and forget.
//
// A session holds its conversation, its project (if any), its permission policy and what was
// approved. Potluck runs the loop itself: ask the model, run the tools it calls, feed the
// results back, until the model is done. Whenever something needs the person (a command, a
// file change, a connector, or a question the model asks), it becomes a Question in one queue
// that any client can answer with yes, no, or their own words. Sessions are saved to disk
// after every step.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Policy is what a session may do without asking.
type Policy struct {
	Files      string          `json:"files"`      // ask | read (read freely, ask before changes) | edit (all freely)
	Commands   string          `json:"commands"`   // ask | auto
	Connectors map[string]bool `json:"connectors"` // switched-on MCP servers (started only if on)
	All        bool            `json:"all"`        // approve everything: nothing asks (the person accepted the warning)
	Thinking   bool            `json:"thinking"`
	// AwayMinutes: how long a question waits for the person before the task carries on by
	// itself (0: the default, 30 minutes; below 0: it waits for ever).
	AwayMinutes int `json:"awayMinutes,omitempty"`
}

// awayUnit is a minute (tests make it shorter).
var awayUnit = time.Minute

// awayAfter is how long a question waits before it is answered for an absent person.
func (p Policy) awayAfter() time.Duration {
	switch {
	case p.AwayMinutes < 0:
		return 0
	case p.AwayMinutes == 0:
		return 30 * awayUnit
	}
	return time.Duration(p.AwayMinutes) * awayUnit
}

// awayAnswer is what a question gets when nobody answered in time. A task left running at
// night must not stop at the first question until morning; nor may it do what was not
// approved. So: a question to the person gets "decide safely yourself", and an approval is
// not given, the model told to carry on without it and say so at the end.
func awayAnswer(kind string, waited time.Duration) string {
	mins := int(waited / awayUnit)
	if kind == "ask" {
		return fmt.Sprintf("(No answer: the person has been away for %d minutes. Choose the safest reasonable option yourself, say which you chose and why, and carry on. Mention it in your final report.)", mins)
	}
	return fmt.Sprintf("(No answer within %d minutes: the person is away, so this was not approved and not done. Carry on without it if you can, and list it in your final report as waiting for their approval.)", mins)
}

// Grants are standing yeses given during a session ("yes, and don't ask again").
type Grants struct {
	Read, Write, Run, Browser, Screen bool
	Connectors                        map[string]bool
}

type SMsg struct {
	Role      string      `json:"role"` // user | assistant | tool
	Content   string      `json:"content"`
	Reasoning string      `json:"reasoning,omitempty"`
	Calls     []SToolCall `json:"calls,omitempty"`
	CallID    string      `json:"callId,omitempty"`
	Name      string      `json:"name,omitempty"`   // tool messages: which tool
	State     string      `json:"state,omitempty"`  // tool messages: ok | error | denied
	Images    []string    `json:"images,omitempty"` // user messages: pictures for a model that can see (file paths)
	At        time.Time   `json:"at"`
	cut       bool        // the engine ended this answer at its length limit, not because it was done
}

type SToolCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Args string `json:"args"`
}

// Question is something only the person can decide. Answer "yes", "no", "always" (yes, and
// stop asking about this kind of thing in this session), or anything else in their own words.
type Question struct {
	ID       string    `json:"id"`
	Session  string    `json:"session"`
	Title    string    `json:"title"`  // one line: what is being asked
	Detail   string    `json:"detail"` // the command, the change, the full question
	Kind     string    `json:"kind"`   // run | read | write | connector | ask | plan
	Created  time.Time `json:"created"`
	Answer   string    `json:"answer,omitempty"`
	Auto     bool      `json:"auto,omitempty"` // answered by Potluck: nobody answered in time
	answered chan string
}

type Session struct {
	ID       string      `json:"id"`
	Title    string      `json:"title"`
	Kind     string      `json:"kind,omitempty"` // "" for a task, "helper" for the Potluck helper
	Project  *ProjectRef `json:"project,omitempty"`
	Created  time.Time   `json:"created"`
	Updated  time.Time   `json:"updated"`
	Status   string      `json:"status"` // idle | working | waiting | error | stopped
	Error    string      `json:"error,omitempty"`
	Policy   Policy      `json:"policy"`
	Grants   Grants      `json:"grants"`
	Messages []SMsg      `json:"messages"`
	Question *Question   `json:"question,omitempty"` // the open question, if waiting
	Steps    int         `json:"steps"`

	lastPrompt  int // tokens the model read last time, to know when to make room
	lastPromptN int // how many messages the task had then
	mu          sync.Mutex
	cancel      context.CancelFunc
	// mcpByName: the connector tools offered at the last step, by the name the model sees
	// (cleaned up and cut to 64 characters, so it cannot be read back into the connector's
	// own name, which may hold spaces or colons).
	mcpByName map[string]MCPTool
	subs        map[chan SEvent]bool
	// saveMu keeps one save at a time from snapshot to rename (two could land in the wrong
	// order); deleted, under it, stops a run that is still ending from writing a deleted task
	// back to disk.
	saveMu  sync.Mutex
	deleted bool
}

type ProjectRef struct {
	Root string `json:"root"`
	Name string `json:"name"`
}

// SEvent is what watchers of a session receive.
type SEvent struct {
	Type      string    `json:"type"` // delta | message | tool | question | status
	Text      string    `json:"text,omitempty"`
	Reasoning string    `json:"reasoning,omitempty"`
	Msg       *SMsg     `json:"msg,omitempty"`
	Question  *Question `json:"question,omitempty"`
	Status    string    `json:"status,omitempty"`
	Error     string    `json:"error,omitempty"`
}

type sessionStore struct {
	mu       sync.Mutex
	dir      string
	host     *Host
	sessions map[string]*Session
	notify   func(q *Question, s *Session) // phones and other alerts (set by the host)
}

func newSessionStore(h *Host, data string) *sessionStore {
	st := &sessionStore{dir: filepath.Join(data, "sessions"), host: h, sessions: map[string]*Session{}}
	_ = os.MkdirAll(st.dir, 0o700)
	files, _ := filepath.Glob(filepath.Join(st.dir, "*.json"))
	for _, f := range files {
		var s Session
		if readJSONFile(f, &s) == nil && s.ID != "" {
			if s.Status == "working" || s.Status == "waiting" {
				s.Status, s.Error = "stopped", "Potluck restarted while this was running. Send a message to carry on."
				s.Question = nil
			}
			s.subs = map[chan SEvent]bool{}
			st.sessions[s.ID] = &s
		}
	}
	return st
}

func (st *sessionStore) save(s *Session) {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	if s.deleted {
		return
	}
	s.mu.Lock()
	b, err := json.MarshalIndent(s, "", " ")
	s.mu.Unlock()
	if err == nil {
		tmp := filepath.Join(st.dir, s.ID+".json.tmp")
		if os.WriteFile(tmp, b, 0o600) == nil {
			_ = os.Rename(tmp, filepath.Join(st.dir, s.ID+".json"))
		}
	}
}

func (s *Session) emit(ev SEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- ev:
		default: // a slow watcher misses deltas, never blocks the task
		}
	}
}

func (s *Session) setStatus(status, errText string) {
	s.mu.Lock()
	s.Status, s.Error, s.Updated = status, errText, time.Now()
	s.mu.Unlock()
	s.emit(SEvent{Type: "status", Status: status, Error: errText})
}

// ---------- creating, talking, answering ----------

func (st *sessionStore) create(project *ProjectRef, pol Policy) *Session {
	s := &Session{ID: randomHex(8), Created: time.Now(), Updated: time.Now(), Status: "idle", Policy: pol, Project: project, subs: map[chan SEvent]bool{}}
	if s.Policy.Files == "" {
		s.Policy.Files = "read"
	}
	if s.Policy.Commands == "" {
		s.Policy.Commands = "ask"
	}
	if project != nil {
		s.Title = project.Name
		// Approvals kept for this project in earlier sessions carry over.
		if g, ok := st.projectGrants(project.Root); ok {
			s.Grants = g
		}
	}
	st.mu.Lock()
	st.sessions[s.ID] = s
	st.mu.Unlock()
	st.save(s)
	return s
}

func (st *sessionStore) get(id string) *Session {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.sessions[id]
}

// send adds the person's message and starts the loop if it is not running.
func (st *sessionStore) send(s *Session, text string) error {
	s.mu.Lock()
	if s.Status == "working" || s.Status == "waiting" {
		s.mu.Unlock()
		return errors.New("this task is still working: stop it first, or answer its question")
	}
	s.Messages = append(s.Messages, SMsg{Role: "user", Content: text, At: time.Now()})
	if s.Title == "" || (s.Project != nil && s.Title == s.Project.Name && len(s.Messages) == 1) {
		s.Title = titleOf(text, s.Project)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.Status, s.Error = "working", "" // from the moment it is sent, so no one sees it as finished
	s.mu.Unlock()
	st.save(s)
	go st.run(ctx, s)
	return nil
}

func titleOf(text string, p *ProjectRef) string {
	t := strings.Join(strings.Fields(text), " ")
	if r := []rune(t); len(r) > 48 {
		t = string(r[:46]) + "…" // whole characters, never half of one
	}
	if p != nil {
		return p.Name + ": " + t
	}
	return t
}

func (st *sessionStore) stop(s *Session) {
	s.mu.Lock()
	c, q := s.cancel, s.Question
	s.mu.Unlock()
	if c != nil {
		c()
	}
	if q != nil {
		select {
		case q.answered <- "no":
		default:
		}
	}
}

// answer resolves the open question.
func (st *sessionStore) answer(s *Session, qid, ans string) error {
	s.mu.Lock()
	q := s.Question
	s.mu.Unlock()
	if q == nil || q.ID != qid {
		return errors.New("that question has already been answered")
	}
	ans = strings.TrimSpace(ans)
	if ans == "" {
		return errors.New("give an answer")
	}
	select {
	case q.answered <- ans:
		return nil
	default:
		return errors.New("that question has already been answered")
	}
}

// questions lists every open question across sessions, newest first (for phones).
func (st *sessionStore) questions() []*Question {
	st.mu.Lock()
	defer st.mu.Unlock()
	var out []*Question
	for _, s := range st.sessions {
		s.mu.Lock()
		if s.Question != nil {
			out = append(out, s.Question)
		}
		s.mu.Unlock()
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out
}

// ask puts a question to the person and waits for the answer.
func (st *sessionStore) ask(ctx context.Context, s *Session, kind, title, detail string) (string, error) {
	q := &Question{ID: randomHex(6), Session: s.ID, Title: title, Detail: detail, Kind: kind, Created: time.Now(), answered: make(chan string, 1)}
	s.mu.Lock()
	s.Question = q
	s.mu.Unlock()
	s.setStatus("waiting", "")
	s.emit(SEvent{Type: "question", Question: q})
	st.save(s)
	if st.notify != nil {
		go st.notify(q, s)
	}
	s.mu.Lock()
	wait := s.Policy.awayAfter()
	s.mu.Unlock()
	var away <-chan time.Time
	if wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		away = t.C
	}
	var ans string
	auto := false
	select {
	case ans = <-q.answered:
	case <-ctx.Done():
		ans = "no"
	case <-away:
		ans, auto = awayAnswer(kind, wait), true
	}
	// A question is never changed once shown (watchers read it without the lock): the
	// answered one is a copy.
	done := *q
	done.Answer, done.Auto = ans, auto
	s.mu.Lock()
	s.Question = nil
	s.mu.Unlock()
	s.setStatus("working", "")
	s.emit(SEvent{Type: "question", Question: &done})
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	return ans, nil
}

// ---------- permissions ----------

// allowed decides whether a tool call may run, asking the person if the policy says so.
// It returns "" to go ahead, or the text to give the model instead of running it.
func (st *sessionStore) allowed(ctx context.Context, s *Session, call SToolCall, args map[string]any) (string, error) {
	s.mu.Lock()
	pol, g := s.Policy, s.Grants
	s.mu.Unlock()
	if pol.All {
		return "", nil
	}
	kind, title, detail := "", "", ""
	connServer := "" // the connector's own name, for a connector tool
	str := func(k string) string { v, _ := args[k].(string); return v }
	switch {
	case call.Name == "run_command" || call.Name == "run_applescript":
		if pol.Commands == "auto" || g.Run {
			return "", nil
		}
		kind = "run"
		if call.Name == "run_applescript" {
			title, detail = "Run this AppleScript to control an app?", str("script")
		} else {
			title, detail = "Run this command?", str("command")+"\n\n(in "+st.commandDir(s, str("folder"))+")"
		}
	case call.Name == "edit_file" || call.Name == "write_file":
		if pol.Files == "edit" || g.Write {
			return "", nil
		}
		kind = "write"
		if call.Name == "edit_file" {
			title, detail = "Change "+str("path")+"?", "Replace:\n"+trimTo(str("old"), 2000)+"\n\nWith:\n"+trimTo(str("new"), 2000)
		} else {
			title, detail = "Write "+str("path")+"?", trimTo(str("content"), 3000)
		}
	case call.Name == "read_file" || call.Name == "list_files" || call.Name == "search_files":
		if pol.Files == "read" || pol.Files == "edit" || g.Read {
			return "", nil
		}
		kind, title, detail = "read", "Look at the project's files?", call.Name+" "+call.Args
	case strings.HasPrefix(call.Name, "browser_"):
		// Each kind of access has its own yes: running commands freely does not include the
		// browser, the screen or connectors (found by the self-review on the pool). Reading
		// counts too: the browser is signed in to the person's sites.
		if g.Browser {
			return "", nil
		}
		kind = "browser"
		switch call.Name {
		case "browser_open":
			title, detail = "Open this page in the browser?", str("url")
		case "browser_click":
			title, detail = "Click this on the page?", fmt.Sprint("[", args["ref"], "]")
		case "browser_type":
			title, detail = "Type this into the page?", fmt.Sprint("[", args["ref"], "] ", trimTo(str("text"), 500))
		default:
			title, detail = "Use the browser?", call.Name
		}
	case strings.HasPrefix(call.Name, "screen_"):
		if g.Screen {
			return "", nil
		}
		kind = "screen"
		switch call.Name {
		case "screen_look":
			title = "Look at your screen?"
		case "screen_click":
			title, detail = "Click on your screen?", fmt.Sprintf("at %v, %v", args["x"], args["y"])
		case "screen_type":
			title, detail = "Type on your screen?", trimTo(str("text"), 500)
		case "screen_key":
			title, detail = "Press keys?", str("keys")
		default:
			title = "Scroll on your screen?"
		}
	case strings.HasPrefix(call.Name, "mcp__"):
		s.mu.Lock()
		mt, known := s.mcpByName[call.Name]
		s.mu.Unlock()
		connServer = mt.Server
		server := mt.Server
		if !known {
			return "That connector tool is not available now.", nil
		}
		if g.Connectors[server] {
			return "", nil
		}
		kind, title, detail = "connector", "Use "+strings.Replace(strings.TrimPrefix(call.Name, "mcp__"), "__", ": ", 1)+"?", trimTo(call.Args, 3000)
	default:
		return "", nil // skills and questions to the person need no permission
	}
	ans, err := st.ask(ctx, s, kind, title, detail)
	if err != nil {
		return "", err
	}
	switch strings.ToLower(ans) {
	case "yes", "y", "ok", "allow":
		return "", nil
	case "always":
		s.mu.Lock()
		switch kind {
		case "run":
			s.Grants.Run = true
		case "write":
			s.Grants.Write, s.Grants.Read = true, true
		case "read":
			s.Grants.Read = true
		case "browser":
			s.Grants.Browser = true
		case "screen":
			s.Grants.Screen = true
		case "connector":
			if s.Grants.Connectors == nil {
				s.Grants.Connectors = map[string]bool{}
			}
			s.Grants.Connectors[connServer] = true
		}
		s.mu.Unlock()
		return "", nil
	case "no", "n", "deny":
		return "The user said no. Do not try it another way; ask what they would like instead, or carry on without it.", nil
	}
	return "The user did not allow that, and said: " + ans, nil
}

// trimTo keeps about n bytes of s, ending on a whole character.
func trimTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "\n…"
}

// projectGrants are approvals kept for a project across sessions.
func (st *sessionStore) projectGrants(root string) (Grants, bool) {
	var all map[string]Grants
	_ = readJSONFile(filepath.Join(st.dir, "project-grants.json"), &all)
	g, ok := all[root]
	return g, ok
}

func (st *sessionStore) keepGrants(root string, g *Grants) {
	var all map[string]Grants
	_ = readJSONFile(filepath.Join(st.dir, "project-grants.json"), &all)
	if all == nil {
		all = map[string]Grants{}
	}
	if g == nil {
		delete(all, root)
	} else {
		all[root] = *g
	}
	_ = writeJSONFile(filepath.Join(st.dir, "project-grants.json"), all)
}

func (h *Host) sessionStore() *sessionStore {
	h.sessOnce.Do(func() {
		h.sess = newSessionStore(h, h.a.dir)
		h.sess.notify = h.pushQuestion // paired phones hear about every question (apns.go)
	})
	return h.sess
}

func removeFile(dir, name string) error {
	err := os.Remove(filepath.Join(dir, name))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
