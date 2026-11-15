package play

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func readLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read stats: %v", err)
	}
	text := strings.TrimSuffix(string(raw), "\n")
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// TestStatSinkKeepsTheNewestLines is the rule this file follows: a ledger keeps
// what happened recently, it does not halve its history when a byte counter trips.
func TestStatSinkKeepsTheNewestLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stats.jsonl")
	// A tiny budget so the trim fires within a handful of writes.
	sink := &statSink{maxBytes: 200, keepLines: 3}

	for i := 0; i < 12; i++ {
		sink.write(path, statLine{SessionID: fmt.Sprintf("s%02d", i), TurnStats: TurnStats{WallMs: int64(i)}})
	}

	lines := readLines(t, path)
	if len(lines) == 0 || len(lines) > 3 {
		t.Fatalf("got %d lines after trimming to 3:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	var last statLine
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &last); err != nil {
		t.Fatalf("last line is not JSON: %v", err)
	}
	if last.SessionID != "s11" {
		t.Fatalf("the newest turn was dropped: last session = %q", last.SessionID)
	}
}

// TestStatSinkTrimKeepsFileReadable: every surviving line must still parse, or the
// account panel would silently shrink after a trim.
func TestStatSinkTrimKeepsFileReadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stats.jsonl")
	sink := &statSink{maxBytes: 300, keepLines: 2}
	for i := 0; i < 8; i++ {
		sink.write(path, statLine{SessionID: "s", TurnStats: TurnStats{PromptTokens: 10, WallMs: 100}})
	}
	for i, line := range readLines(t, path) {
		var row statLine
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("line %d unreadable after trim: %v\n%s", i, err, line)
		}
		if row.PromptTokens != 10 {
			t.Fatalf("line %d lost its numbers: %+v", i, row)
		}
	}
}

// TestStatSinkConcurrentWritesStayWhole: the mutex must cover the trim too, or two
// goroutines could rewrite the file from different snapshots.
func TestStatSinkConcurrentWritesStayWhole(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stats.jsonl")
	sink := &statSink{maxBytes: 400, keepLines: 4}

	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				sink.write(path, statLine{SessionID: fmt.Sprintf("w%d-%d", id, i)})
			}
		}(w)
	}
	wg.Wait()

	for i, line := range readLines(t, path) {
		var row statLine
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatalf("line %d was corrupted by concurrent writers: %v\n%s", i, err, line)
		}
	}
}

// TestStatsReadsWhatSurvivedTrim closes the loop: the reader must work on the file
// the writer leaves behind.
func TestStatsReadsWhatSurvivedTrim(t *testing.T) {
	svc := newService(t)
	defer svc.Close()
	path := filepath.Join(svc.st.RuntimeDir("demo"), "stats.jsonl")
	svc.stats = &statSink{maxBytes: 300, keepLines: 3}
	for i := 0; i < 10; i++ {
		svc.stats.write(path, statLine{TurnStats: TurnStats{PromptTokens: 5, WallMs: 50}})
	}

	sum, err := svc.Stats("demo", 0)
	if err != nil {
		t.Fatalf("Stats after trim: %v", err)
	}
	if sum.Turns == 0 || sum.Turns > 3 {
		t.Fatalf("turns = %d after a trim to 3", sum.Turns)
	}
	if sum.PromptTokens != sum.Turns*5 {
		t.Fatalf("tokens = %d for %d turns, want %d", sum.PromptTokens, sum.Turns, sum.Turns*5)
	}
}
