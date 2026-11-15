package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/usherf233/adks/internal/store"
)

func guardTestServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return New(st, fstest.MapFS{}, "test", "")
}

func TestGuardRejectsInvalidSlug(t *testing.T) {
	s := guardTestServer(t)
	for _, raw := range []string{
		"/api/projects/..%5Coutside", // Windows separator survives ServeMux
		"/api/projects/UPPER",
		"/api/projects/a%20b",
	} {
		req := httptest.NewRequest(http.MethodGet, raw, nil)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid project slug") {
			t.Errorf("GET %s: got %d %q, want 400 invalid slug", raw, w.Code, w.Body.String())
		}
	}
}

func TestGuardRejectsCrossOriginWrites(t *testing.T) {
	s := guardTestServer(t)
	cases := []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"foreign origin", map[string]string{"Origin": "https://evil.example", "Content-Type": "application/json"}, http.StatusForbidden},
		{"cross-site fetch", map[string]string{"Sec-Fetch-Site": "cross-site", "Content-Type": "application/json"}, http.StatusForbidden},
		{"opaque origin", map[string]string{"Origin": "null", "Content-Type": "application/json"}, http.StatusForbidden},
		{"simple text/plain", map[string]string{"Content-Type": "text/plain"}, http.StatusUnsupportedMediaType},
		{"missing content type", map[string]string{}, http.StatusUnsupportedMediaType},
		{"same origin", map[string]string{"Origin": "http://example.com", "Content-Type": "application/json"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/projects/demo/verify", strings.NewReader(`{}`))
			req.Host = "example.com"
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, req)
			if tc.want != 0 && w.Code != tc.want {
				t.Fatalf("got %d %q, want %d", w.Code, w.Body.String(), tc.want)
			}
			if tc.want == 0 && (w.Code == http.StatusForbidden || w.Code == http.StatusUnsupportedMediaType) {
				t.Fatalf("same-origin write was blocked: %d %q", w.Code, w.Body.String())
			}
		})
	}
}

func TestDeleteDoesNotRequireContentType(t *testing.T) {
	s := guardTestServer(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/projects/demo", nil)
	req.Host = "example.com"
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code == http.StatusForbidden || w.Code == http.StatusUnsupportedMediaType {
		t.Fatalf("same-origin DELETE was blocked: %d %q", w.Code, w.Body.String())
	}
}

func TestPutInvalidYAMLDoesNotPanic(t *testing.T) {
	s := guardTestServer(t)
	if _, err := s.store.Create("demo", "Demo"); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"manifestYaml":""}`, `{"manifestYaml":"no: [such: field]"}`} {
		req := httptest.NewRequest(http.MethodPut, "/api/projects/demo", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("PUT %s: got %d %q", body, w.Code, w.Body.String())
		}
		if !strings.Contains(w.Body.String(), `"saved":false`) {
			t.Fatalf("PUT %s: expected saved:false, got %q", body, w.Body.String())
		}
	}
}

// TestPlayHubDeliverChecksOwner moved to internal/play with the hub itself: the
// ownership rule belongs with the state it protects, not with the HTTP surface.

func TestExportDirectoriesAreUnique(t *testing.T) {
	s := guardTestServer(t)
	base := s.store.ExportsDir()
	first, err := newExportDir(base, "demo", "verify")
	if err != nil {
		t.Fatal(err)
	}
	second, err := newExportDir(base, "demo", "verify")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("concurrent exports would share %q", first)
	}
}

// POST has no {slug} path segment, so the guard cannot check it. The handler
// must apply the same rule instead of leaving a second, weaker admission path.
func TestCreateUsesSameSlugRule(t *testing.T) {
	s := guardTestServer(t)
	for _, slug := range []string{"UPPER", "../escape", "a b", `a\b`} {
		body := `{"slug":"` + slug + `"}`
		req := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "invalid project slug") {
			t.Errorf("POST slug %q: got %d %q, want 400 invalid slug", slug, w.Code, w.Body.String())
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/projects", strings.NewReader(`{"slug":"valid-demo"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("valid slug was rejected: %d %q", w.Code, w.Body.String())
	}
}

// TestPlayHubCloseDropsPending lives in internal/play: the shutdown contract of
// the hub does not need an HTTP server to be exercised.

// One rule everywhere: a project that does not exist is a 404, not a 200 with
// an error field and not a 400 that looks like a bad request.
func TestMissingProjectIs404OnEveryEndpoint(t *testing.T) {
	s := guardTestServer(t)
	for _, tc := range []struct {
		method, path, body string
	}{
		{"GET", "/api/projects/ghost", ""},
		{"PUT", "/api/projects/ghost", `{"manifestYaml":"metadata: {slug: ghost}"}`},
		{"DELETE", "/api/projects/ghost", ""},
		{"GET", "/api/projects/ghost/versions", ""},
		{"POST", "/api/projects/ghost/rollback", `{"version":"x.yaml"}`},
		{"GET", "/api/projects/ghost/graph", ""},
		{"POST", "/api/projects/ghost/probe/model", `{}`},
		{"POST", "/api/projects/ghost/verify", `{}`},
		{"GET", "/api/projects/ghost/files", ""},
	} {
		var body *strings.Reader
		if tc.body == "" {
			body = strings.NewReader("")
		} else {
			body = strings.NewReader(tc.body)
		}
		req := httptest.NewRequest(tc.method, tc.path, body)
		req.Host = "example.com"
		if tc.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("%s %s: got %d %q, want 404", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}

// The editor must open a manifest that does not parse, so GET is the endpoint
// that answers 200 with a loadError instead of failing.
func TestBrokenManifestStillOpens(t *testing.T) {
	s := guardTestServer(t)
	if _, err := s.store.Create("broken", "Broken"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.store.ProjectDir("broken"), "agent.yaml"), []byte("no: [such: field]"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/projects/broken", nil)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("a broken manifest must still open: got %d %q", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"loadError"`) {
		t.Fatalf("expected loadError in the response, got %q", w.Body.String())
	}

	// ...but every endpoint that needs a compiled project says 400.
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/projects/broken/graph", ""},
		{"POST", "/api/projects/broken/probe/model", `{}`},
		{"GET", "/api/projects/broken/files", ""},
	} {
		var body *strings.Reader
		if tc.body == "" {
			body = strings.NewReader("")
		} else {
			body = strings.NewReader(tc.body)
		}
		req := httptest.NewRequest(tc.method, tc.path, body)
		req.Host = "example.com"
		if tc.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s %s on a broken manifest: got %d %q, want 400", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
}
