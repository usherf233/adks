package api

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/usherf233/adks/internal/play"
	"github.com/usherf233/adks/internal/store"
	"github.com/usherf233/adks/manifest"
)

// These tests drive the builder the way the browser does: create, edit, preview,
// export. They exist because every handler used to sit at 0% coverage while the
// guard and 404 paths were well covered — which is exactly how a field can be
// declared, validated and exported without ever reaching the artifact.

func flowServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// The real embedded source tree: an export copies kit/ and manifest/ out of it
	// byte for byte, so a fake seed would test a different product.
	return New(st, os.DirFS("../.."), "test", "")
}

func call(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, req)
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return v
}

// TestProjectLifecycleOverHTTP walks the editor's actual path: create, list, read,
// save, preview. Export lives in its own test because it shells out to the Go
// toolchain.
func TestProjectLifecycleOverHTTP(t *testing.T) {
	s := flowServer(t)
	defer s.Close()

	// Liveness is the one endpoint everything else assumes.
	if w := call(t, s, http.MethodGet, "/api/health", ""); w.Code != http.StatusOK {
		t.Fatalf("health: %d %s", w.Code, w.Body.String())
	}

	if w := call(t, s, http.MethodPost, "/api/projects", `{"slug":"demo","name":"Demo"}`); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}

	list := decode[struct {
		Projects []store.ProjectMeta `json:"projects"`
	}](t, call(t, s, http.MethodGet, "/api/projects", ""))
	if len(list.Projects) != 1 || list.Projects[0].Slug != "demo" {
		t.Fatalf("list did not report the new project: %+v", list.Projects)
	}

	// The editor opens a project with one round trip and expects an estimate,
	// even before the user touches anything.
	view := decode[struct {
		Manifest     *manifest.Project `json:"manifest"`
		ManifestYAML string            `json:"manifestYaml"`
		Estimate     estimate          `json:"estimate"`
		Issues       []manifest.Issue  `json:"issues"`
	}](t, call(t, s, http.MethodGet, "/api/projects/demo", ""))
	if view.Manifest == nil || view.ManifestYAML == "" {
		t.Fatalf("get did not return the manifest: %+v", view)
	}
	if view.Estimate.TotalTokens <= 0 || view.Estimate.Chars <= 0 {
		t.Fatalf("estimate is empty: %+v", view.Estimate)
	}
	if manifest.HasErrors(view.Issues) {
		t.Fatalf("a freshly created project must be valid: %+v", view.Issues)
	}

	// Edit through the YAML tab: the text is the single source of truth.
	edited := strings.Replace(view.ManifestYAML, "You are a helpful assistant.", "You are a refunds assistant.", 1)
	if edited == view.ManifestYAML {
		t.Fatal("precondition: the default instruction was not found in the YAML")
	}
	put := call(t, s, http.MethodPut, "/api/projects/demo", mustJSON(t, map[string]string{"manifestYaml": edited}))
	if put.Code != http.StatusOK {
		t.Fatalf("put: %d %s", put.Code, put.Body.String())
	}
	if got := decode[struct {
		Slug string `json:"slug"`
	}](t, put).Slug; got != "demo" {
		t.Fatalf("put returned slug %q", got)
	}

	// An edit must survive a re-read, and must have produced a version snapshot.
	again := decode[struct {
		ManifestYAML string   `json:"manifestYaml"`
		Versions     []string `json:"versions"`
	}](t, call(t, s, http.MethodGet, "/api/projects/demo", ""))
	if !strings.Contains(again.ManifestYAML, "refunds assistant") {
		t.Fatalf("the saved instruction did not round-trip:\n%s", again.ManifestYAML)
	}
	if len(again.Versions) == 0 {
		t.Fatal("saving did not snapshot the previous version")
	}

	// The export preview is what the user inspects before downloading.
	files := decode[struct {
		Files []filesView `json:"files"`
	}](t, call(t, s, http.MethodGet, "/api/projects/demo/files", ""))
	var sawMain, sawDocker bool
	for _, f := range files.Files {
		switch f.Path {
		case "main.go":
			sawMain = true
			if !strings.Contains(f.Preview, "-host") {
				t.Fatalf("main.go preview does not mention -host:\n%s", f.Preview)
			}
		case "Dockerfile":
			sawDocker = true
			if !strings.Contains(f.Preview, "EXPOSE") {
				t.Fatalf("Dockerfile preview has no EXPOSE:\n%s", f.Preview)
			}
		}
	}
	if !sawMain || !sawDocker {
		t.Fatalf("preview is missing generated files: %+v", files.Files)
	}

	if w := call(t, s, http.MethodDelete, "/api/projects/demo", ""); w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if w := call(t, s, http.MethodGet, "/api/projects/demo", ""); w.Code != http.StatusNotFound {
		t.Fatalf("a deleted project still answers: %d", w.Code)
	}
}

