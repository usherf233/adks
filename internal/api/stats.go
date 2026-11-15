package api

import (
	"net/http"
	"strconv"
)

// projectStats reports what the project has cost so far.
//
// It reads the same file the trial run appends to, so the number here and the
// line under each turn cannot disagree. A project that has never run returns an
// empty account rather than a 404: the endpoint describes a project, and the
// project exists.
func (s *Server) projectStats(w http.ResponseWriter, r *http.Request) {
	slug := slugOf(r)
	if _, _, err := s.store.Load(slug); err != nil {
		writeLoadErr(w, err, http.StatusBadRequest)
		return
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	sum, err := s.play.Stats(slug, limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}
