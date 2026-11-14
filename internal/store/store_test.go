package store

import (
	"strings"
	"testing"

	"github.com/adks-dev/adks/manifest"
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

func countErrors(issues []manifest.Issue) int {
	n := 0
	for _, i := range issues {
		if i.Level == manifest.LevelError {
			n++
		}
	}
	return n
}
