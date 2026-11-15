package verify

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"github.com/usherf233/adks/internal/codegen"
	"github.com/usherf233/adks/manifest"
)

// TestSmokeStartsExportedBinary pins the launcher argument format. ADK v2's
// full launcher only accepts "console" and "web" as top-level keywords; "api"
// is a web sublauncher and must come after the web flags. A wrong argv makes
// the binary exit before it ever serves /health.
func TestSmokeStartsExportedBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and starts a generated binary")
	}
	// The generated kit builds the model client at start-up but never calls it
	// during a smoke run, so a placeholder credential is enough.
	t.Setenv("GOOGLE_API_KEY", "dummy")

	seed := os.DirFS("../..")
	b, err := codegen.Render(manifest.New("demo", "Demo"), seed)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := codegen.WriteDir(b, dir); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	check := smoke(ctx, dir)
	if !check.OK {
		t.Fatalf("smoke run failed: %s", check.Detail)
	}
}

// TestIsBindConflict pins the one condition worth retrying. A crash must not be
// retried, or the check would turn a broken binary into a slow pass.
func TestIsBindConflict(t *testing.T) {
	conflicts := []string{
		"listen tcp 127.0.0.1:8080: bind: address already in use",
		"bind: Only one usage of each socket address is normally permitted.",
		"syscall.EADDRINUSE",
	}
	for _, d := range conflicts {
		if !isBindConflict(d) {
			t.Errorf("not recognised as a conflict: %q", d)
		}
	}
	crashes := []string{
		"panic: runtime error: invalid memory address",
		"build agent: model gemini: environment variable GOOGLE_API_KEY is empty",
		"",
	}
	for _, d := range crashes {
		if isBindConflict(d) {
			t.Errorf("a crash would be retried as a port conflict: %q", d)
		}
	}
}

// TestSmokeRetriesWhenThePortIsTaken exercises the retry with a real binary and a
// real conflict: a listener is held on the first port handed out, then released so
// the second attempt can succeed. Without the retry this test fails exactly the way
// the Windows CI job did.
func TestSmokeRetriesWhenThePortIsTaken(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and starts a generated binary")
	}
	t.Setenv("GOOGLE_API_KEY", "dummy")

	seed := os.DirFS("../..")
	b, err := codegen.Render(manifest.New("demo", "Demo"), seed)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := codegen.WriteDir(b, dir); err != nil {
		t.Fatal(err)
	}

	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	taken := held.Addr().(*net.TCPAddr).Port

	orig := nextPort
	attempts := 0
	nextPort = func() (int, error) {
		attempts++
		if attempts == 1 {
			return taken, nil
		}
		// Release only when the retry asks for a port: releasing on a timer raced
		// the child's bind, and the first attempt then succeeded.
		_ = held.Close()
		return orig()
	}
	defer func() { nextPort = orig }()
	defer held.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	check := smoke(ctx, dir)
	if !check.OK {
		t.Fatalf("smoke gave up instead of retrying: %s", check.Detail)
	}
	if attempts < 2 {
		t.Fatalf("smoke made %d attempts, want at least 2", attempts)
	}
}
