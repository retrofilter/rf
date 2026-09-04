package console

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func viewOf(r *http.Request) string {
	switch v := r.FormValue("view"); v {
	case viewNew, viewOverview, viewGraph:
		return v
	}
	return viewTerminal
}

func (s *Server) model(active, view string, graphOpen bool) (viewModel, error) {
	sessions, err := s.reg.Sessions()
	if err != nil {
		return viewModel{}, err
	}
	return viewModel{
		Sessions:  sessions,
		Scheduled: s.scheduledRows(),
		Active:    active,
		View:      view,
		GraphOpen: graphOpen || view == viewGraph,
		Usage:     s.usage.Get(),
		Sys:       s.sys.Get(),
		Whoami:    whoami(),
		Now:       time.Now(),
	}, nil
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	m, err := s.model("", viewTerminal, false)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = page(m).Render(r.Context(), w)
}

func (s *Server) handlePoll(w http.ResponseWriter, r *http.Request) {
	m, err := s.model(r.FormValue("active"), viewOf(r), r.FormValue("graph") == "1")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	setTriggers(w, pollEvents(m))
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = pollFragment(m).Render(r.Context(), w)
}

func pollEvents(m viewModel) map[string]any {
	events := map[string]any{}
	switch {
	case len(m.Sessions) == 0:
		if m.Active != "" {
			events["rf:detached"] = true
		}
		if m.View == viewTerminal {
			events["rf:showoverview"] = true
		}
	case m.activeSession() == nil && m.View == viewTerminal:
		events["rf:select"] = map[string]string{"id": m.Sessions[0].ID}
	case m.Active != "" && m.activeSession() == nil:
		events["rf:detached"] = true
	}
	return events
}

func setTriggers(w http.ResponseWriter, events map[string]any) {
	if len(events) == 0 {
		return
	}
	if payload, err := json.Marshal(events); err == nil {
		w.Header().Set("HX-Trigger", string(payload))
	}
}

func (s *Server) handleSpawn(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if !nameRe.MatchString(name) {
		spawnError(w, r, http.StatusBadRequest, "session name must match [a-zA-Z0-9_-]+")
		return
	}
	id, err := s.mgr.Spawn(name, "", "")
	if errors.Is(err, ErrNameTaken) {
		id = s.mgr.FindByName(name)
		if id == "" {
			spawnError(w, r, http.StatusConflict, "session already exists: "+name)
			return
		}
	} else if err != nil {
		spawnError(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	s.reg.Invalidate()
	setTriggers(w, map[string]any{"rf:select": map[string]string{"id": id}})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// An empty body clears any prior error from #nv-error.
}

func spawnError(w http.ResponseWriter, r *http.Request, code int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)
	_ = errorText(msg).Render(r.Context(), w)
}

func (s *Server) handleClose(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.Close(r.FormValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.reg.Invalidate()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = overviewList(s.overview()).Render(r.Context(), w)
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseUint(r.FormValue("id"), 10, 32)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	spec, err := s.startSpecFor(r.FormValue("kind"), uint32(id))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sid, err := s.mgr.Spawn(spec.name, spec.dir, spec.initial)
	if errors.Is(err, ErrNameTaken) {
		sid = s.mgr.FindByName(spec.name)
		if sid == "" {
			http.Error(w, "session already exists: "+spec.name, http.StatusConflict)
			return
		}
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.reg.Invalidate()
	setTriggers(w, map[string]any{"rf:select": map[string]string{"id": sid}})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
}