// TestExportZipCarriesLauncherFlags closes the loop the manual e2e run opened: the
// downloaded archive must contain a main.go that binds where the manifest says and
// a Dockerfile that exposes the same port.
func TestExportZipCarriesLauncherFlags(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go mod tidy and packages the export")
	}
	s := flowServer(t)
	defer s.Close()

	p := manifest.New("flags", "Flags")
	p.Spec.Runtime.Server.Host = "0.0.0.0"
	p.Spec.Runtime.Server.Port = 9191
	raw, err := manifest.Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	if w := call(t, s, http.MethodPost, "/api/projects", `{"slug":"flags","name":"Flags"}`); w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if w := call(t, s, http.MethodPut, "/api/projects/flags", mustJSON(t, map[string]string{"manifestYaml": string(raw)})); w.Code != http.StatusOK {
		t.Fatalf("put: %d %s", w.Code, w.Body.String())
	}

	w := call(t, s, http.MethodPost, "/api/projects/flags/export", `{"zip":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("export: %d %s", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("export content type = %q", ct)
	}

	files := readZip(t, w.Body.Bytes())
	main, ok := files["main.go"]
	if !ok {
		t.Fatalf("archive has no main.go: %v", keys(files))
	}
	for _, want := range []string{`host = "0.0.0.0"`, `port = "9191"`, `"-host", bind`} {
		if !strings.Contains(main, want) {
			t.Errorf("main.go does not contain %q", want)
		}
	}
	if docker, ok := files["Dockerfile"]; !ok {
		t.Error("archive has no Dockerfile")
	} else if !strings.Contains(docker, "EXPOSE 9191") {
		t.Errorf("Dockerfile does not expose the manifest port:\n%s", docker)
	}
	if _, ok := files["agent.yaml"]; !ok {
		t.Error("archive has no agent.yaml")
	}
	if _, ok := files["kit/build.go"]; !ok {
		t.Error("archive did not copy kit/ verbatim")
	}
}

// TestGraphEndpointProjectsTopology covers the projection the UI draws: depths
// from START, in/out counts, and an empty view for a project that has no graph.
func TestGraphEndpointProjectsTopology(t *testing.T) {
	s := flowServer(t)
	defer s.Close()

	p := manifest.New("flow", "Flow")
	p.Spec.Mode = manifest.ModeGraph
	p.Spec.Graph = &manifest.GraphSpec{
		Nodes: []manifest.NodeSpec{
			{ID: "entry", Kind: manifest.NodeRoute, Rules: []manifest.RuleSpec{{Op: "always", Route: "next"}}},
			{ID: "worker", Kind: manifest.NodeAgent, Instruction: "Do the work."},
		},
		Edges: []manifest.EdgeSpec{
			{From: manifest.NodeRefStart, To: "entry"},
			{From: "entry", To: "worker", Route: "next"},
		},
	}
	raw, err := manifest.Encode(p)
	if err != nil {
		t.Fatal(err)
	}
	if w := call(t, s, http.MethodPost, "/api/projects", `{"slug":"flow","name":"Flow"}`); w.Code != http.StatusCreated {
		t.Fatalf("create: %d", w.Code)
	}
	if w := call(t, s, http.MethodPut, "/api/projects/flow", mustJSON(t, map[string]string{"manifestYaml": string(raw)})); w.Code != http.StatusOK {
		t.Fatalf("put: %d %s", w.Code, w.Body.String())
	}

	view := decode[graphView](t, call(t, s, http.MethodGet, "/api/projects/flow/graph", ""))
	if len(view.Nodes) != 2 || len(view.Edges) != 2 {
		t.Fatalf("graph view = %+v", view)
	}
	for _, n := range view.Nodes {
		switch n.ID {
		case "entry":
			if n.Depth != 1 {
				t.Errorf("entry depth = %d, want 1", n.Depth)
			}
			if n.In != 1 || n.Out != 1 {
				t.Errorf("entry in/out = %d/%d, want 1/1", n.In, n.Out)
			}
		case "worker":
			if n.Depth != 2 {
				t.Errorf("worker depth = %d, want 2", n.Depth)
			}
		default:
			t.Errorf("unexpected node %q", n.ID)
		}
	}

	// An llm-mode project has no graph; that is a fact, not an error.
	if w := call(t, s, http.MethodPost, "/api/projects", `{"slug":"plain","name":"Plain"}`); w.Code != http.StatusCreated {
		t.Fatalf("create plain: %d", w.Code)
	}
	empty := decode[graphView](t, call(t, s, http.MethodGet, "/api/projects/plain/graph", ""))
	if len(empty.Nodes) != 0 {
		t.Fatalf("an llm project reported a graph: %+v", empty)
	}
}

// TestPlayRejectsBadRequests keeps the streaming endpoint's preconditions
// testable without a model: every check here fails before a run starts.
func TestPlayRejectsBadRequests(t *testing.T) {
	s := flowServer(t)
	defer s.Close()
	if w := call(t, s, http.MethodPost, "/api/projects", `{"slug":"demo","name":"Demo"}`); w.Code != http.StatusCreated {
		t.Fatalf("create: %d", w.Code)
	}

	if w := call(t, s, http.MethodPost, "/api/projects/demo/play", `{"message":"hi"}`); w.Code != http.StatusBadRequest {
		t.Errorf("play without a sessionId: %d %s", w.Code, w.Body.String())
	}
	if w := call(t, s, http.MethodPost, "/api/projects/ghost/play", `{"sessionId":"s1","message":"hi"}`); w.Code != http.StatusNotFound {
		t.Errorf("play on a missing project: %d %s", w.Code, w.Body.String())
	}
	if w := call(t, s, http.MethodPost, "/api/projects/demo/play/confirm", `{"callId":"c1"}`); w.Code != http.StatusBadRequest {
		t.Errorf("confirm without a sessionId: %d", w.Code)
	}
	// Nothing is waiting for this call id, which is a no-op rather than an error:
	// the run may already have timed out.
	body := decode[struct {
		OK  bool   `json:"ok"`
		Why string `json:"reason"`
	}](t, call(t, s, http.MethodPost, "/api/projects/demo/play/confirm", `{"callId":"nope","sessionId":"s1"}`))
	if body.OK || body.Why == "" {
		t.Fatalf("confirm on an unknown call = %+v", body)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func readZip(t *testing.T, raw []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("archive is not a zip: %v", err)
	}
	out := make(map[string]string, len(zr.File))
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(rc); err != nil {
			rc.Close()
			t.Fatal(err)
		}
		rc.Close()
		out[f.Name] = buf.String()
	}
	return out
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestStatsEndpoint is the read side of the account. A fresh project answers with
// an empty account rather than a 404: the endpoint describes a project, and the
// project exists.
func TestStatsEndpoint(t *testing.T) {
	s := flowServer(t)
	defer s.Close()
	if w := call(t, s, http.MethodPost, "/api/projects", `{"slug":"demo","name":"Demo"}`); w.Code != http.StatusCreated {
		t.Fatalf("create: %d", w.Code)
	}

	w := call(t, s, http.MethodGet, "/api/projects/demo/stats", "")
	if w.Code != http.StatusOK {
		t.Fatalf("stats: %d %s", w.Code, w.Body.String())
	}
	sum := decode[play.StatsSummary](t, w)
	if sum.Turns != 0 || sum.Tools == nil {
		t.Fatalf("empty account = %+v", sum)
	}
}
