package kit

import (
	"path/filepath"
	"testing"

	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/session/sessiontestsuite"

	"github.com/adks-dev/adks/manifest"
)

// TestSQLiteSessionConformance runs ADK's own conformance suite against the sqlite
// backend.
//
// The suite is the only honest way to answer "does this backend behave like a
// session service": it encodes the obligations (event IDs, temp-key stripping,
// compaction round-trip) that a hand-written smoke test would miss.
func TestSQLiteSessionConformance(t *testing.T) {
	sessiontestsuite.RunServiceTests(t, sessiontestsuite.SuiteOptions{
		SupportsUserProvidedSessionID: true,
	}, func(t *testing.T) session.Service {
		dsn := filepath.Join(t.TempDir(), "sessions.db")
		svc, err := NewSessionService(t.Context(), manifest.SessionSpec{
			Backend:     "sqlite",
			DSN:         dsn,
			AutoMigrate: true,
		})
		if err != nil {
			t.Fatalf("NewSessionService: %v", err)
		}
		// Windows cannot delete the temp directory while the pool is open, and a
		// cleanup error would be reported as a test failure.
		t.Cleanup(func() { _ = CloseService(svc) })
		return svc
	})
}

func TestSQLiteSessionSurvivesReopen(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "sessions.db")
	spec := manifest.SessionSpec{Backend: "sqlite", DSN: dsn, AutoMigrate: true}

	first, err := NewSessionService(t.Context(), spec)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	created, err := first.Create(t.Context(), &session.CreateRequest{AppName: "app", UserID: "u"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// A second service over the same file stands in for a process restart: if the
	// migration were wrong, this is where the session would vanish.
	second, err := NewSessionService(t.Context(), spec)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = CloseService(first); _ = CloseService(second) })
	got, err := second.Get(t.Context(), &session.GetRequest{AppName: "app", UserID: "u", SessionID: created.Session.ID()})
	if err != nil {
		t.Fatalf("get after reopen: %v", err)
	}
	if got.Session.ID() != created.Session.ID() {
		t.Fatalf("session id changed across reopen: %q", got.Session.ID())
	}
}

// TestEnsureSQLiteDirCreatesParent covers the probe path: a fresh project has no
// runtime directory yet, and sqlite reports the missing folder as "out of memory".
func TestEnsureSQLiteDirCreatesParent(t *testing.T) {
	root := t.TempDir()
	dsn := "file:" + filepath.Join(root, "deep", "nested", "sessions.db") + "?_pragma=journal_mode(WAL)"

	if err := ensureSQLiteDir(dsn); err != nil {
		t.Fatalf("ensure dir: %v", err)
	}
	svc, err := NewSessionService(t.Context(), manifest.SessionSpec{Backend: "sqlite", DSN: dsn})
	if err != nil {
		t.Fatalf("open after mkdir: %v", err)
	}
	// Windows keeps the file locked until the pool is released, and the temp dir
	// cleanup runs before the test function returns.
	if err := CloseService(svc); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestEnsureSQLiteDirIgnoresMemoryDSN(t *testing.T) {
	for _, dsn := range []string{"", ":memory:", "sessions.db"} {
		if err := ensureSQLiteDir(dsn); err != nil {
			t.Fatalf("ensureSQLiteDir(%q): %v", dsn, err)
		}
	}
}

// TestProbeSessionIntoMissingDirectory is the regression for the builder's
// "测试会话存储" button: the DSN points into a directory the project has not created.
func TestProbeSessionIntoMissingDirectory(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "runtime", "probe", "sessions.db")
	detail, err := ProbeSession(t.Context(), manifest.SessionSpec{Backend: "sqlite", DSN: dsn})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if detail == "" {
		t.Fatal("probe returned an empty detail")
	}
}
