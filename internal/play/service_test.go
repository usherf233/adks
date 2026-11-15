package play

import (
	"context"
	"testing"
	"time"

	"github.com/usherf233/adks/internal/store"
	"github.com/usherf233/adks/manifest"
)

func newService(t *testing.T) *Service {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return New(st)
}

// TestPrepareMissingProjectIsAnError is the contract the HTTP layer depends on:
// setup failures are returned, not streamed, so a 404 stays a 404.
func TestPrepareMissingProjectIsAnError(t *testing.T) {
	s := newService(t)
	defer s.Close()
	if _, err := s.Prepare(context.Background(), "ghost"); err == nil {
		t.Fatal("preparing a project that does not exist must fail")
	}
}

// TestConfirmWithoutPendingIsFalse keeps the "already timed out" case cheap: a
// late click is not an error, it is a no.
func TestConfirmWithoutPendingIsFalse(t *testing.T) {
	s := newService(t)
	defer s.Close()
	if s.Confirm("demo", "s1", "no-such-call", true) {
		t.Fatal("a decision nobody waits for was accepted")
	}
}

// TestInvalidateUnknownProjectIsSafe: the HTTP layer invalidates on save and
// delete without checking, so a miss must be a no-op.
func TestInvalidateUnknownProjectIsSafe(t *testing.T) {
	s := newService(t)
	defer s.Close()
	s.Invalidate("never-compiled")
}

// TestCloseTwiceIsSafe: shutdown runs once, but a test or a second signal must not
// panic on a released service.
func TestCloseTwiceIsSafe(t *testing.T) {
	s := newService(t)
	s.Close()
	s.Close()
}

// TestWallClockFollowsTheManifest pins the budget the HTTP layer hands to the run:
// the manifest's value when set, the default when it is not.
func TestWallClockFollowsTheManifest(t *testing.T) {
	p := manifest.New("demo", "Demo")
	p.Spec.Policy.Limits.MaxWallClockSec = 45
	if got := (&Session{project: p}).WallClock(); got != 45*time.Second {
		t.Fatalf("wall clock = %v, want 45s", got)
	}
	p.Spec.Policy.Limits.MaxWallClockSec = 0
	if got := (&Session{project: p}).WallClock(); got != defaultWallClock {
		t.Fatalf("wall clock = %v, want the %v default", got, defaultWallClock)
	}
	// A session without a project (test placeholder) still gets a usable budget.
	if got := (&Session{}).WallClock(); got != defaultWallClock {
		t.Fatalf("wall clock = %v, want the default", got)
	}
}
