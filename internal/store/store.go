// Package store keeps projects on disk. There is no database: a project is a
// directory with a YAML file, a versions folder and a runtime folder, so a user
// can read, diff and back it up with ordinary tools.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/adks-dev/adks/manifest"
)

const (
	manifestFile = "agent.yaml"
	indexFile    = "index.json"
	maxSnapshots = 50
)

// checkSlug is the store's own boundary: paths are derived from this string,
// so it does not trust the HTTP layer even though both use the same rule.
func checkSlug(slug string) error {
	if !manifest.ValidSlug(slug) {
		return fmt.Errorf("store: invalid project slug %q", slug)
	}
	return nil
}

// Store is the filesystem-backed project repository.
type Store struct {
	root string
	// perSlug serialises writes so two concurrent saves cannot interleave a
	// snapshot and a rename.
	perSlug sync.Map
	// indexMu serialises the shared index.json across different projects.
	indexMu sync.Mutex
}

// ProjectMeta is one row of the project list.
type ProjectMeta struct {
	Slug         string    `json:"slug"`
	Name         string    `json:"name"`
	Updated      time.Time `json:"updated"`
	ManifestHash string    `json:"manifestHash"`
	Model        string    `json:"model"`
	AgentName    string    `json:"agentName"`
	ToolCount    int       `json:"toolCount"`
	ErrorCount   int       `json:"errorCount"`
	WarnCount    int       `json:"warnCount"`
}

type index struct {
	Projects []ProjectMeta `json:"projects"`
}

// New opens (and creates) the store rooted at root.
func New(root string) (*Store, error) {
	if root == "" {
		return nil, fmt.Errorf("store: root directory is empty")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("store: resolve %q: %w", root, err)
	}
	for _, sub := range []string{"projects", "exports"} {
		if err := os.MkdirAll(filepath.Join(abs, sub), 0o755); err != nil {
			return nil, fmt.Errorf("store: create %q: %w", sub, err)
		}
	}
	return &Store{root: abs}, nil
}

// Root is the absolute home directory of the builder.
func (s *Store) Root() string { return s.root }

// ProjectDir is where a project's files live.
func (s *Store) ProjectDir(slug string) string {
	return filepath.Join(s.root, "projects", slug)
}

// RuntimeDir is where a project's sessions, artifacts and audit log go when the
// builder runs it. It is separate from the exported project so trying a project
// does not leave files in the export.
func (s *Store) RuntimeDir(slug string) string {
	return filepath.Join(s.ProjectDir(slug), ".runtime")
}

// ExportsDir is the default destination for exported projects.
func (s *Store) ExportsDir() string { return filepath.Join(s.root, "exports") }

func (s *Store) lock(slug string) func() {
	v, _ := s.perSlug.LoadOrStore(slug, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// List reads every project directory and summarises it. A broken manifest is
// reported in the list rather than hidden: a project the UI cannot parse is
// exactly the one a user needs to see.
func (s *Store) List() ([]ProjectMeta, error) {
	dir := filepath.Join(s.root, "projects")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("store: list projects: %w", err)
	}
	out := make([]ProjectMeta, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		meta, err := s.summarise(e.Name())
		if err != nil {
			out = append(out, ProjectMeta{Slug: e.Name(), Name: e.Name(), ErrorCount: 1})
			continue
		}
		out = append(out, meta)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Updated.After(out[j].Updated) })
	return out, nil
}

func (s *Store) summarise(slug string) (ProjectMeta, error) {
	p, raw, err := s.Load(slug)
	if err != nil {
		return ProjectMeta{}, err
	}
	issues := manifest.Validate(p)
	m := ProjectMeta{
		Slug:         p.Metadata.Slug,
		Name:         p.Metadata.Name,
		Updated:      p.Metadata.Updated,
		ManifestHash: hashOf(raw),
		Model:        p.Spec.Agent.Model.Provider + "/" + p.Spec.Agent.Model.Name,
		AgentName:    p.Spec.Agent.Name,
		ToolCount:    len(p.Spec.Tools.Builtin) + len(p.Spec.Tools.HTTP) + len(p.Spec.Tools.MCP),
	}
	for _, i := range issues {
		if i.Level == manifest.LevelError {
			m.ErrorCount++
		} else {
			m.WarnCount++
		}
	}
	if m.Slug == "" {
		m.Slug = slug
	}
	return m, nil
}

// Load reads and parses one project. The raw bytes are returned too, because the
// editor's YAML tab must show exactly what is on disk.
func (s *Store) Load(slug string) (*manifest.Project, []byte, error) {
	if err := checkSlug(slug); err != nil {
		return nil, nil, err
	}
	path := filepath.Join(s.ProjectDir(slug), manifestFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("load project %q: %w", slug, err)
	}
	p, err := manifest.Decode(raw)
	if err != nil {
		return nil, raw, fmt.Errorf("load project %q: %w", slug, err)
	}
	return p, raw, nil
}

// Create writes a starter project. The slug must be free.
func (s *Store) Create(slug, name string) (*manifest.Project, error) {
	if err := checkSlug(slug); err != nil {
		return nil, err
	}
	if strings.TrimSpace(name) == "" {
		name = slug
	}
	p := manifest.New(slug, name)
	if issues := manifest.Validate(p); manifest.HasErrors(issues) {
		for _, i := range issues {
			if i.Level == manifest.LevelError {
				return nil, fmt.Errorf("invalid project %s: %s", i.Path, i.Message)
			}
		}
	}
	if err := os.MkdirAll(filepath.Join(s.root, "projects"), 0o755); err != nil {
		return nil, fmt.Errorf("store: prepare projects directory: %w", err)
	}
	// Mkdir is the atomic "claim" for the slug: Stat-then-Save lets two
	// concurrent creates both report success and silently overwrite each other.
	if err := os.Mkdir(s.ProjectDir(slug), 0o755); err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("project %q already exists", slug)
		}
		return nil, fmt.Errorf("store: create project %q: %w", slug, err)
	}
	if _, _, err := s.Save(slug, p, false); err != nil {
		return nil, err
	}
	return p, nil
}

