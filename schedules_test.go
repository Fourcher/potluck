package main

import (
	"strings"
	"testing"
	"time"
)

func TestScheduleTimes(t *testing.T) {
	loc := time.Local
	at := func(d, h, m int) time.Time { return time.Date(2026, 11, d, h, m, 0, 0, loc) } // Nov 2026: the 27th is a Friday
	daily := Schedule{Every: "daily", At: "02:00", On: true, Created: at(26, 10, 0)}
	if n, _ := daily.next(at(26, 10, 0)); !n.Equal(at(27, 2, 0)) {
		t.Fatalf("daily next %v", n)
	}
	if daily.due(at(27, 1, 59)) || !daily.due(at(27, 2, 0)) {
		t.Fatal("daily due")
	}
	wk := Schedule{Every: "weekdays", At: "02:00", On: true, Created: at(27, 10, 0)} // Friday after 2:00
	if n, _ := wk.next(at(27, 10, 0)); !n.Equal(at(30, 2, 0)) {
		t.Fatalf("weekdays skips the weekend: %v", n)
	}
	hrs := Schedule{Every: "hours", Hours: 6, On: true, Created: at(26, 0, 0), LastRun: at(26, 0, 0)}
	if !hrs.due(at(26, 6, 0)) || hrs.due(at(26, 5, 59)) {
		t.Fatal("hours due")
	}
	if (Schedule{Every: "daily", At: "25:00"}).due(at(27, 2, 0)) {
		t.Fatal("a bad time was due")
	}
	off := daily
	off.On = false
	if off.due(at(28, 3, 0)) {
		t.Fatal("a schedule turned off ran")
	}
}

// A due schedule starts a task; one still going is skipped, and a time missed while the
// computer was off runs once.
func TestScheduleRuns(t *testing.T) {
	_, st := withFakeModel(t, "text:ran", "text:ran again")
	now := time.Now()
	sc := Schedule{ID: "s1", Title: "Nightly", Text: "run the tests", Every: "daily", At: now.Add(-time.Hour).Format("15:04"), On: true, Created: now.Add(-49 * time.Hour)}
	_ = st.saveSchedules([]Schedule{sc})
	st.runDue(now) // two nights missed: one run
	all := st.loadSchedules()
	s := st.get(all[0].LastID)
	if s == nil {
		t.Fatal("no task started")
	}
	s.mu.Lock()
	sys := s.Schedule
	s.mu.Unlock()
	if sys != "s1" {
		t.Fatal("the task does not know its schedule")
	}
	if st.taskSystem(s) == "" || !strings.Contains(st.taskSystem(s), "do not call request_permissions") {
		t.Fatal("a scheduled task is told to ask for permissions up front")
	}
	st.runDue(now.Add(time.Minute)) // not due again until tomorrow
	if again := st.loadSchedules()[0]; again.LastID != all[0].LastID {
		t.Fatal("ran twice for one missed time")
	}
	waitStatus(t, s, "idle")
}

