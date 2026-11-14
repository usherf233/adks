package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/usherf233/adks/kit"
	"github.com/usherf233/adks/manifest"
)

// probeModel makes one tiny real call, because a key that parses is not the same
// as a key that works.
func (s *Server) probeModel(w http.ResponseWriter, r *http.Request) {
	slug := slugOf(r)
	p, _, err := s.store.Load(slug)
	if err != nil {
		writeLoadErr(w, err, http.StatusBadRequest)
		return
	}
	start := time.Now()
	detail, err := kit.ProbeModel(r.Context(), p.Spec.Agent.Model)
	dur := time.Since(start).Milliseconds()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":     false,
			"model":  p.Spec.Agent.Model.Provider + "/" + p.Spec.Agent.Model.Name,
			"detail": err.Error(),
			"durMs":  dur,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"model":  p.Spec.Agent.Model.Provider + "/" + p.Spec.Agent.Model.Name,
		"detail": fmt.Sprintf("replied %q", strings.TrimSpace(detail)),
		"durMs":  dur,
	})
}

// probeSQL creates and deletes one session, which exercises dialect support, the
// DSN and the migration in a single step.
func (s *Server) probeSQL(w http.ResponseWriter, r *http.Request) {
	slug := slugOf(r)
	p, _, err := s.store.Load(slug)
	if err != nil {
		writeLoadErr(w, err, http.StatusBadRequest)
		return
	}
	// Only the saved manifest decides which database is probed. Letting the
	// request override backend/DSN turned this endpoint into an arbitrary network
	// and filesystem probe; the UI always posts an empty object anyway.
	spec := p.Spec.Runtime.Session
	runtimeDir := s.store.RuntimeDir(slug)
	if spec.Backend == "sqlite" {
		spec.DSN = rebaseSQLiteDSN(spec.DSN, runtimeDir)
	}

	start := time.Now()
	detail, err := kit.ProbeSession(r.Context(), spec)
	dur := time.Since(start).Milliseconds()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "detail": err.Error(), "durMs": dur})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "detail": detail, "durMs": dur})
}

// probeMCP connects, lists the tools the agent would see, and disconnects.
func (s *Server) probeMCP(w http.ResponseWriter, r *http.Request) {
	slug := slugOf(r)
	p, _, err := s.store.Load(slug)
	if err != nil {
		writeLoadErr(w, err, http.StatusBadRequest)
		return
	}
	var req struct {
		MCPID string `json:"mcpId"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	var ref *manifest.MCPRef
	for i := range p.Spec.Tools.MCP {
		if p.Spec.Tools.MCP[i].ID == req.MCPID {
			ref = &p.Spec.Tools.MCP[i]
			break
		}
	}
	if ref == nil {
		writeErr(w, http.StatusNotFound, errors.New("no MCP server with that id in this project"))
		return
	}

	start := time.Now()
	tools, err := kit.ProbeMCP(r.Context(), *ref, kit.MCPDeps{})
	dur := time.Since(start).Milliseconds()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "detail": err.Error(), "durMs": dur})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":    true,
		"tools": tools,
		"count": len(tools),
		"durMs": dur,
	})
}
