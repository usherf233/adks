// Package api is the builder's HTTP surface: project CRUD, validation, probes,
// a streaming trial run, export and verification.
package api

import (
	"io/fs"
	"log"
	"net/http"

	"github.com/usherf233/adks/internal/store"
)

// Server wires the store and the embedded assets into a handler.
type Server struct {
	store   *store.Store
	seed    fs.FS
	version string
	token   string
	layers  *layerCache
	plays   *playHub
}

// New builds a server. seed must contain the kit and manifest sources, which are
// copied verbatim into exports.
func New(st *store.Store, seed fs.FS, version, token string) *Server {
	return &Server{
		store:   st,
		seed:    seed,
		version: version,
		token:   token,
		layers:  newLayerCache(st),
		plays:   newPlayHub(),
	}
}

// Close releases every compiled layer and drops pending confirmations. Call it
// on shutdown so MCP child processes and SQLite handles do not outlive the server.
func (s *Server) Close() {
	if s.layers != nil {
		s.layers.CloseAll()
	}
	if s.plays != nil {
		s.plays.Close()
	}
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": s.version, "home": s.store.Root()})
}

// Handler returns the router. The middleware order is deliberate: guard runs
// outside auth so an unauthenticated request cannot learn which slugs exist.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/health", s.health)

	mux.HandleFunc("GET /api/projects", s.listProjects)
	mux.HandleFunc("POST /api/projects", s.createProject)
	mux.HandleFunc("GET /api/projects/{slug}", s.getProject)
	mux.HandleFunc("PUT /api/projects/{slug}", s.putProject)
	mux.HandleFunc("DELETE /api/projects/{slug}", s.deleteProject)

	mux.HandleFunc("GET /api/projects/{slug}/versions", s.listVersions)
	mux.HandleFunc("POST /api/projects/{slug}/rollback", s.rollback)
	mux.HandleFunc("GET /api/projects/{slug}/graph", s.graph)

	mux.HandleFunc("POST /api/projects/{slug}/probe/model", s.probeModel)
	mux.HandleFunc("POST /api/projects/{slug}/probe/sql", s.probeSQL)
	mux.HandleFunc("POST /api/projects/{slug}/probe/mcp", s.probeMCP)

	mux.HandleFunc("POST /api/projects/{slug}/play", s.play)
	mux.HandleFunc("POST /api/projects/{slug}/play/confirm", s.confirm)

	mux.HandleFunc("GET /api/projects/{slug}/files", s.files)
	mux.HandleFunc("POST /api/projects/{slug}/verify", s.verify)
	mux.HandleFunc("POST /api/projects/{slug}/export", s.export)

	web, err := fs.Sub(s.seed, "web")
	if err != nil {
		log.Printf("api: web assets unavailable: %v", err)
	} else {
		mux.Handle("GET /", http.FileServerFS(web))
	}

	return s.guard(s.auth(mux))
}
