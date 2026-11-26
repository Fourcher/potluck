package main

// Scheduled tasks: "every night at 2:00, pull, run the tests, fix what fails, report". The
// person sets the task, its project, when it runs and what it may do, once, while they are
// there; each run is then an ordinary task (its questions, report and phone alert as usual)
// started by Potluck at the time. A run is skipped while the previous one still goes, and a
// time missed while the computer was off is run once when Potluck is back.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Schedule struct {
	ID      string      `json:"id"`
	Title   string      `json:"title"`
	Text    string      `json:"text"` // what the task is asked each time
	Project *ProjectRef `json:"project,omitempty"`
	Policy  Policy      `json:"policy"` // what it may do, approved when the schedule was made
	Every   string      `json:"every"`  // daily | weekdays | hours
	At      string      `json:"at"`     // "02:00" for daily and weekdays
	Hours   int         `json:"hours"`  // for "hours": every this many hours
	On      bool        `json:"on"`
	Created time.Time   `json:"created"`
	LastRun time.Time   `json:"lastRun,omitempty"`
	LastID  string      `json:"lastTask,omitempty"` // the task of the last run
	Note    string      `json:"note,omitempty"`     // why the last time did not run
}

var schedMu sync.Mutex

func (st *sessionStore) schedFile() string { return filepath.Join(st.dir, "schedules.json") }

func (st *sessionStore) loadSchedules() []Schedule {
	var all []Schedule
	_ = readJSONFile(st.schedFile(), &all)
	return all
}

func (st *sessionStore) saveSchedules(all []Schedule) error {
	return writeJSONFile(st.schedFile(), all)
}

// next is when a schedule runs next after t.
func (sc Schedule) next(after time.Time) (time.Time, error) {
	switch sc.Every {
	case "hours":
		if sc.Hours < 1 || sc.Hours > 24*7 {
			return time.Time{}, errors.New("every how many hours? (1 to 168)")
		}
		base := sc.LastRun
		if base.IsZero() {
			base = sc.Created
		}
		n := base.Add(time.Duration(sc.Hours) * time.Hour)
		for !n.After(after) && after.Sub(n) > time.Duration(sc.Hours)*time.Hour {
			n = n.Add(time.Duration(sc.Hours) * time.Hour) // long off: the latest missed time
		}
		return n, nil
	case "daily", "weekdays":
		h, m, ok := parseClock(sc.At)
		if !ok {
			return time.Time{}, errors.New("at what time? (like 02:00)")
		}
		d := time.Date(after.Year(), after.Month(), after.Day(), h, m, 0, 0, after.Location())
		for !d.After(after) || (sc.Every == "weekdays" && (d.Weekday() == time.Saturday || d.Weekday() == time.Sunday)) {
			d = d.AddDate(0, 0, 1)
		}
		return d, nil
	}
	return time.Time{}, errors.New("how often: daily, weekdays or every few hours")
}

func parseClock(s string) (int, int, bool) {
	hh, mm, ok := strings.Cut(strings.TrimSpace(s), ":")
	h, e1 := strconv.Atoi(hh)
	m, e2 := strconv.Atoi(mm)
	return h, m, ok && e1 == nil && e2 == nil && h >= 0 && h < 24 && m >= 0 && m < 60
}

// due says whether a schedule should run now: its next time since the last run has come.
func (sc Schedule) due(now time.Time) bool {
	if !sc.On {
		return false
	}
	from := sc.LastRun
	if from.IsZero() {
		from = sc.Created
	}
	n, err := sc.next(from)
	return err == nil && !n.After(now)
}

// scheduleLoop starts the scheduled tasks when they are due.
func (st *sessionStore) scheduleLoop() {
	for {
		time.Sleep(30 * time.Second)
		st.runDue(time.Now())
	}
}

