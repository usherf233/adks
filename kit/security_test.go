package kit

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/usherf233/adks/manifest"
)

func TestHTTPTool5xxKeepsResponseBodyReadable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("upstream boom"))
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")

	tl, err := NewHTTPTool(manifest.HTTPSpec{
		Name: "get_x", Method: "GET", URL: srv.URL + "/x", AllowedHosts: []string{host},
	}, HTTPToolDeps{Client: NewHTTPClient([]string{host})})
	if err != nil {
		t.Fatal(err)
	}
	out, err := callTool(t, tl, nil)
	if err != nil {
		t.Fatal(err)
	}
	if asInt(out["status"]) != http.StatusInternalServerError {
		t.Fatalf("status = %v, want 500", out["status"])
	}
	if !strings.Contains(fmt.Sprint(out["body"]), "upstream boom") {
		t.Fatalf("response body was lost: %#v", out["body"])
	}
	if strings.Contains(fmt.Sprint(out["error"]), "closed Body") {
		t.Fatalf("handler read a closed body: %v", out["error"])
	}
}

func TestHTTPToolRedirectStaysInItsOwnAllowList(t *testing.T) {
	reached := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
	}))
	defer target.Close()
	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/x", http.StatusFound)
	}))
	defer src.Close()
	srcHost := strings.TrimPrefix(src.URL, "http://")

	tl, err := NewHTTPTool(manifest.HTTPSpec{
		Name: "get_x", Method: "GET", URL: src.URL + "/start", AllowedHosts: []string{srcHost},
	}, HTTPToolDeps{Client: NewHTTPClient([]string{srcHost})})
	if err != nil {
		t.Fatal(err)
	}
	out, err := callTool(t, tl, nil)
	if err != nil {
		t.Fatal(err)
	}
	if reached {
		t.Fatal("redirect reached a host outside this tool's allow list")
	}
	if out["ok"] != false || !strings.Contains(fmt.Sprint(out["error"]), "allowedHosts") {
		t.Fatalf("expected a host-guard failure, got %#v", out)
	}
}

func TestMCPTransportUsesExpandedHeaders(t *testing.T) {
	ref := manifest.MCPRef{
		ID: "remote", Transport: "http", URL: "https://mcp.example.com/rpc",
		Headers: map[string]string{"Authorization": "${MCP_TOKEN}"},
	}
	tr, err := buildTransport(ref, MCPDeps{Headers: map[string]string{"Authorization": "resolved-token"}})
	if err != nil {
		t.Fatal(err)
	}
	st, ok := tr.(*mcp.StreamableClientTransport)
	if !ok {
		t.Fatalf("unexpected transport %T", tr)
	}
	ht, ok := st.HTTPClient.Transport.(headerTransport)
	if !ok {
		t.Fatalf("unexpected round tripper %T", st.HTTPClient.Transport)
	}
	if ht.headers["Authorization"] != "resolved-token" {
		t.Fatalf("header = %q, want the expanded value", ht.headers["Authorization"])
	}
	if ht.host != "mcp.example.com" {
		t.Fatalf("header scope = %q", ht.host)
	}
}

func TestMCPTransportRejectsUnsafeEndpoints(t *testing.T) {
	for _, raw := range []string{"", "ftp://mcp.example.com/rpc", "http://169.254.169.254/latest/meta-data/"} {
		_, err := buildTransport(manifest.MCPRef{ID: "remote", Transport: "sse", URL: raw}, MCPDeps{})
		if err == nil {
			t.Errorf("endpoint %q was accepted", raw)
		}
	}
}

func TestLoopGuardIsScopedToAnInvocation(t *testing.T) {
	tl, err := NewHTTPTool(manifest.HTTPSpec{
		Name: "dummy", Method: "GET", URL: "https://api.example.com/x", AllowedHosts: []string{"api.example.com"},
	}, HTTPToolDeps{Client: NewHTTPClient([]string{"api.example.com"})})
	if err != nil {
		t.Fatal(err)
	}
	guard := NewLoopGuard()
	for i := 0; i < 4; i++ {
		if _, err := guard(newFakeCtx(), tl, map[string]any{}); err != nil {
			t.Fatalf("separate invocation %d was flagged as a loop: %v", i, err)
		}
	}
	ctx := newFakeCtx()
	for i := 0; i < 2; i++ {
		if _, err := guard(ctx, tl, map[string]any{}); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if _, err := guard(ctx, tl, map[string]any{}); !errors.Is(err, ErrLoopDetected) {
		t.Fatalf("three identical calls in one invocation must loop-guard, got %v", err)
	}
}

type slowCloser struct{}

func (slowCloser) Close() error { time.Sleep(500 * time.Millisecond); return nil }

func TestLayerCloseRespectsContext(t *testing.T) {
	l := &Layer{}
	l.AddCloser(slowCloser{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := l.Close(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Close ignored its deadline: %v", err)
	}
}
