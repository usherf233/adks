package api

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/usherf233/adks/internal/play"
)

// TestKeepAliveFramesArriveDuringSilence is the point of the heartbeat: a real
// connection must receive bytes while the turn has nothing to say.
//
// The timeline is compressed (20ms beats, 90ms of silence) so the property is
// tested in milliseconds rather than by waiting out a proxy timeout, but the
// connection is a real one: httptest listens on a socket and the client reads the
// same bytes a browser would.
func TestKeepAliveFramesArriveDuringSilence(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f, ok := w.(http.Flusher)
		if !ok {
			t.Error("the recorder cannot flush; the test proves nothing")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)

		sw := newSSEWriter(w, f)
		stop := make(chan struct{})
		done := make(chan struct{})
		go keepAlive(stop, done, sw, 20*time.Millisecond)
		defer func() {
			close(stop)
			<-done
		}()

		// Longer than three beats, which is the case that used to hang a proxy.
		time.Sleep(90 * time.Millisecond)
		sw.event(play.Event{Type: "done"})
	})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var comments, datas int
	sawDataFirst := false
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, ": keep-alive"):
			comments++
		case strings.HasPrefix(line, "data: "):
			datas++
			if comments == 0 {
				sawDataFirst = true
			}
			if !strings.Contains(line, `"type":"done"`) {
				t.Errorf("data frame is not the event: %q", line)
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("stream ended early: %v", err)
	}
	if datas != 1 {
		t.Fatalf("got %d data frames, want 1", datas)
	}
	if comments < 3 {
		t.Fatalf("got %d keep-alive frames over 90ms of silence, want at least 3", comments)
	}
	if sawDataFirst {
		t.Fatal("the event arrived before any keep-alive frame; the timeline did not test silence")
	}
}

// TestSSEWriterFraming pins the wire format both kinds of frame produce.
func TestSSEWriterFraming(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := newSSEWriter(rec, rec)

	sw.comment("keep-alive")
	sw.event(play.Event{Type: "text", Text: "你好"})

	got := rec.Body.String()
	if !strings.HasPrefix(got, ": keep-alive\n\n") {
		t.Fatalf("comment frame is malformed: %q", got)
	}
	if !strings.Contains(got, "\ndata: ") || !strings.HasSuffix(got, "\n\n") {
		t.Fatalf("data frame is malformed: %q", got)
	}
}

// TestSSEWriterSurvivesConcurrentWrites is the reason the type exists: the turn and
// the ticker write at the same time, and an interleaved frame would be garbage the
// browser silently ignores.
func TestSSEWriterSurvivesConcurrentWrites(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := newSSEWriter(rec, rec)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			sw.event(play.Event{Type: "text", Text: "x"})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			sw.comment("keep-alive")
		}
	}()
	wg.Wait()

	for i, line := range strings.Split(rec.Body.String(), "\n") {
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, ": "):
			// A comment frame owns its whole line.
		case strings.HasPrefix(line, "data: "):
			// The decisive check: a frame that was interleaved with another writer
			// is no longer valid JSON, and the browser would silently drop it.
			var ev play.Event
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
				t.Fatalf("line %d is a corrupt data frame (%v): %q", i, err, line)
			}
		default:
			t.Fatalf("line %d is not part of a frame: %q", i, line)
		}
	}
}

// TestKeepAliveStopsWhenAsked keeps the shutdown contract: the handler waits for
// this goroutine before returning, so it must not linger.
func TestKeepAliveStopsWhenAsked(t *testing.T) {
	rec := httptest.NewRecorder()
	sw := newSSEWriter(rec, rec)
	stop := make(chan struct{})
	done := make(chan struct{})
	go keepAlive(stop, done, sw, time.Hour) // never fires on its own
	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("keepAlive did not return after stop closed")
	}
}