func (st *sessionStore) runDue(now time.Time) {
	schedMu.Lock()
	defer schedMu.Unlock()
	all := st.loadSchedules()
	changed := false
	for i := range all {
		sc := &all[i]
		if !sc.due(now) {
			continue
		}
		changed = true
		sc.LastRun = now
		if prev := st.get(sc.LastID); prev != nil {
			prev.mu.Lock()
			busy := prev.Status == "working" || prev.Status == "waiting"
			prev.mu.Unlock()
			if busy {
				sc.Note = "Skipped at " + now.Format("Jan 2 15:04") + ": the last run was still going."
				continue
			}
		}
		if _, err := st.startScheduled(sc); err != nil {
			sc.Note = "Did not start at " + now.Format("Jan 2 15:04") + ": " + err.Error()
		} else {
			sc.Note = ""
		}
	}
	if changed {
		_ = st.saveSchedules(all)
	}
}

// startScheduled starts one run of a schedule as an ordinary task.
func (st *sessionStore) startScheduled(sc *Schedule) (*Session, error) {
	if sc.Project != nil {
		if _, err := projectInfo(st.host.a.dir, sc.Project.Root); err != nil {
			return nil, err
		}
	}
	s := st.create(sc.Project, sc.Policy)
	s.mu.Lock()
	s.Schedule = sc.ID
	s.Title = sc.Title + " · " + time.Now().Format("Jan 2 15:04")
	s.mu.Unlock()
	if err := st.send(s, sc.Text); err != nil {
		return nil, err
	}
	sc.LastID = s.ID
	return s, nil
}

// handleSchedules: GET lists, POST makes one; /<id>/on, /off, /run, /delete.
func (st *sessionStore) handleSchedules(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && (r.Header.Get("X-Potluck") != "1" || !sameOrigin(r)) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "send X-Potluck: 1 from this computer"})
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/potluck/schedules"), "/")
	schedMu.Lock()
	defer schedMu.Unlock()
	all := st.loadSchedules()
	if rest == "" {
		if r.Method == http.MethodGet {
			type view struct {
				Schedule
				Next string `json:"next,omitempty"`
			}
			out := []view{}
			for _, sc := range all {
				v := view{Schedule: sc}
				from := sc.LastRun
				if from.IsZero() {
					from = sc.Created
				}
				if n, err := sc.next(maxTime(from, time.Now())); err == nil && sc.On {
					v.Next = n.Format(time.RFC3339)
				}
				out = append(out, v)
			}
			sort.Slice(out, func(i, j int) bool { return out[i].Created.Before(out[j].Created) })
			writeJSON(w, map[string]any{"schedules": out})
			return
		}
		var in Schedule
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			fail(w, err)
			return
		}
		in.Text = strings.TrimSpace(in.Text)
		if in.Text == "" {
			fail(w, errors.New("what should the task do each time?"))
			return
		}
		if in.Project != nil {
			if _, err := projectInfo(st.host.a.dir, in.Project.Root); err != nil {
				fail(w, err)
				return
			}
		}
		in.ID, in.Created, in.On, in.LastRun, in.LastID, in.Note = randomHex(6), time.Now(), true, time.Time{}, "", ""
		if in.Title == "" {
			in.Title = titleOf(in.Text, in.Project)
		}
		if _, err := in.next(time.Now()); err != nil {
			fail(w, err)
			return
		}
		all = append(all, in)
		if err := st.saveSchedules(all); err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, in)
		return
	}
	id, action, _ := strings.Cut(rest, "/")
	i := -1
	for k := range all {
		if all[k].ID == id {
			i = k
		}
	}
	if i < 0 {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	switch action {
	case "on", "off":
		all[i].On = action == "on"
		if all[i].On {
			all[i].LastRun = time.Now() // from now on, not the times missed while it was off
		}
	case "delete":
		all = append(all[:i], all[i+1:]...)
	case "run":
		s, err := st.startScheduled(&all[i])
		if err != nil {
			fail(w, err)
			return
		}
		_ = st.saveSchedules(all)
		writeJSON(w, map[string]any{"ok": true, "task": s.ID})
		return
	default:
		http.NotFound(w, r)
		return
	}
	if err := st.saveSchedules(all); err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// scheduledNote tells a scheduled task that its permissions were approved when the schedule
// was made: there is no one to ask up front at 2:00.
func scheduledNote() string {
	return ("This task runs on a schedule the user set up; they approved what it may do when they set it up, so do not call request_permissions. Work within that; anything else that needs their approval is asked as usual and, if nobody answers, left out and listed in your final report.")
}

var _ = fmt.Sprint
