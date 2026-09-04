package console

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

func (s *Server) handleRename(w http.ResponseWriter, r *http.Request) {
	spawnName := r.URL.Query().Get("session")
	if spawnName == "" {
		http.Error(w, "session parameter required", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if !nameRe.MatchString(name) {
		http.Error(w, "session name must match [a-zA-Z0-9_-]+", http.StatusBadRequest)
		return
	}
	switch err := s.mgr.Rename(spawnName, name); {
	case errors.Is(err, ErrNoSession):
		http.Error(w, "no such session: "+spawnName, http.StatusNotFound)
		return
	case errors.Is(err, ErrNameTaken):
		http.Error(w, "session name already in use: "+name, http.StatusConflict)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.reg.Invalidate()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.reg.Sessions()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	sessions = append(append([]Session{}, sessions...), s.scheduledRows()...)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(sessions)
}

func (s *Server) handleNewSession(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if !nameRe.MatchString(name) {
		http.Error(w, "session name must match [a-zA-Z0-9_-]+", http.StatusBadRequest)
		return
	}
	id, err := s.mgr.Spawn(name, r.FormValue("dir"), r.FormValue("run"))
	if errors.Is(err, ErrNameTaken) {
		http.Error(w, "session already exists: "+name, http.StatusConflict)
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.reg.Invalidate()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"id": id, "name": name})
}
