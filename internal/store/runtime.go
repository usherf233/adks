package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/usherf233/adks/manifest"
)

// PrepareRuntime returns a copy of p whose relative paths are rebased onto the
// project's runtime directory.
//
// Without this a trial run would write sessions.db and artifacts into the builder
// process's working directory, which is both surprising and a way to lose state
// between two different projects. The copy matters: the caller's manifest must
// keep the paths the user typed, because those are what gets exported.
func (s *Store) PrepareRuntime(p *manifest.Project, slug string) (*manifest.Project, error) {
	dir := s.RuntimeDir(slug)
	raw, err := manifest.Encode(p)
	if err != nil {
		return nil, err
	}
	clone, err := manifest.Decode(raw)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create runtime directory: %w", err)
	}

	sess := &clone.Spec.Runtime.Session
	if sess.Backend == "sqlite" {
		sess.DSN = RebaseSQLiteDSN(sess.DSN, dir)
	}
	art := &clone.Spec.Runtime.Artifact
	if art.Backend == "local" {
		if art.Dir == "" || strings.HasPrefix(art.Dir, "./") || art.Dir == "." {
			art.Dir = filepath.Join(dir, "artifacts")
		}
	}
	return clone, nil
}

// RebaseSQLiteDSN rewrites a relative sqlite DSN to an absolute one under dir,
// keeping any driver pragmas intact.
//
// It is exported because the SQL probe applies the same rule: two copies of
// "where does the builder's sqlite file live" would drift.
func RebaseSQLiteDSN(dsn, dir string) string {
	if dsn == "" {
		return filepath.Join(dir, "sessions.db")
	}
	if !strings.HasPrefix(dsn, "file:") {
		if filepath.IsAbs(dsn) {
			return dsn
		}
		return filepath.Join(dir, filepath.Base(dsn))
	}
	rest := strings.TrimPrefix(dsn, "file:")
	pathPart, query, hasQuery := strings.Cut(rest, "?")
	if filepath.IsAbs(pathPart) {
		return dsn
	}
	abs := filepath.Join(dir, filepath.Base(pathPart))
	if hasQuery {
		return "file:" + abs + "?" + query
	}
	return "file:" + abs
}
