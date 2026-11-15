package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usherf233/adks/manifest"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestCreateLoadSave(t *testing.T) {
	s := newStore(t)
	p, err := s.Create("support-bot", "客服助手")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.Metadata.ID == "" || !strings.HasPrefix(p.Metadata.ID, "prj_") {
		t.Fatalf("id not generated: %q", p.Metadata.ID)
	}

	loaded, raw, err := s.Load("support-bot")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Metadata.Name != "客服助手" {
		t.Fatalf("name lost: %q", loaded.Metadata.Name)
	}
	if len(raw) == 0 {
		t.Fatal("raw YAML must be returned for the editor")
	}

	loaded.Spec.Agent.Description = "changed"
	if _, issues, err := s.Save("support-bot", loaded, true); err != nil {
		t.Fatalf("Save: %v", err)
	} else if countErrors(issues) > 0 {
		t.Fatalf("unexpected validation errors: %v", issues)
	}

	again, _, err := s.Load("support-bot")
	if err != nil {
		t.Fatal(err)
	}
	if again.Spec.Agent.Description != "changed" {
		t.Fatal("save did not persist")
	}
}

func TestCreateRejectsDuplicateAndBadSlug(t *testing.T) {
	s := newStore(t)
	if _, err := s.Create("demo-app", "Demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("demo-app", "Demo again"); err == nil {
		t.Fatal("expected duplicate slug to be rejected")
	}
	if _, err := s.Create("Demo App", "bad"); err == nil {
		t.Fatal("expected invalid slug to be rejected")
	}
}

func TestVersionsAndRollback(t *testing.T) {
	s := newStore(t)
	p, err := s.Create("demo-app", "Demo")
	if err != nil {
		t.Fatal(err)
	}

	for _, desc := range []string{"v1", "v2", "v3"} {
		p.Spec.Agent.Description = desc
		if _, _, err := s.Save("demo-app", p, true); err != nil {
			t.Fatal(err)
		}
	}

	versions, err := s.Versions("demo-app")
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) < 3 {
		t.Fatalf("expected at least 3 snapshots, got %v", versions)
	}

	// Roll back to the oldest snapshot: the description should be the one that was
	// on disk before the first overwrite ("v1" was written by the second save).
	oldest := versions[len(versions)-1]
	if _, _, err := s.Rollback("demo-app", oldest); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	after, _, err := s.Load("demo-app")
	if err != nil {
		t.Fatal(err)
	}
	if after.Spec.Agent.Description == "v3" {
		t.Fatal("rollback did not change the manifest")
	}
	// The rollback itself must be reversible.
	versions, _ = s.Versions("demo-app")
	if len(versions) < 4 {
		t.Fatalf("rollback should have snapshotted the current version, got %v", versions)
	}
}

func TestSaveRawReportsParseErrorsAsIssues(t *testing.T) {
	s := newStore(t)
	if _, err := s.Create("demo-app", "Demo"); err != nil {
		t.Fatal(err)
	}
	_, issues, err := s.SaveRaw("demo-app", []byte("apiVersion: [oops"))
	if err != nil {
		t.Fatalf("parse errors must not be transport errors: %v", err)
	}
	if len(issues) != 1 || issues[0].Level != "error" {
		t.Fatalf("expected one error issue, got %v", issues)
	}
}

