package verify

import (
	"context"
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
