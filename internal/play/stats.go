package play

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// statLine is one row of stats.jsonl. The message text is deliberately absent:
// the account is about cost, and a file that grows per turn is the last place to
// accumulate conversation content.
type statLine struct {
	TS        time.Time `json:"ts"`
	Slug      string    `json:"slug"`
	SessionID string    `json:"sessionId"`
	UserID    string    `json:"userId,omitempty"`
	TurnStats
}

// statSink serialises appends to stats files. Two sessions of the same project can
// finish at once, and an interleaved append would corrupt a line.
type statSink struct {
	mu sync.Mutex
}

func (s *statSink) write(path string, line statLine) {
	buf, err := json.Marshal(line)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(buf, '\n'))
}

// Percentiles is a distribution summarised three ways. p50 is what a typical turn
// feels like, p95 is what people complain about.
type Percentiles struct {
	P50 int64 `json:"p50"`
	P95 int64 `json:"p95"`
	Avg int64 `json:"avg"`
}

// ToolSummary is one tool's contribution across turns.
type ToolSummary struct {
	Name   string `json:"name"`
	Calls  int    `json:"calls"`
	Errors int    `json:"errors"`
	DurMs  int64  `json:"durMs"`
	P50    int64  `json:"p50"`
}

// StatsSummary is the project's account across the turns that are still on disk.
type StatsSummary struct {
	Turns        int `json:"turns"`
	ErrTurns     int `json:"errTurns"`
	PromptTokens int `json:"promptTokens"`
	OutputTokens int `json:"outputTokens"`
	CachedTokens int `json:"cachedTokens"`
	// CacheHitRate is cached/prompt: the one number that shows whether the prefix
	// stability work is paying for itself.
	CacheHitRate float64       `json:"cacheHitRate"`
	WallMs       Percentiles   `json:"wallMs"`
	FirstByteMs  Percentiles   `json:"firstByteMs"`
	Tools        []ToolSummary `json:"tools"`
}

// maxStatsLines bounds how much history one request parses. The file grows by one
// line per turn forever; reading all of it would make a stats panel slower the
// longer a project lives.
const maxStatsLines = 2000

// Stats aggregates the project's recorded turns, newest limit lines only.
//
// A missing file is an empty account rather than an error: a project that has
// never run is not broken.
func (s *Service) Stats(slug string, limit int) (StatsSummary, error) {
	if limit <= 0 || limit > maxStatsLines {
		limit = maxStatsLines
	}
	path := filepath.Join(s.st.RuntimeDir(slug), "stats.jsonl")
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return StatsSummary{Tools: []ToolSummary{}}, nil
		}
		return StatsSummary{}, err
	}
	defer f.Close()

	lines, err := tailLines(f, limit)
	if err != nil {
		return StatsSummary{}, err
	}

	var (
		sum     StatsSummary
		walls   []int64
		firsts  []int64
		toolDur = map[string][]int64{}
		toolAgg = map[string]*ToolSummary{}
	)
	for _, raw := range lines {
		var row statLine
		if err := json.Unmarshal(raw, &row); err != nil {
			// One truncated line (a crash mid-write) must not void the account.
			continue
		}
		sum.Turns++
		if row.ErrCode != "" {
			sum.ErrTurns++
		}
		sum.PromptTokens += row.PromptTokens
		sum.OutputTokens += row.OutputTokens
		sum.CachedTokens += row.CachedTokens
		walls = append(walls, row.WallMs)
		if row.FirstByteMs > 0 {
			firsts = append(firsts, row.FirstByteMs)
		}
		for _, t := range row.Tools {
			agg, ok := toolAgg[t.Name]
			if !ok {
				agg = &ToolSummary{Name: t.Name}
				toolAgg[t.Name] = agg
			}
			agg.Calls += t.Calls
			agg.Errors += t.Errors
			agg.DurMs += t.DurMs
			toolDur[t.Name] = append(toolDur[t.Name], t.DurMs)
		}
	}

	sum.WallMs = summarize(walls)
	sum.FirstByteMs = summarize(firsts)
	if sum.PromptTokens > 0 {
		sum.CacheHitRate = float64(sum.CachedTokens) / float64(sum.PromptTokens)
	}
	for name, agg := range toolAgg {
		agg.P50 = percentile(toolDur[name], 50)
		sum.Tools = append(sum.Tools, *agg)
	}
	// Most expensive first: the tool worth optimising is at the top.
	sort.Slice(sum.Tools, func(i, j int) bool { return sum.Tools[i].DurMs > sum.Tools[j].DurMs })
	if sum.Tools == nil {
		sum.Tools = []ToolSummary{}
	}
	return sum, nil
}

// tailLines returns at most limit trailing lines, without loading a file that
// only ever grows into memory in full.
func tailLines(r io.Reader, limit int) ([][]byte, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	ring := make([][]byte, 0, limit)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		if len(ring) == limit {
			copy(ring, ring[1:])
			ring[limit-1] = append([]byte(nil), line...)
			continue
		}
		ring = append(ring, append([]byte(nil), line...))
	}
	return ring, sc.Err()
}

func summarize(xs []int64) Percentiles {
	if len(xs) == 0 {
		return Percentiles{}
	}
	var total int64
	for _, v := range xs {
		total += v
	}
	return Percentiles{
		P50: percentile(xs, 50),
		P95: percentile(xs, 95),
		Avg: total / int64(len(xs)),
	}
}

// percentile sorts a copy, so callers can reuse their slices.
func percentile(xs []int64, p int) int64 {
	if len(xs) == 0 {
		return 0
	}
	sorted := append([]int64(nil), xs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := (len(sorted)*p - 1) / 100
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
