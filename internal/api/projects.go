package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/adks-dev/adks/internal/store"
	"github.com/adks-dev/adks/manifest"
)

func (s *Server) listProjects(w http.ResponseWriter, _ *http.Request) {
	list, err := s.store.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if list == nil {
		list = []store.ProjectMeta{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": list, "home": s.store.Root()})
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	req.Slug = strings.TrimSpace(req.Slug)
	if req.Slug == "" {
		writeErr(w, http.StatusBadRequest, errors.New("slug is required"))
		return
	}
	// POST has no {slug} path segment, so the guard middleware cannot check it.
	// Use the same rule as the guard and the store rather than a third copy.
	if !manifest.ValidSlug(req.Slug) {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid project slug %q", req.Slug))
		return
	}
	p, err := s.store.Create(req.Slug, req.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"slug": p.Metadata.Slug, "id": p.Metadata.ID})
}

// projectView is what the editor needs in one round trip.
type projectView struct {
	Slug         string            `json:"slug"`
	Name         string            `json:"name"`
	ManifestYAML string            `json:"manifestYaml"`
	Manifest     *manifest.Project `json:"manifest"`
	Issues       []manifest.Issue  `json:"issues"`
	Versions     []string          `json:"versions"`
	Estimate     estimate          `json:"estimate"`
	LoadError    string            `json:"loadError,omitempty"`
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	slug := slugOf(r)
	p, raw, err := s.store.Load(slug)
	if err != nil {
		// A manifest that does not parse still has to be openable: that is
		// precisely when the user needs the editor.
		writeJSON(w, http.StatusOK, projectView{
			Slug:         slug,
			LoadError:    err.Error(),
			ManifestYAML: string(raw),
			Issues:       []manifest.Issue{{Level: manifest.LevelError, Path: "yaml", Message: err.Error()}},
		})
		return
	}
	versions, _ := s.store.Versions(slug)
	writeJSON(w, http.StatusOK, projectView{
		Slug:         p.Metadata.Slug,
		Name:         p.Metadata.Name,
		ManifestYAML: string(raw),
		Manifest:     p,
		Issues:       manifest.Validate(p),
		Versions:     versions,
		Estimate:     estimateFor(p),
	})
}

func (s *Server) putProject(w http.ResponseWriter, r *http.Request) {
	slug := slugOf(r)
	var req struct {
		ManifestYAML string            `json:"manifestYaml"`
		Manifest     *manifest.Project `json:"manifest"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	var (
		p      *manifest.Project
		issues []manifest.Issue
		err    error
	)
	if req.Manifest != nil {
		// The form posts JSON: the browser never needs a YAML library, and the
		// YAML on disk stays the single source of truth.
		req.Manifest.Metadata.Slug = slug
		// The form only sends what the user touched, so defaults are re-applied
		// here: otherwise clearing apiKeyEnv would leave the manifest without one.
		manifest.ApplyDefaults(req.Manifest)
		p, issues, err = s.store.Save(slug, req.Manifest, true)
	} else {
		p, issues, err = s.store.SaveRaw(slug, []byte(req.ManifestYAML))
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if p == nil {
		// SaveRaw reports a YAML parse failure as issues for the editor. There is
		// no project to summarise and no compiled layer to invalidate.
		writeJSON(w, http.StatusOK, map[string]any{"issues": issues, "saved": false})
		return
	}

	// A changed manifest invalidates the compiled layer, but not here: the cache
	// compares hashes on the next run so an unsaved edit never costs a rebuild.
	s.layers.Invalidate(slug)
	writeJSON(w, http.StatusOK, map[string]any{
		"issues":   issues,
		"slug":     p.Metadata.Slug,
		"estimate": estimateFor(p),
	})
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	slug := slugOf(r)
	s.layers.Invalidate(slug)
	if err := s.store.Delete(slug); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) listVersions(w http.ResponseWriter, r *http.Request) {
	versions, err := s.store.Versions(slugOf(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if versions == nil {
		versions = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"versions": versions})
}

func (s *Server) rollback(w http.ResponseWriter, r *http.Request) {
	slug := slugOf(r)
	var req struct {
		Version string `json:"version"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	p, issues, err := s.store.Rollback(slug, req.Version)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if p == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "issues": issues})
		return
	}
	s.layers.Invalidate(slug)
	raw, _ := manifest.Encode(p)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":           true,
		"issues":       issues,
		"manifestYaml": string(raw),
		"manifest":     p,
		"estimate":     estimateFor(p),
	})
}
