package api

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/adks-dev/adks/manifest"
)

// runtimeProject rebases relative paths onto the project's runtime directory.
//
// Without this a trial run would write sessions.db and artifacts into the builder
// process's working directory, which is both surprising and a way to lose state
// between two different projects.
func runtimeProject(p *manifest.Project, runtimeDir string) (*manifest.Project, error) {
	raw, err := manifest.Encode(p)
	if err != nil {
		return nil, err
	}
	clone, err := manifest.Decode(raw)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return nil, fmt.Errorf("create runtime directory: %w", err)
	}

	sess := &clone.Spec.Runtime.Session
	if sess.Backend == "sqlite" {
		sess.DSN = rebaseSQLiteDSN(sess.DSN, runtimeDir)
	}
	art := &clone.Spec.Runtime.Artifact
	if art.Backend == "local" {
		if art.Dir == "" || strings.HasPrefix(art.Dir, "./") || art.Dir == "." {
			art.Dir = filepath.Join(runtimeDir, "artifacts")
		}
	}
	return clone, nil
}

// rebaseSQLiteDSN rewrites a relative sqlite DSN to an absolute one under dir,
// keeping any driver pragmas intact.
func rebaseSQLiteDSN(dsn, dir string) string {
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
