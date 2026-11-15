package store

import (
	"path/filepath"
	"testing"

	"github.com/usherf233/adks/manifest"
)

// TestRebaseSQLiteDSN covers the four shapes a DSN actually takes. The file: form
// is the one that bites: the driver pragmas live in the query string and must
// survive the rewrite, or busy_timeout and WAL silently disappear.
func TestRebaseSQLiteDSN(t *testing.T) {
	dir := filepath.Join("root", "proj", ".runtime")
	// A genuinely absolute path: filepath.Join("abs", ...) is still relative on
	// Windows, and that is exactly the shape this function must not touch.
	abs, err := filepath.Abs(filepath.Join("abs", "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		dsn  string
		want string
	}{
		{"empty becomes the default file", "", filepath.Join(dir, "sessions.db")},
		{"relative is rooted, basename only", "./data/sessions.db", filepath.Join(dir, "sessions.db")},
		{"bare filename", "sessions.db", filepath.Join(dir, "sessions.db")},
		{"absolute is left alone", abs, abs},
		{
			name: "file URI keeps its pragmas",
			dsn:  "file:./.adks/sessions.db?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)",
			want: "file:" + filepath.Join(dir, "sessions.db") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)",
		},
		{
			name: "absolute file URI is left alone",
			dsn:  "file:" + abs + "?_pragma=journal_mode(WAL)",
			want: "file:" + abs + "?_pragma=journal_mode(WAL)",
		},
		{
			name: "file URI without a query",
			dsn:  "file:data/sessions.db",
			want: "file:" + filepath.Join(dir, "sessions.db"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RebaseSQLiteDSN(tc.dsn, dir); got != tc.want {
				t.Fatalf("RebaseSQLiteDSN(%q) = %q, want %q", tc.dsn, got, tc.want)
			}
		})
	}
}

// TestPrepareRuntimeCopiesInsteadOfMutating is the reason the function clones
// through Encode/Decode: the caller's manifest is what gets exported, so a trial
// run must not rewrite the paths the user typed.
func TestPrepareRuntimeCopiesInsteadOfMutating(t *testing.T) {
	s := newStore(t)
	p := manifest.New("demo", "Demo")
	p.Spec.Runtime.Session.Backend = "sqlite"
	p.Spec.Runtime.Session.DSN = "./.adks/sessions.db"
	p.Spec.Runtime.Artifact.Backend = "local"
	p.Spec.Runtime.Artifact.Dir = "./.adks/artifacts"

	original := p.Spec.Runtime
	got, err := s.PrepareRuntime(p, "demo")
	if err != nil {
		t.Fatalf("PrepareRuntime: %v", err)
	}

	if p.Spec.Runtime.Session.DSN != original.Session.DSN || p.Spec.Runtime.Artifact.Dir != original.Artifact.Dir {
		t.Fatal("PrepareRuntime mutated the caller's manifest")
	}
	runtimeDir := s.RuntimeDir("demo")
	if want := filepath.Join(runtimeDir, "sessions.db"); got.Spec.Runtime.Session.DSN != want {
		t.Fatalf("dsn = %q, want %q", got.Spec.Runtime.Session.DSN, want)
	}
	if want := filepath.Join(runtimeDir, "artifacts"); got.Spec.Runtime.Artifact.Dir != want {
		t.Fatalf("artifact dir = %q, want %q", got.Spec.Runtime.Artifact.Dir, want)
	}
}

// TestPrepareRuntimeLeavesForeignBackendsAlone keeps the rebase scoped: a postgres
// DSN is not a path, and an absolute artifact directory is the user's decision.
func TestPrepareRuntimeLeavesForeignBackendsAlone(t *testing.T) {
	s := newStore(t)
	p := manifest.New("demo", "Demo")
	p.Spec.Runtime.Session.Backend = "postgres"
	p.Spec.Runtime.Session.DSN = "postgres://user:pw@db:5432/app"
	p.Spec.Runtime.Artifact.Backend = "local"
	p.Spec.Runtime.Artifact.Dir = "/var/lib/artifacts"

	got, err := s.PrepareRuntime(p, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.Runtime.Session.DSN != p.Spec.Runtime.Session.DSN {
		t.Fatalf("a postgres DSN was rewritten to %q", got.Spec.Runtime.Session.DSN)
	}
	if got.Spec.Runtime.Artifact.Dir != "/var/lib/artifacts" {
		t.Fatalf("an absolute artifact dir was rewritten to %q", got.Spec.Runtime.Artifact.Dir)
	}
}
