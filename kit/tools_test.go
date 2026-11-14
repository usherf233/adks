package kit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/genai"

	"github.com/usherf233/adks/manifest"
)

// callTool runs a tool the way the runner does, without a model.
func callTool(t *testing.T, tl tool.Tool, args map[string]any) (map[string]any, error) {
	t.Helper()
	type runner interface {
		Run(ctx agent.Context, args any) (map[string]any, error)
	}
	r, ok := tl.(runner)
	if !ok {
		t.Fatalf("tool %T is not runnable", tl)
	}
	return r.Run(newFakeCtx(), args)
}

func TestHTTPToolSubstitutesAndAuthenticates(t *testing.T) {
	var gotPath, gotQuery, gotAuth, gotIdem string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("Authorization")
		gotIdem = r.Header.Get("Idempotency-Key")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"A-1","status":"shipped"}`))
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	spec := manifest.HTTPSpec{
		Name: "get_order", Description: "fetch one order by its identifier",
		Method: "POST", URL: srv.URL + "/v1/orders/{order_id}",
		AllowedHosts: []string{host},
		Auth:         &manifest.AuthSpec{Scheme: "bearer", SecretEnv: "TEST_ORDER_TOKEN"},
		Params: []manifest.ParamSpec{
			{Name: "order_id", Type: "string", In: "path", Required: true},
			{Name: "verbose", Type: "boolean", In: "query"},
			{Name: "note", Type: "string", In: "body"},
		},
	}
	t.Setenv("TEST_ORDER_TOKEN", "s3cret")

	tl, err := NewHTTPTool(spec, HTTPToolDeps{Client: NewHTTPClient([]string{host})})
	if err != nil {
		t.Fatalf("NewHTTPTool: %v", err)
	}
	out, err := callTool(t, tl, map[string]any{"order_id": "A/1", "verbose": true, "note": "hi"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if out["ok"] != true || asInt(out["status"]) != 200 {
		t.Fatalf("unexpected result: %#v", out)
	}
	if gotPath != "/v1/orders/A%2F1" && gotPath != "/v1/orders/A/1" {
		t.Errorf("path not substituted: %q", gotPath)
	}
	if gotQuery != "verbose=true" {
		t.Errorf("query = %q", gotQuery)
	}
	if gotAuth != "Bearer s3cret" {
		t.Errorf("auth header = %q", gotAuth)
	}
	if gotIdem == "" {
		t.Error("write tool must send an Idempotency-Key")
	}
	body, _ := out["body"].(map[string]any)
	if body["status"] != "shipped" {
		t.Errorf("json body not decoded: %#v", out["body"])
	}
}

// TestHTTPToolExposesDeclaredSchema checks the wiring, not just the schema helper:
// a tool whose declaration drops the parameters would still run, but the model
// would have to guess the argument names.
func TestHTTPToolExposesDeclaredSchema(t *testing.T) {
	tl, err := NewHTTPTool(manifest.HTTPSpec{
		Name: "get_order", Description: "fetch one order by its identifier",
		Method: "GET", URL: "https://api.example.com/v1/orders/{order_id}",
		AllowedHosts: []string{"api.example.com"},
		Params: []manifest.ParamSpec{
			{Name: "order_id", Type: "string", In: "path", Required: true, Description: "订单号"},
		},
	}, HTTPToolDeps{Client: NewHTTPClient([]string{"api.example.com"})})
	if err != nil {
		t.Fatal(err)
	}
	d, ok := tl.(interface {
		Declaration() *genai.FunctionDeclaration
	})
	if !ok {
		t.Fatalf("tool %T does not expose a declaration", tl)
	}
	decl := d.Declaration()
	if decl == nil || decl.ParametersJsonSchema == nil {
		t.Fatalf("declaration lost the parameters schema: %+v", decl)
	}
	raw, err := json.Marshal(decl.ParametersJsonSchema)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "order_id") {
		t.Fatalf("schema does not mention the parameter: %s", raw)
	}
	if tl.Name() != "get_order" || tl.Description() == "" {
		t.Fatalf("tool identity wrong: %q %q", tl.Name(), tl.Description())
	}
}

func TestHTTPToolReportsFailureAsData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("order not found"))
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	tl, err := NewHTTPTool(manifest.HTTPSpec{
		Name: "get_order", Description: "fetch one order by its identifier",
		Method: "GET", URL: srv.URL + "/x", AllowedHosts: []string{host},
	}, HTTPToolDeps{Client: NewHTTPClient([]string{host})})
	if err != nil {
		t.Fatal(err)
	}
	out, err := callTool(t, tl, nil)
	if err != nil {
		// A Go error would stop the model from reacting; the design says data.
		t.Fatalf("tool returned an error instead of a structured failure: %v", err)
	}
	if out["ok"] != false {
		t.Fatalf("expected ok=false, got %#v", out)
	}
	if asInt(out["status"]) != 404 {
		t.Fatalf("expected status 404, got %#v", out["status"])
	}
	body, _ := out["body"].(string)
	if !strings.Contains(body, "UNTRUSTED_DATA") {
		t.Errorf("free text response must be wrapped: %q", body)
	}
}

func TestHTTPClientBlocksUnexpectedHost(t *testing.T) {
	client := NewHTTPClient([]string{"api.example.com"})
	req, _ := http.NewRequest("GET", "https://evil.example.net/x", nil)
	if _, err := client.Do(req); err == nil {
		t.Fatal("expected the transport to refuse a host outside the allow list")
	}
}

func TestHTTPToolMissingSecretFailsAtBuild(t *testing.T) {
	os.Unsetenv("TEST_MISSING_TOKEN")
	_, err := NewHTTPTool(manifest.HTTPSpec{
		Name: "get_order", Description: "fetch one order by its identifier",
		Method: "GET", URL: "https://api.example.com/x", AllowedHosts: []string{"api.example.com"},
		Auth: &manifest.AuthSpec{Scheme: "bearer", SecretEnv: "TEST_MISSING_TOKEN"},
	}, HTTPToolDeps{Client: NewHTTPClient([]string{"api.example.com"})})
	if err == nil {
		t.Fatal("expected an error for a missing credential")
	}
}

func TestHTTPToolTruncatesLargeResponse(t *testing.T) {
	big := strings.Repeat("x", 5000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(big))
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	tl, err := NewHTTPTool(manifest.HTTPSpec{
		Name: "get_doc", Description: "fetch a long document by its identifier",
		Method: "GET", URL: srv.URL + "/d", AllowedHosts: []string{host},
		MaxRespBytes: 500,
	}, HTTPToolDeps{Client: NewHTTPClient([]string{host})})
	if err != nil {
		t.Fatal(err)
	}
	out, err := callTool(t, tl, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out["truncated"] != true {
		t.Fatalf("expected truncated flag, got %#v", out)
	}
	body, _ := out["body"].(string)
	if len(body) > 700 {
		t.Fatalf("body not truncated: %d bytes", len(body))
	}
}

func TestSchemaFromParamsIsStable(t *testing.T) {
	params := []manifest.ParamSpec{
		{Name: "zeta", Type: "string", In: "query"},
		{Name: "alpha", Type: "integer", In: "query", Required: true},
		{Name: "tags", Type: "array_of_string", In: "body"},
	}
	var first []byte
	for i := 0; i < 20; i++ {
		raw, err := json.Marshal(schemaFromParams(params))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = raw
			continue
		}
		if string(raw) != string(first) {
			t.Fatalf("schema bytes changed between renders:\n%s\n%s", first, raw)
		}
	}
	// PropertyOrder must be explicit: the map alone does not fix key order.
	s := schemaFromParams(params)
	if len(s.PropertyOrder) != 3 || s.PropertyOrder[0] != "alpha" {
		t.Fatalf("PropertyOrder not set/sorted: %v", s.PropertyOrder)
	}
}

func TestFingerprintIgnoresMapOrder(t *testing.T) {
	a := Fingerprint("t", map[string]any{"x": 1, "y": map[string]any{"b": 2, "a": 3}})
	b := Fingerprint("t", map[string]any{"y": map[string]any{"a": 3, "b": 2}, "x": 1})
	if a != b {
		t.Fatalf("fingerprint depends on map iteration order: %s vs %s", a, b)
	}
	if a == Fingerprint("t2", map[string]any{"x": 1}) {
		t.Fatal("fingerprint must include the tool name")
	}
}

func TestSanitizeTruncatesOnRuneBoundary(t *testing.T) {
	in := []byte(strings.Repeat("字", 100))
	out := SanitizeToolOutput("src", in, 50)
	content, _ := out["content"].(string)
	if strings.ContainsRune(content, '\uFFFD') {
		t.Fatalf("truncation split a rune: %q", content)
	}
	if out["truncated"] != true {
		t.Fatalf("expected truncated: %#v", out)
	}
}

func TestLocalArtifactServiceRoundTrip(t *testing.T) {
	root := t.TempDir()
	svc, err := NewLocalArtifactService(root)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	v1, err := svc.Save(ctx, saveReq("report.txt", "first"))
	if err != nil {
		t.Fatalf("save v1: %v", err)
	}
	v2, err := svc.Save(ctx, saveReq("report.txt", "second"))
	if err != nil {
		t.Fatalf("save v2: %v", err)
	}
	if v1.Version != 1 || v2.Version != 2 {
		t.Fatalf("versions = %d, %d", v1.Version, v2.Version)
	}

	loaded, err := loadReq(svc, ctx, "report.txt", 0)
	if err != nil {
		t.Fatalf("load latest: %v", err)
	}
	if string(loaded.Part.InlineData.Data) != "second" {
		t.Fatalf("latest content = %q", loaded.Part.InlineData.Data)
	}
	back, err := loadReq(svc, ctx, "report.txt", 1)
	if err != nil {
		t.Fatalf("load v1: %v", err)
	}
	if string(back.Part.InlineData.Data) != "first" {
		t.Fatalf("v1 content = %q", back.Part.InlineData.Data)
	}

	// A file name is model-controlled input; traversal must not escape the root.
	if _, err := svc.Save(ctx, saveReq("../../escape.txt", "x")); err == nil {
		t.Fatal("expected path traversal to be rejected")
	}

	if err := svc.Delete(ctx, deleteReq("report.txt", 0)); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "app", "user", "sess", "report.txt")); !os.IsNotExist(err) {
		t.Fatalf("artifact directory survived delete: %v", err)
	}
}

func saveReq(name, text string) *artifact.SaveRequest {
	return &artifact.SaveRequest{
		AppName: "app", UserID: "user", SessionID: "sess", FileName: name,
		Part: &genai.Part{Text: text},
	}
}

func loadReq(svc artifact.Service, ctx context.Context, name string, version int64) (*artifact.LoadResponse, error) {
	return svc.Load(ctx, &artifact.LoadRequest{
		AppName: "app", UserID: "user", SessionID: "sess", FileName: name, Version: version,
	})
}

func TestEnumMatchesDeclaredParameterType(t *testing.T) {
	s := schemaFromParams([]manifest.ParamSpec{
		{Name: "count", Type: "integer", Enum: []string{"1", "2"}},
		{Name: "label", Type: "string", Enum: []string{"a", "b"}},
	})
	got := s.Properties["count"].Enum
	if len(got) != 2 || got[0] != int64(1) || got[1] != int64(2) {
		t.Fatalf("integer enum keeps string values, making the schema unsatisfiable: %#v", got)
	}
	if s.Properties["label"].Enum[0] != "a" {
		t.Fatalf("string enum was coerced unexpectedly: %#v", s.Properties["label"].Enum)
	}
}

func deleteReq(name string, version int64) *artifact.DeleteRequest {
	return &artifact.DeleteRequest{
		AppName: "app", UserID: "user", SessionID: "sess", FileName: name, Version: version,
	}
}
