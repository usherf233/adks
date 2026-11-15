package kit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAuditSinkAppendsAndCloses covers the lifecycle the layer now relies on: one
// handle, one line per event, and a Close that does not lose the tail.
func TestAuditSinkAppendsAndCloses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	sink, err := NewAuditSink(path, false)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := sink.Write(AuditEvent{Tool: "lookup", OK: true}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3:\n%s", len(lines), raw)
	}
	for _, l := range lines {
		if !strings.Contains(l, `"tool":"lookup"`) {
			t.Fatalf("line is not the expected event: %s", l)
		}
	}
}

// TestAuditSinkWritesAfterCloseAreDropped pins that a late tool callback during
// shutdown cannot resurrect a closed handle.
func TestAuditSinkWritesAfterCloseAreDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	sink, err := NewAuditSink(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Write(AuditEvent{Tool: "before"}); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sink.Write(AuditEvent{Tool: "late"}); err != nil {
		t.Fatalf("write after close should be a no-op, got %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], `"before"`) {
		t.Fatalf("a closed sink kept writing: %s", raw)
	}
}

// TestAuditSinkRotatesBySize is the disk bound: an audit log must not grow without
// limit just because a loop kept calling tools.
func TestAuditSinkRotatesBySize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	if err := os.WriteFile(path, make([]byte, maxAuditBytes), 0o644); err != nil {
		t.Fatal(err)
	}
	sink, err := NewAuditSink(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Write(AuditEvent{Tool: "after-rotate", OK: true}); err != nil {
		t.Fatal(err)
	}
	if err := sink.Close(); err != nil {
		t.Fatal(err)
	}

	rotated, err := os.Stat(path + ".1")
	if err != nil {
		t.Fatalf("the oversized log was not rotated: %v", err)
	}
	if rotated.Size() != maxAuditBytes {
		t.Fatalf("rotated file is %d bytes, want %d", rotated.Size(), maxAuditBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "after-rotate") {
		t.Fatalf("the new file does not carry the post-rotation event: %s", raw)
	}
}

// TestAuditSinkNilIsSafe keeps the "no audit path configured" path allocation-free
// and panic-free, because it is the default for a trial run.
func TestAuditSinkNilIsSafe(t *testing.T) {
	sink, err := NewAuditSink("", false)
	if err != nil || sink != nil {
		t.Fatalf("empty path must yield a nil sink, got %v / %v", sink, err)
	}
	if err := sink.Write(AuditEvent{Tool: "x"}); err != nil {
		t.Fatalf("nil sink write: %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("nil sink close: %v", err)
	}
}
