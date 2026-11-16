package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed web
var webFS embed.FS

// App ties the pieces together on one computer.
type App struct {
	dir    string
	id     *Identity
	cfg    *ConfigStore
	agent  *Agent
	host   *Host
	helper *Helper
	token  *JoinToken

	fwMu    sync.Mutex
	fwAt    time.Time
	fwState string
}

// setRole turns this computer into the main computer or a helper. It can be called once.
func (app *App) setRole(role string) error {
	if app.host != nil || app.helper != nil {
		return errors.New("this computer already has a role; restart Potluck to change it")
	}
	switch role {
	case "main":
		if app.cfg.Get().Limits == (Limits{}) {
			app.cfg.Update(func(c *Config) { c.Limits = defaultLimits(true) })
		}
		h := newHost(app.agent, app.id, app.cfg)
		if err := h.Run(); err != nil {
			return err
		}
		app.host = h
	case "helper":
		if app.cfg.Get().Limits == (Limits{}) {
			app.cfg.Update(func(c *Config) { c.Limits = defaultLimits(false) })
		}
		app.helper = newHelper(app.agent, app.id, app.cfg, app.token)
		go app.helper.Run()
	default:
		return errors.New("unknown role")
	}
	app.cfg.Update(func(c *Config) { c.Role = role })
	return nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// serveUI runs the control panel. It listens on this computer only.
func (app *App) serveUI() error {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(webFS, "web")
	mux.Handle("/", http.FileServer(http.FS(sub)))

	api := func(path string, fn func(r *http.Request) (any, error)) {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			// Only pages served by this app may call the API: blocks other websites from
			// poking at it through the browser.
			host, _, _ := net.SplitHostPort(r.Host)
			if host != "127.0.0.1" && host != "localhost" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			if r.Method != http.MethodGet && r.Header.Get("X-Potluck") != "1" {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			out, err := fn(r)
			if err != nil {
				fail(w, err)
				return
			}
			if out == nil {
				out = map[string]bool{"ok": true}
			}
			writeJSON(w, out)
		})
	}
	body := func(r *http.Request, v any) error {
		return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v)
	}
	needMain := func() (*Host, error) {
		if app.host == nil {
			return nil, errors.New("this is not the main computer")
		}
		return app.host, nil
	}

	api("/api/state", func(r *http.Request) (any, error) { return app.state(), nil })

	api("/api/role", func(r *http.Request) (any, error) {
		var in struct{ Role string }
		if err := body(r, &in); err != nil {
			return nil, err
		}
		return nil, app.setRole(in.Role)
	})

	api("/api/node", func(r *http.Request) (any, error) {
		var in struct {
			ID     string `json:"id"`
			Limits Limits `json:"limits"`
			Name   string `json:"name"`
		}
		if err := body(r, &in); err != nil {
			return nil, err
		}
		req := map[string]any{"limits": in.Limits, "name": in.Name}
		if app.host != nil {
			var info NodeInfo
			if err := app.host.call(in.ID, "set_limits", req, &info, 20*time.Second); err != nil {
				return nil, err
			}
			app.host.mu.Lock()
			if n := app.host.nodes[in.ID]; n != nil {
				if n.Role != "" && n.Info.Limits.Pedal != info.Limits.Pedal {
					app.host.stale = true // the pedal moved under a running model
				}
				info.ID = in.ID
				n.Info = info
			}
			app.host.mu.Unlock()
			if in.Name != "" && in.ID != app.id.FP {
				app.cfg.Update(func(c *Config) { c.Trusted[in.ID] = in.Name })
			}
			return nil, nil
		}
		raw, _ := json.Marshal(req)
		_, err := app.agent.Handle("set_limits", raw)
		return nil, err
	})

	api("/api/pending", func(r *http.Request) (any, error) {
		h, err := needMain()
		if err != nil {
			return nil, err
		}
		var in struct {
			ID      string `json:"id"`
			Approve bool   `json:"approve"`
		}
		if err := body(r, &in); err != nil {
			return nil, err
		}
		h.mu.Lock()
		p := h.pending[in.ID]
		h.mu.Unlock()
		if p == nil {
			return nil, errors.New("that request is gone")
		}
		select {
		case p.decide <- in.Approve:
		default:
		}
		return nil, nil
	})

	api("/api/forget", func(r *http.Request) (any, error) {
		var in struct {
			ID string `json:"id"`
		}
		_ = body(r, &in)
		if app.helper != nil {
			app.helper.Forget()
			return nil, nil
		}
		h, err := needMain()
		if err != nil {
			return nil, err
		}
		if in.ID == app.id.FP {
			return nil, errors.New("the main computer cannot be removed")
		}
		app.cfg.Update(func(c *Config) { delete(c.Trusted, in.ID) })
		h.mu.Lock()
		if n := h.nodes[in.ID]; n != nil {
			if n.peer != nil {
				n.peer.Close()
			}
			delete(h.nodes, in.ID)
		}
		h.mu.Unlock()
		return nil, nil
	})

	api("/api/manual", func(r *http.Request) (any, error) {
		var in struct{ Addr string }
		if err := body(r, &in); err != nil {
			return nil, err
		}
		if app.helper == nil {
			return nil, errors.New("only helpers connect to a main computer")
		}
		app.helper.SetManual(in.Addr)
		return nil, nil
	})

	api("/api/head", func(r *http.Request) (any, error) {
		if _, err := needMain(); err != nil {
			return nil, err
		}
		var in struct{ ID string }
		if err := body(r, &in); err != nil {
			return nil, err
		}
		app.cfg.Update(func(c *Config) { c.HeadChoice = in.ID })
		return nil, nil
	})

	api("/api/store", func(r *http.Request) (any, error) {
		h, err := needMain()
		if err != nil {
			return nil, err
		}
		return h.storeSearch(r.URL.Query().Get("source"), r.URL.Query().Get("q"))
	})

	api("/api/files", func(r *http.Request) (any, error) {
		h, err := needMain()
		if err != nil {
			return nil, err
		}
		files, err := hfFiles(r.URL.Query().Get("repo"))
		if err != nil {
			return nil, errors.New("could not list that model's files on Hugging Face")
		}
		h.judge(files)
		return files, nil
	})

	api("/api/plan", func(r *http.Request) (any, error) {
		h, err := needMain()
		if err != nil {
			return nil, err
		}
		var m ModelRef
		if err := body(r, &m); err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
		defer cancel()
		if err := resolveModel(ctx, &m); err != nil {
			return nil, err
		}
		info, err := fetchHeader(ctx, m.URL)
		if err != nil {
			return nil, errors.New("could not read that model: " + err.Error())
		}
		// Two ways to run it: pooled across the computers, and (when one computer is close
		// to big enough) on that computer alone with its disk covering the difference.
		return map[string]any{"model": m, "info": info, "plan": h.plan(info, ""), "solo": h.bestSolo(info)}, nil
	})

	api("/api/start", func(r *http.Request) (any, error) {
		h, err := needMain()
		if err != nil {
			return nil, err
		}
		var in struct {
			ModelRef
			Ctx  int    `json:"ctx"`
			Solo string `json:"solo"` // run on this one computer alone
		}
		if err := body(r, &in); err != nil {
			return nil, err
		}
		h.mu.Lock()
		h.paused, h.pauseWhy = nil, ""
		h.mu.Unlock()
		return nil, h.Start(in.ModelRef, in.Ctx, in.Solo)
	})

	api("/api/stop", func(r *http.Request) (any, error) {
		h, err := needMain()
		if err != nil {
			return nil, err
		}
		h.mu.Lock()
		h.paused, h.pauseWhy = nil, ""
		h.mu.Unlock()
		go h.Stop()
		return nil, nil
	})

	api("/api/test", func(r *http.Request) (any, error) {
		h, err := needMain()
		if err != nil {
			return nil, err
		}
		return nil, h.Test()
	})

	api("/api/kit", func(r *http.Request) (any, error) {
		h, err := needMain()
		if err != nil {
			return nil, err
		}
		if r.Method == http.MethodGet {
			return map[string]any{"drives": kitDrives()}, nil
		}
		var in struct{ Path string }
		if err := body(r, &in); err != nil {
			return nil, err
		}
		dir, missing, err := h.MakeKit(in.Path)
		if err != nil {
			return nil, err
		}
		return map[string]any{"dir": dir, "missing": missing}, nil
	})

	api("/api/share", func(r *http.Request) (any, error) {
		h, err := needMain()
		if err != nil {
			return nil, err
		}
		var in struct{ On bool }
		if err := body(r, &in); err != nil {
			return nil, err
		}
		app.cfg.Update(func(c *Config) { c.ShareChat = in.On })
		h.applyShare()
		return nil, nil
	})

	api("/api/storage", func(r *http.Request) (any, error) {
		if app.host != nil {
			if r.Method == http.MethodGet {
				return app.host.Storage(), nil
			}
			var in struct {
				Node   string `json:"node"`
				Action string `json:"action"` // delete_model | clear_cache
				File   string `json:"file"`
			}
			if err := body(r, &in); err != nil {
				return nil, err
			}
			if in.Action != "delete_model" && in.Action != "clear_cache" {
				return nil, errors.New("unknown action")
			}
			var rep StorageReport
			err := app.host.call(in.Node, in.Action, map[string]string{"file": in.File}, &rep, time.Minute)
			return rep, err
		}
		return map[string]StorageReport{app.id.FP: app.agent.Storage()}, nil
	})

	api("/api/quit", func(r *http.Request) (any, error) {
		go func() {
			if app.host != nil {
				app.host.Stop()
			}
			app.agent.Stop()
			time.Sleep(300 * time.Millisecond)
			os.Exit(0)
		}()
		return nil, nil
	})

	api("/api/firewall", func(r *http.Request) (any, error) {
		if _, err := needMain(); err != nil {
			return nil, err
		}
		return nil, allowThroughFirewall()
	})

	api("/api/log", func(r *http.Request) (any, error) {
		var st AgentStatus
		if app.host != nil {
			id := r.URL.Query().Get("node")
			if id == "" {
				app.host.mu.Lock()
				id = app.host.cluster.Head
				app.host.mu.Unlock()
			}
			if id == "" {
				return map[string]string{"log": ""}, nil
			}
			if err := app.host.call(id, "status", nil, &st, 20*time.Second); err != nil {
				return nil, err
			}
		} else {
			st = app.agent.Status()
		}
		return map[string]string{"log": st.HeadLog}, nil
	})

	srv := &http.Server{Addr: "127.0.0.1:" + strconv.Itoa(portUI), Handler: mux}
	return srv.ListenAndServe()
}