// Save writes a project atomically and snapshots the previous version.
//
// The snapshot is taken before the rename, so a save that fails half way leaves
// both the old manifest and a recoverable copy of it.
func (s *Store) Save(slug string, p *manifest.Project, snapshot bool) (*manifest.Project, []manifest.Issue, error) {
	if err := checkSlug(slug); err != nil {
		return nil, nil, err
	}
	unlock := s.lock(slug)
	defer unlock()
	return s.saveLocked(slug, p, snapshot)
}

// saveLocked is Save's body. Callers must already hold s.lock(slug); keeping it
// separate lets Rollback hold the same lock across snapshot and write.
func (s *Store) saveLocked(slug string, p *manifest.Project, snapshot bool) (*manifest.Project, []manifest.Issue, error) {
	if p.Metadata.Slug == "" {
		p.Metadata.Slug = slug
	}
	p.Touch(time.Now())
	issues := manifest.Validate(p)

	dir := s.ProjectDir(slug)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return p, issues, fmt.Errorf("save project %q: %w", slug, err)
	}
	if snapshot {
		if err := s.snapshot(slug); err != nil {
			return p, issues, err
		}
	}
	raw, err := manifest.Encode(p)
	if err != nil {
		return p, issues, err
	}
	if err := writeAtomic(filepath.Join(dir, manifestFile), raw); err != nil {
		return p, issues, err
	}
	if err := s.reindex(); err != nil {
		return p, issues, err
	}
	return p, issues, nil
}

// SaveRaw parses YAML from the editor, then saves. Parse errors are returned as an
// issue rather than an error so the UI can show them next to the editor.
func (s *Store) SaveRaw(slug string, raw []byte) (*manifest.Project, []manifest.Issue, error) {
	if err := checkSlug(slug); err != nil {
		return nil, nil, err
	}
	p, err := manifest.Decode(raw)
	if err != nil {
		return nil, []manifest.Issue{{Level: manifest.LevelError, Path: "yaml", Message: err.Error()}}, nil
	}
	return s.Save(slug, p, true)
}

func (s *Store) snapshot(slug string) error {
	path := filepath.Join(s.ProjectDir(slug), manifestFile)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	dir := filepath.Join(s.ProjectDir(slug), "versions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	name := time.Now().UTC().Format("20060102T150405.000000000Z") + ".yaml"
	if err := writeAtomic(filepath.Join(dir, name), raw); err != nil {
		return err
	}
	return pruneVersions(dir, maxSnapshots)
}

func pruneVersions(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > keep {
		if err := os.Remove(filepath.Join(dir, names[0])); err != nil {
			return err
		}
		names = names[1:]
	}
	return nil
}

// Versions lists snapshot names, newest first.
func (s *Store) Versions(slug string) ([]string, error) {
	if err := checkSlug(slug); err != nil {
		return nil, err
	}
	dir := filepath.Join(s.ProjectDir(slug), "versions")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".yaml") {
			out = append(out, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out, nil
}

// ReadVersion returns the YAML of one snapshot.
func (s *Store) ReadVersion(slug, name string) ([]byte, error) {
	if err := checkSlug(slug); err != nil {
		return nil, err
	}
	if strings.ContainsAny(name, `/\`) {
		return nil, fmt.Errorf("invalid version name %q", name)
	}
	return os.ReadFile(filepath.Join(s.ProjectDir(slug), "versions", name))
}

// Rollback restores a snapshot. The current manifest is snapshotted first so a
// rollback is itself reversible.
func (s *Store) Rollback(slug, name string) (*manifest.Project, []manifest.Issue, error) {
	if err := checkSlug(slug); err != nil {
		return nil, nil, err
	}
	raw, err := s.ReadVersion(slug, name)
	if err != nil {
		return nil, nil, fmt.Errorf("rollback %q to %q: %w", slug, name, err)
	}
	p, err := manifest.Decode(raw)
	if err != nil {
		return nil, []manifest.Issue{{Level: manifest.LevelError, Path: "yaml", Message: err.Error()}}, nil
	}
	// Snapshot and write must share one critical section, otherwise a concurrent
	// save lands between them and the rollback snapshots the wrong state.
	unlock := s.lock(slug)
	defer unlock()
	if err := s.snapshot(slug); err != nil {
		return nil, nil, err
	}
	return s.saveLocked(slug, p, false)
}

// Delete removes a project directory.
func (s *Store) Delete(slug string) error {
	if err := checkSlug(slug); err != nil {
		return err
	}
	unlock := s.lock(slug)
	defer unlock()
	if err := os.RemoveAll(s.ProjectDir(slug)); err != nil {
		return fmt.Errorf("delete project %q: %w", slug, err)
	}
	return s.reindex()
}

func (s *Store) reindex() error {
	// Projects have independent per-slug locks, but index.json is shared: two
	// saves for different slugs must not overwrite each other's snapshot.
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	list, err := s.List()
	if err != nil {
		return err
	}
	buf, err := json.MarshalIndent(index{Projects: list}, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(s.root, indexFile), buf)
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Windows refuses to replace an open file; the rename below works because the
	// temp file is already closed.
	return os.Rename(name, path)
}

func hashOf(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Hash is exported for callers that compare a manifest against its last run.
func Hash(b []byte) string { return hashOf(b) }
