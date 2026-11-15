package api

import (
	"errors"
	"net/http"

	"github.com/usherf233/adks/internal/play"
	"github.com/usherf233/adks/internal/store"
)

// evals returns the project's regression cases as text, so the editor can show
// what the file actually says rather than a re-serialised version of it.
func (s *Server) evals(w http.ResponseWriter, r *http.Request) {
	slug := slugOf(r)
	if _, _, err := s.store.Load(slug); err != nil {
		writeLoadErr(w, err, http.StatusBadRequest)
		return
	}
	raw, err := s.store.LoadEvals(slug)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	suite, perr := play.ParseEvals(raw)
	resp := map[string]any{"yaml": string(raw)}
	if perr != nil {
		// A broken file must not be a 500: the editor needs to show the parse error
		// and let the user fix it.
		resp["error"] = perr.Error()
	} else {
		resp["cases"] = suite.Cases
	}
	writeJSON(w, http.StatusOK, resp)
}

// saveEvals validates before writing. A file that does not parse would turn every
// later run into a confusing failure, so it is rejected at the door.
func (s *Server) saveEvals(w http.ResponseWriter, r *http.Request) {
	slug := slugOf(r)
	var req struct {
		YAML string `json:"yaml"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	suite, err := play.ParseEvals([]byte(req.YAML))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.store.SaveEvals(slug, []byte(req.YAML)); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "cases": suite.Cases})
}

// runEvals executes the stored cases. It is synchronous and slow by nature: every
// case is a real model run, which is the point.
func (s *Server) runEvals(w http.ResponseWriter, r *http.Request) {
	slug := slugOf(r)
	if _, _, err := s.store.Load(slug); err != nil {
		writeLoadErr(w, err, http.StatusBadRequest)
		return
	}
	var req struct {
		// YAML, when present, runs what the editor currently holds rather than what
		// was last saved, so a case can be tried before committing it.
		YAML string `json:"yaml"`
	}
	readOptionalJSON(r, &req)

	raw := []byte(req.YAML)
	if len(raw) == 0 {
		stored, err := s.store.LoadEvals(slug)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		raw = stored
	}
	suite, err := play.ParseEvals(raw)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(suite.Cases) == 0 {
		writeErr(w, http.StatusBadRequest, play.ErrNoEvals)
		return
	}
	report, err := s.play.RunEvals(r.Context(), slug, suite)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, report)
}
