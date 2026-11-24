package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
)

// HTTP for agent sessions, on the chat address. Only this computer's own chat page (and,
// through phone.go, paired phones) may use them.
//
//	GET  /potluck/sessions                    list (newest first, without messages)
//	POST /potluck/sessions                    {project:{root,name}, policy, text} -> new session, started
//	GET  /potluck/sessions/<id>               one session with its messages
//	POST /potluck/sessions/<id>/message       {text}
//	POST /potluck/sessions/<id>/answer        {question, answer}  answer: yes | no | always | own words
//	POST /potluck/sessions/<id>/stop
//	POST /potluck/sessions/<id>/policy        {policy}
//	POST /potluck/sessions/<id>/forget-grants  give back what was kept for its project
//	POST /potluck/sessions/<id>/delete
//	GET  /potluck/sessions/<id>/events        server-sent events (SEvent)
//	GET  /potluck/questions                   every open question, for phones and alerts

type sessionSummary struct {
	ID       string      `json:"id"`
	Title    string      `json:"title"`
	Project  *ProjectRef `json:"project,omitempty"`
	Status   string      `json:"status"`
	Error    string      `json:"error,omitempty"`
	Updated  time.Time   `json:"updated"`
	Question *Question   `json:"question,omitempty"`
	Steps    int         `json:"steps"`
}

func (st *sessionStore) handle(w http.ResponseWriter, r *http.Request, mutate bool) {
	if r.Method != http.MethodGet && (r.Header.Get("X-Potluck") != "1" || !sameOrigin(r)) {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "send X-Potluck: 1 from this computer"})
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/potluck/")
	if path == "questions" {
		writeJSON(w, map[string]any{"questions": st.questions()})
		return
	}
	parts := strings.Split(strings.TrimPrefix(path, "sessions"), "/")
	if len(parts) > 0 && parts[0] == "" {
		parts = parts[1:]
	}
	if len(parts) == 0 || parts[0] == "" {
		if r.Method == http.MethodGet {
			st.mu.Lock()
			var out []sessionSummary
			for _, s := range st.sessions {
				s.mu.Lock()
				out = append(out, sessionSummary{s.ID, s.Title, s.Project, s.Status, s.Error, s.Updated, s.Question, s.Steps})
				s.mu.Unlock()
			}
			st.mu.Unlock()
			sort.Slice(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
			writeJSON(w, map[string]any{"sessions": out})
			return
		}
		var in struct {
			Project *ProjectRef `json:"project"`
			Policy  Policy      `json:"policy"`
			Text    string      `json:"text"`
			Kind    string      `json:"kind"` // "helper": the Potluck helper
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&in); err != nil {
			fail(w, err)
			return
		}
		if in.Project != nil {
			if _, err := projectInfo(st.host.a.dir, in.Project.Root); err != nil {
				fail(w, err)
				return
			}
		}
		if in.Kind == "helper" {
			in.Project = nil
			st.host.EnsureHelper()
		}
		s := st.create(in.Project, in.Policy)
		if in.Kind == "helper" {
			s.mu.Lock()
			s.Kind, s.Title = "helper", "Potluck helper"
			s.mu.Unlock()
		}
		if strings.TrimSpace(in.Text) != "" {
			if err := st.send(s, in.Text); err != nil {
				fail(w, err)
				return
			}
		}
		writeJSON(w, s)
		return
	}
	s := st.get(parts[0])
	if s == nil {
		http.NotFound(w, r)
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	var in struct {
		Text     string `json:"text"`
		Question string `json:"question"`
		Answer   string `json:"answer"`
		Policy   Policy `json:"policy"`
	}
	if r.Method == http.MethodPost {
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&in)
	}
	var err error
	switch {
	case action == "" && r.Method == http.MethodGet:
		s.mu.Lock()
		b, _ := json.Marshal(s)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
		return
	case action == "events":
		st.events(w, r, s)
		return
	case action == "message":
		err = st.send(s, in.Text)
	case action == "answer":
		err = st.answer(s, in.Question, in.Answer)
	case action == "stop":
		st.stop(s)
	case action == "policy":
		s.mu.Lock()
		s.Policy = in.Policy
		s.mu.Unlock()
		st.save(s)
	case action == "forget-grants":
		s.mu.Lock()
		s.Grants = Grants{}
		p := s.Project
		s.mu.Unlock()
		if p != nil {
			st.keepGrants(p.Root, nil)
		}
		st.save(s)
	case action == "delete":
		st.stop(s)
		st.mu.Lock()
		delete(st.sessions, s.ID)
		st.mu.Unlock()
		err = removeFile(st.dir, s.ID+".json")
	default:
		err = errors.New("unknown action")
	}
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// events streams a session's progress as server-sent events until the client goes away.
func (st *sessionStore) events(w http.ResponseWriter, r *http.Request, s *Session) {
	fl, ok := w.(http.Flusher)
	if !ok {
		fail(w, errors.New("streaming is not available"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	ch := make(chan SEvent, 256)
	s.mu.Lock()
	s.subs[ch] = true
	status, errText, q := s.Status, s.Error, s.Question
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}()
	send := func(ev SEvent) bool {
		b, _ := json.Marshal(ev)
		if _, err := w.Write(append(append([]byte("data: "), b...), '\n', '\n')); err != nil {
			return false
		}
		fl.Flush()
		return true
	}
	send(SEvent{Type: "status", Status: status, Error: errText})
	if q != nil {
		send(SEvent{Type: "question", Question: q})
	}
	tick := time.NewTicker(25 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			if !send(ev) {
				return
			}
		case <-tick.C: // keep the connection open through proxies and idle timers
			if _, err := w.Write([]byte(": ping\n\n")); err != nil {
				return
			}
			fl.Flush()
		}
	}
}
