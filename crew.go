package main

// The crew: several models at once, each on one computer, working in tandem.
//
// Pooling one model across computers only pays when it is too big for any one of them (each
// extra computer adds a network hand-off to every word). When it is not, the other computers
// do more good running models of their own. The main model keeps working as before; crew
// members are extra models in their own engine slots (aux.go). A request names the one it
// wants with "model": "crew:<name>"; a task hands sub-tasks to crew members with the delegate
// tool (sessions), so one supervisor on the strongest model spreads work over every computer.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

type CrewMember struct {
	Name     string    `json:"name"` // what requests and tasks call it, e.g. "llmos-inkling"
	Node     string    `json:"node"`
	NodeName string    `json:"nodeName"`
	Model    ModelRef  `json:"model"`
	Ctx      int       `json:"ctx"`
	Phase    string    `json:"phase"` // starting | running | error
	Note     string    `json:"note,omitempty"`
	Error    string    `json:"error,omitempty"`
	Since    time.Time `json:"since"`
	bytes    int64     // memory it holds on its computer
}

var crewNameClean = regexp.MustCompile(`[^a-z0-9]+`)

func crewSlotName(s string) string {
	s = strings.Trim(crewNameClean.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 40 {
		s = s[:40]
	}
	if s == "" || s == "helper" {
		s = "crew"
	}
	return s
}

// crewReserveLocked is the memory crew members hold on a computer (called with h.mu held).
func (h *Host) crewReserveLocked(node string) int64 {
	var n int64
	for _, c := range h.crew {
		if c.Node == node && (c.Phase == "starting" || c.Phase == "running") {
			n += c.bytes
		}
	}
	return n
}

// CrewList returns the crew members, sorted by name.
func (h *Host) CrewList() []CrewMember {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]CrewMember, 0, len(h.crew))
	for _, c := range h.crew {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (h *Host) crewMember(name string) *CrewMember {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c := h.crew[name]; c != nil {
		cp := *c
		return &cp
	}
	return nil
}

// StartCrew loads a model on one computer as a crew member. It returns at once; the member's
// phase says how far it got.
func (h *Host) StartCrew(name, node string, model ModelRef, ctxSize int) (string, error) {
	h.mu.Lock()
	n := h.nodes[node]
	if n == nil || !n.Online {
		h.mu.Unlock()
		return "", errors.New("that computer is not connected")
	}
	if !n.Info.Limits.ActiveNow(time.Now()) {
		h.mu.Unlock()
		return "", fmt.Errorf("%s isn't lending its memory now (its switch or hours)", n.Info.Name)
	}
	if n.Role != "" && h.cluster.Phase != "idle" {
		h.mu.Unlock()
		return "", fmt.Errorf("%s is busy with the main model; pick another computer or stop the main model first", n.Info.Name)
	}
	if name == "" {
		name = n.Info.Name + "-" + strings.TrimSuffix(model.File, ".gguf")
	}
	name = crewSlotName(name)
	if old := h.crew[name]; old != nil && (old.Phase == "starting" || old.Phase == "running") {
		h.mu.Unlock()
		return "", fmt.Errorf("a crew member called %s is already running", name)
	}
	if ctxSize <= 0 {
		ctxSize = 16384
	}
	c := &CrewMember{Name: name, Node: node, NodeName: n.Info.Name, Model: model, Ctx: ctxSize, Phase: "starting", Note: "Reading the model's blueprint", Since: time.Now()}
	if h.crew == nil {
		h.crew = map[string]*CrewMember{}
	}
	h.crew[name] = c
	h.mu.Unlock()
	go func() {
		err := h.startCrew(c)
		h.mu.Lock()
		if err != nil {
			c.Phase, c.Error, c.Note = "error", err.Error(), ""
		} else {
			c.Phase, c.Note = "running", ""
		}
		h.mu.Unlock()
	}()
	return name, nil
}

func (h *Host) crewNote(c *CrewMember, s string) {
	h.mu.Lock()
	c.Note = s
	h.mu.Unlock()
}

func (h *Host) startCrew(c *CrewMember) error {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
	model := c.Model
	if err := resolveModel(ctx, &model); err != nil {
		return err
	}
	if model.Source == "local" && model.Node == "" {
		model.Node = c.Node
	}
	if model.Source == "local" && model.Node != c.Node {
		return errors.New("that model file is on another computer; pick the computer that has it, or a model from Hugging Face")
	}
	info, err := h.modelInfo(ctx, &model)
	if err != nil {
		return fmt.Errorf("could not read the model: %w", err)
	}
	info.PlanCtx = c.Ctx
	var pn *PlanNode
	for _, x := range h.planNodes() {
		if x.ID == c.Node {
			x := x
			pn = &x
		}
	}
	if pn == nil {
		return errors.New("that computer is not available right now")
	}
	plan := planSolo(info, *pn)
	if !plan.Fits {
		return errors.New(plan.Reason)
	}
	if plan.Overflow > 0 {
		return fmt.Errorf("it doesn't fit in %s's free memory (%s short); pick a smaller size", c.NodeName, humanBytes(plan.Overflow))
	}
	h.mu.Lock()
	var kv int64
	for _, b := range info.ContextBytes(c.Ctx) {
		kv += b
	}
	c.Model, c.bytes = model, info.FileSize+kv
	ni := h.nodes[c.Node].Info
	h.mu.Unlock()
	h.crewNote(c, "Getting the AI engine ready")
	spec, err := h.runtimeSpec(ni, true)
	if err != nil {
		return err
	}
	spec.Solo = true
	if err := h.call(c.Node, "ensure_runtime", spec, nil, 2*time.Hour); err != nil {
		return err
	}
	if model.Source != "local" {
		h.crewNote(c, "Downloading "+humanBytes(model.Size))
		if err := h.call(c.Node, "fetch_model", FetchModelReq{URL: model.URL, File: model.File, Size: model.Size, SHA256: model.SHA256, Parts: model.Parts}, nil, 24*time.Hour); err != nil {
			return err
		}
	}
	h.crewNote(c, "Loading the model")
	ngl := plan.NGL
	if ngl > info.EngineLayers() {
		ngl = 999
	}
	if err := h.call(c.Node, "start_aux", StartAuxReq{Name: c.Name, File: model.File, Ctx: c.Ctx, NGL: ngl, ExpertCPU: plan.ExpertCPU, KVKeyQ8: plan.KVKeyQ8}, nil, time.Minute); err != nil {
		return err
	}
	for deadline := time.Now().Add(30 * time.Minute); time.Now().Before(deadline); time.Sleep(3 * time.Second) {
		var st AuxStatus
		if err := h.call(c.Node, "aux_status", map[string]string{"name": c.Name}, &st, 20*time.Second); err != nil {
			continue
		}
		if st.Ready {
			return nil
		}
		if !st.Running {
			return errors.New("the engine stopped while loading: " + st.Log)
		}
		if h.crewMember(c.Name) == nil {
			return errors.New("stopped")
		}
	}
	return errors.New("loading took too long")
}

// StopCrew unloads a crew member.
func (h *Host) StopCrew(name string) error {
	h.mu.Lock()
	c := h.crew[name]
	delete(h.crew, name)
	h.mu.Unlock()
	if c == nil {
		return errors.New("no crew member by that name")
	}
	return h.call(c.Node, "stop_aux", map[string]string{"name": c.Name}, nil, 30*time.Second)
}

// crewChatModels lists running crew members for the chat's model menu and /v1/models.
func (h *Host) crewChatModels() []chatModel {
	var out []chatModel
	for _, c := range h.CrewList() {
		if c.Phase != "running" {
			continue
		}
		out = append(out, chatModel{Title: c.Name + " (" + strings.TrimSuffix(c.Model.File, ".gguf") + ")", File: "crew:" + c.Name, Size: c.Model.Size, Node: "ext:crew", NodeName: c.NodeName + " (crew)"})
	}
	return out
}