// firewall caches the firewall check for a few seconds; it runs a system tool.
func (app *App) firewall() string {
	app.fwMu.Lock()
	defer app.fwMu.Unlock()
	if time.Since(app.fwAt) > 5*time.Second {
		app.fwState, app.fwAt = firewallState(), time.Now()
	}
	return app.fwState
}

// state is everything the control panel shows.
func (app *App) state() map[string]any {
	c := app.cfg.Get()
	out := map[string]any{"version": version, "role": c.Role, "id": app.id.FP}
	if app.host != nil {
		h := app.host
		_ = h.planNodes() // refreshes each node's offer and whether it may work now
		h.mu.Lock()
		nodes := make([]Node, 0, len(h.nodes))
		for _, n := range h.nodes {
			nodes = append(nodes, *n)
		}
		pend := make([]Pending, 0, len(h.pending))
		for _, p := range h.pending {
			pend = append(pend, *p)
		}
		cl := h.cluster
		pauseWhy, stale := h.pauseWhy, h.stale && h.cluster.Phase == "running"
		h.mu.Unlock()
		sort.Slice(nodes, func(i, j int) bool {
			if nodes[i].IsMain != nodes[j].IsMain {
				return nodes[i].IsMain
			}
			return strings.ToLower(nodes[i].Info.Name) < strings.ToLower(nodes[j].Info.Name)
		})
		var total, biggest int64
		for _, n := range nodes {
			total += n.Offer
			if n.Offer > biggest {
				biggest = n.Offer
			}
		}
		ips := localIPs()
		join, shared := "", ""
		if len(ips) > 0 {
			join = "http://" + net.JoinHostPort(ips[0], strconv.Itoa(portJoin))
			shared = "http://" + net.JoinHostPort(ips[0], strconv.Itoa(portChat))
		}
		out["main"] = map[string]any{
			"nodes": nodes, "pending": pend, "cluster": cl, "joinURL": join, "addrs": ips,
			"headChoice": c.HeadChoice, "chatURL": "http://127.0.0.1:" + strconv.Itoa(portChat),
			"totalOffer": total, "biggestOffer": biggest, "lastModel": c.LastModel, "firewall": app.firewall(),
			"shareChat": c.ShareChat, "sharedURL": shared, "dataDir": app.dir, "pauseWhy": pauseWhy, "stale": stale,
		}
	}
	if app.helper != nil {
		out["helper"] = map[string]any{"link": app.helper.State(), "info": app.agent.Info(), "status": app.agent.Status(), "dataDir": app.dir}
	}
	if app.host == nil && app.helper == nil {
		out["info"] = app.agent.Info()
		out["hasToken"] = app.token != nil
	}
	return out
}