func TestDeleteRemovesProject(t *testing.T) {
	s := newStore(t)
	if _, err := s.Create("demo-app", "Demo"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("demo-app"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Load("demo-app"); err == nil {
		t.Fatal("expected load to fail after delete")
	}
	list, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("list should be empty, got %v", list)
	}
}

func TestStoreRejectsUnsafeSlug(t *testing.T) {
	s := newStore(t)
	for _, slug := range []string{"../outside", `..\outside`, "UPPER", ""} {
		if err := s.Delete(slug); err == nil {
			t.Errorf("Delete(%q) should be rejected", slug)
		}
		if _, _, err := s.Load(slug); err == nil {
			t.Errorf("Load(%q) should be rejected", slug)
		}
		if _, _, err := s.SaveRaw(slug, []byte("x")); err == nil {
			t.Errorf("SaveRaw(%q) should be rejected", slug)
		}
	}
}

func TestMissingProjectIsNotFound(t *testing.T) {
	s := newStore(t)
	if _, _, err := s.Load("ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Load of a missing project: err = %v, want ErrNotFound", err)
	}
	if _, err := s.Versions("ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Versions of a missing project: err = %v, want ErrNotFound", err)
	}
	if err := s.Delete("ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete of a missing project: err = %v, want ErrNotFound", err)
	}
	if _, _, err := s.SaveRaw("ghost", []byte("metadata: {slug: ghost}")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SaveRaw must not resurrect a deleted project: err = %v, want ErrNotFound", err)
	}
	if _, _, err := s.Rollback("ghost", "20260101T000000.000000000Z.yaml"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Rollback of a missing project: err = %v, want ErrNotFound", err)
	}
}

// A project whose manifest does not parse must never report ErrNotFound: the
// editor is expected to open it and fix it, not to 404.
func TestBrokenManifestIsNotNotFound(t *testing.T) {
	s := newStore(t)
	if _, err := s.Create("broken", "Broken"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.ProjectDir("broken"), "agent.yaml"), []byte("no: [such: field]"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Load("broken"); err == nil {
		t.Fatal("expected a parse error")
	} else if errors.Is(err, ErrNotFound) {
		t.Fatalf("a broken manifest exists and must not be ErrNotFound: %v", err)
	}
}

func TestReadVersionMissingSnapshotIsNotFound(t *testing.T) {
	s := newStore(t)
	if _, err := s.Create("demo-app", "Demo"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadVersion("demo-app", "20260101T000000.000000000Z.yaml"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if _, err := s.ReadVersion("demo-app", `..\..\evil.yaml`); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("a traversal attempt must be a plain rejection, got %v", err)
	}
}

func countErrors(issues []manifest.Issue) int {
	n := 0
	for _, i := range issues {
		if i.Level == manifest.LevelError {
			n++
		}
	}
	return n
}

// TestPruneExportDirsKeepsNewest pins the retention rule: exports accumulate one
// full module copy per verify/export, and ordering has to be by modification
// time because the names begin with the slug rather than a timestamp.
func TestPruneExportDirsKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	base := time.Now().Add(-time.Hour)
	for i := 0; i < 5; i++ {
		p := filepath.Join(dir, "demo-export-"+string(rune('a'+i)))
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		// Later entries get later mtimes; only the newest two must survive.
		mod := base.Add(time.Duration(i) * time.Minute)
		if err := os.Chtimes(p, mod, mod); err != nil {
			t.Fatal(err)
		}
	}

	if err := PruneExportDirs(dir, 2); err != nil {
		t.Fatalf("PruneExportDirs: %v", err)
	}

	left, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range left {
		names = append(names, e.Name())
	}
	if len(names) != 2 {
		t.Fatalf("kept %d directories, want 2: %v", len(names), names)
	}
	for _, want := range []string{"demo-export-d", "demo-export-e"} {
		found := false
		for _, n := range names {
			if n == want {
				found = true
			}
		}
		if !found {
			t.Errorf("pruning removed the newest %q; kept %v", want, names)
		}
	}
}

func TestPruneExportDirsNoOpCases(t *testing.T) {
	// A missing directory is not an error: the first export creates it.
	if err := PruneExportDirs(filepath.Join(t.TempDir(), "absent"), 3); err != nil {
		t.Fatalf("missing directory: %v", err)
	}
	// keep <= 0 disables pruning entirely.
	dir := t.TempDir()
	for i := 0; i < 3; i++ {
		if err := os.MkdirAll(filepath.Join(dir, "d"+string(rune('0'+i))), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := PruneExportDirs(dir, 0); err != nil {
		t.Fatal(err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 3 {
		t.Fatalf("keep=0 removed directories: %d left", len(left))
	}
}
