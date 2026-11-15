package kit

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/usherf233/adks/manifest"
)

// TestDoWithRetryRetriesGetOnServerError is the regression for a retry that never
// fired: the old condition needed req.Body != nil, and a GET has no body, so the
// most common kind of HTTP tool was exempt from the retry it documented.
func TestDoWithRetryRetriesGetOnServerError(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&hits, 1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := doWithRetry(srv.Client(), req, manifest.HTTPSpec{Name: "lookup"})
	if err != nil {
		t.Fatalf("doWithRetry: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 after a retry", resp.StatusCode)
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Fatalf("server saw %d attempts, want 2", got)
	}
}

// TestDoWithRetryReplaysBody is the other half: a POST must still be retried with
// its body intact, or the retry would send an empty request.
func TestDoWithRetryReplaysBody(t *testing.T) {
	var (
		hits  int32
		bodys []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodys = append(bodys, string(raw))
		if atomic.AddInt32(&hits, 1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(`{"id":1}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := doWithRetry(srv.Client(), req, manifest.HTTPSpec{Name: "create"})
	if err != nil {
		t.Fatalf("doWithRetry: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if len(bodys) != 2 {
		t.Fatalf("server saw %d attempts, want 2", len(bodys))
	}
	if bodys[1] != `{"id":1}` {
		t.Fatalf("the retried body was not replayed: %q", bodys[1])
	}
}

// TestDoWithRetrySkipsUnreplayableBody keeps the retry honest: a stream that
// cannot be rewound must not be sent twice with different content.
func TestDoWithRetrySkipsUnreplayableBody(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	// A reader the standard library cannot snapshot, so GetBody stays nil.
	body := io.LimitReader(strings.NewReader(`{"id":1}`), 64)
	req, err := http.NewRequest(http.MethodPost, srv.URL, body)
	if err != nil {
		t.Fatal(err)
	}
	if req.GetBody != nil {
		t.Fatal("precondition: this body should not be replayable")
	}
	resp, err := doWithRetry(srv.Client(), req, manifest.HTTPSpec{Name: "stream"})
	if err != nil {
		t.Fatalf("doWithRetry: %v", err)
	}
	defer resp.Body.Close()

	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("server saw %d attempts, want 1 for an unreplayable body", got)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want the original 503", resp.StatusCode)
	}
}

// TestDoWithRetryLeaves4xxAlone: retrying a client error only burns time.
func TestDoWithRetryLeaves4xxAlone(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := doWithRetry(srv.Client(), req, manifest.HTTPSpec{Name: "secure"})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("a 401 was retried %d times", got-1)
	}
}
