package api

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/usherf233/adks/internal/codegen"
	"github.com/usherf233/adks/internal/store"
	"github.com/usherf233/adks/internal/verify"
	"github.com/usherf233/adks/manifest"
)

// filesView is a preview of what an export would contain.
type filesView struct {
	Path    string `json:"path"`
	Bytes   int    `json:"bytes"`
	Preview string `json:"preview,omitempty"`
	Binary  bool   `json:"binary,omitempty"`
}

func (s *Server) files(w http.ResponseWriter, r *http.Request) {
	slug := slugOf(r)
	p, _, err := s.store.Load(slug)
	if err != nil {
		writeLoadErr(w, err, http.StatusBadRequest)
		return
	}
	b, err := codegen.Render(p, s.seed)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	out := make([]filesView, 0, len(b.Files))
	for _, rel := range b.Paths() {
		content := b.Files[rel]
		v := filesView{Path: rel, Bytes: len(content)}
		if isTextual(rel) {
			preview := string(content)
			if len(preview) > 1200 {
				preview = preview[:1200] + "\n…"
			}
			v.Preview = preview
		} else {
			v.Binary = true
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": out, "totalBytes": b.Bytes()})
}

// newExportDir creates a unique destination so concurrent exports and verifies
// never remove or overwrite each other's files. The timestamp keeps directories
// readable; MkdirTemp's random suffix makes collisions impossible.
//
// Older directories are pruned here rather than on a timer: this is the only
// place that grows the directory, so it is the only place that has to bound it.
func newExportDir(base, slug, kind string) (string, error) {
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", fmt.Errorf("prepare exports directory: %w", err)
	}
	if err := store.PruneExportDirs(base, store.MaxExportDirs); err != nil {
		// Not fatal: a tidy exports directory is housekeeping, and failing an
		// export because an old directory could not be removed would be worse.
		log.Printf("api: prune exports: %v", err)
	}
	pattern := slug + "-" + kind + "-" + time.Now().Format("20060102-150405") + "-*"
	dir, err := os.MkdirTemp(base, pattern)
	if err != nil {
		return "", fmt.Errorf("create %s directory: %w", kind, err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

func isTextual(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".go", ".md", ".yaml", ".yml", ".mod", ".sum", ".txt", "":
		return true
	}
	return strings.EqualFold(path.Base(name), "Dockerfile") || strings.HasPrefix(path.Base(name), ".git")
}

// verify renders the project to disk and runs the deterministic checks. Rendering
// to a real directory is deliberate: go build needs one, and a compile error is
// only meaningful against the files that would ship.
func (s *Server) verify(w http.ResponseWriter, r *http.Request) {
	slug := slugOf(r)
	var req struct {
		Smoke bool `json:"smoke"`
	}
	readOptionalJSON(r, &req)

	p, _, err := s.store.Load(slug)
	if err != nil {
		writeLoadErr(w, err, http.StatusBadRequest)
		return
	}
	b, err := codegen.Render(p, s.seed)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	dir, err := newExportDir(s.store.ExportsDir(), slug, "verify")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	finished := false
	defer func() {
		if !finished {
			_ = os.RemoveAll(dir)
		}
	}()
	if err := codegen.WriteDir(b, dir); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// Resolve requirements before building: the copied go.mod carries the
	// builder's indirect markers, which plain `go build` rejects.
	// Both export and verify use the same tidy budget; without it a hung proxy
	// could hold the verification request open indefinitely.
	tidyCtx, cancel := context.WithTimeout(r.Context(), tidyTimeout)
	tidyErr := codegen.Tidy(tidyCtx, dir)
	cancel()
	if tidyErr != nil {
		// Not fatal: the checks below run with -mod=mod, so a missing proxy
		// downgrades to "slower first build", not "broken".
		log.Printf("api: verify tidy %q: %v", dir, tidyErr)
	}

	checks, err := verify.Run(r.Context(), dir, verify.Options{SkipSmoke: !req.Smoke})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	finished = true
	ok := true
	for _, c := range checks {
		if !c.OK {
			ok = false
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": ok, "dir": dir, "checks": checks})
}

// export writes a self-contained module and optionally returns it as a zip. The
// directory is always written: when the browser downloads a zip, the user
// usually still wants to know where the source lives.
func (s *Server) export(w http.ResponseWriter, r *http.Request) {
	slug := slugOf(r)
	var req struct {
		Zip bool `json:"zip"`
	}
	readOptionalJSON(r, &req)

	p, _, err := s.store.Load(slug)
	if err != nil {
		writeLoadErr(w, err, http.StatusBadRequest)
		return
	}
	if err := validateForExport(p); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	b, err := codegen.Render(p, s.seed)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	dir, err := newExportDir(s.store.ExportsDir(), slug, "export")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	finished := false
	defer func() {
		if !finished {
			_ = os.RemoveAll(dir)
		}
	}()
	if err := codegen.WriteDir(b, dir); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	tidy := "ok"
	tidyCtx, cancel := context.WithTimeout(r.Context(), tidyTimeout)
	if err := codegen.Tidy(tidyCtx, dir); err != nil {
		tidy = "skipped: " + err.Error()
		log.Printf("api: export tidy %q: %v", dir, err)
	}
	cancel()
	finished = true

	// Build the archive in memory so a failure can still be reported as a 500
	// and can never leave the client with a truncated zip.
	if req.Zip {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		if err := codegen.WriteZip(b, zw); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if err := zw.Close(); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": slug + ".zip"}))
		_, _ = w.Write(buf.Bytes())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dir": dir, "files": len(b.Files), "bytes": b.Bytes(), "tidy": tidy})
}

// validateForExport refuses to ship a manifest with errors. Warnings do not block:
// they are advice about prompt size and cache behaviour, not correctness.
func validateForExport(p *manifest.Project) error {
	for _, i := range manifest.Validate(p) {
		if i.Level == manifest.LevelError {
			return fmt.Errorf("manifest has errors: %s: %s", i.Path, i.Message)
		}
	}
	return nil
}
