package kit

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/usherf233/adks/manifest"
)

// HTTPToolDeps is what an HTTP tool needs from its host process.
type HTTPToolDeps struct {
	// Client is shared by every HTTP tool of one build so connections are
	// pooled. Its transport must already enforce the host allow list.
	Client *http.Client
	// Secrets maps an environment variable name to its value, resolved at build
	// time. Values stay in memory and never reach the manifest or a log.
	Secrets map[string]string
	// Semaphore bounds concurrent tool calls. Nil means no bound.
	Semaphore chan struct{}
	// Audit receives one event per call. Nil disables auditing.
	Audit *AuditSink
}

// NewHTTPTool turns one manifest HTTP declaration into an ADK tool.
//
// ADK Go ships no OpenAPI toolset, so this is the only way a manifest can express
// a REST call. Consequences worth stating: the schema is built from the manifest
// parameters (not from an OpenAPI document), and failures come back as data
// rather than as errors, so the model can read the status code and retry with
// different arguments.
func NewHTTPTool(spec manifest.HTTPSpec, deps HTTPToolDeps) (tool.Tool, error) {
	if deps.Client == nil {
		return nil, fmt.Errorf("http tool %q: no HTTP client supplied", spec.Name)
	}
	timeout := time.Duration(spec.TimeoutSec) * time.Second
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	maxBytes := spec.MaxRespBytes
	if maxBytes <= 0 {
		maxBytes = 64 * 1024
	}

	authHeader, authValue, err := resolveAuth(spec, deps.Secrets)
	if err != nil {
		return nil, err
	}

	handler := func(ctx agent.Context, args map[string]any) (map[string]any, error) {
		if deps.Semaphore != nil {
			select {
			case deps.Semaphore <- struct{}{}:
				defer func() { <-deps.Semaphore }()
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}

		req, err := buildRequest(spec, args, authHeader, authValue, ctx)
		if err != nil {
			return map[string]any{"ok": false, "error": err.Error(), "kind": "request"}, nil
		}

		cctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()

		start := time.Now()
		resp, err := doWithRetry(deps.Client, req.WithContext(cctx), spec)
		durMs := time.Since(start).Milliseconds()
		if err != nil {
			return map[string]any{
				"ok":     false,
				"kind":   "transport",
				"error":  err.Error(),
				"durMs":  durMs,
				"status": 0,
			}, nil
		}
		defer resp.Body.Close()

		body, readErr := io.ReadAll(io.LimitReader(resp.Body, int64(maxBytes)+1))
		if readErr != nil {
			return map[string]any{"ok": false, "kind": "read", "error": readErr.Error(), "status": resp.StatusCode}, nil
		}

		out := map[string]any{
			"status": resp.StatusCode,
			"ok":     resp.StatusCode >= 200 && resp.StatusCode < 300,
			"durMs":  durMs,
		}
		if !out["ok"].(bool) {
			out["error"] = fmt.Sprintf("%s returned HTTP %d", spec.Name, resp.StatusCode)
		}

		// A JSON object is data the model can reason about; free text is wrapped
		// so its origin stays visible.
		var parsed any
		if json.Unmarshal(body, &parsed) == nil {
			out["body"] = parsed
		} else {
			out["body"] = SanitizeToolOutput(spec.URL, body, maxBytes)["content"]
		}
		if len(body) > maxBytes {
			out["truncated"] = true
			out["originalBytes"] = len(body)
		}
		return out, nil
	}

	cfg := functiontool.Config{
		Name:        spec.Name,
		Description: spec.Description,
		InputSchema: schemaFromParams(spec.Params),
	}
	return functiontool.New[map[string]any, map[string]any](cfg, handler)
}

// resolveAuth reads the secret once, at build time. A missing secret is a
// configuration error: discovering it mid-conversation would look like a tool
// bug to the model.
func resolveAuth(spec manifest.HTTPSpec, secrets map[string]string) (header, value string, err error) {
	if spec.Auth == nil {
		return "", "", nil
	}
	secret, ok := secrets[spec.Auth.SecretEnv]
	if !ok || secret == "" {
		secret = os.Getenv(spec.Auth.SecretEnv)
	}
	if secret == "" {
		return "", "", fmt.Errorf("http tool %q: environment variable %s is empty", spec.Name, spec.Auth.SecretEnv)
	}
	switch spec.Auth.Scheme {
	case "bearer":
		return "Authorization", "Bearer " + secret, nil
	case "apikey-header":
		h := spec.Auth.Header
		if h == "" {
			h = "X-API-Key"
		}
		return h, secret, nil
	case "basic":
		// Accept either "user:pass" or an already-encoded value, so a manifest
		// that followed the old (broken) behaviour keeps working.
		if decoded, err := base64.StdEncoding.DecodeString(secret); err == nil && bytes.Contains(decoded, []byte(":")) {
			return "Authorization", "Basic " + secret, nil
		}
		return "Authorization", "Basic " + base64.StdEncoding.EncodeToString([]byte(secret)), nil
	}
	return "", "", fmt.Errorf("http tool %q: unsupported auth scheme %q", spec.Name, spec.Auth.Scheme)
}

func buildRequest(spec manifest.HTTPSpec, args map[string]any, authHeader, authValue string, ctx agent.Context) (*http.Request, error) {
	rawURL := spec.URL
	query := url.Values{}
	body := map[string]any{}

	for _, p := range spec.Params {
		v, present := args[p.Name]
		if !present || v == nil {
			if p.Required {
				return nil, fmt.Errorf("missing required parameter %q", p.Name)
			}
			continue
		}
		switch p.In {
		case "path":
			rawURL = strings.ReplaceAll(rawURL, "{"+p.Name+"}", url.PathEscape(fmt.Sprint(v)))
		case "query":
			query.Set(p.Name, fmt.Sprint(v))
		case "body":
			body[p.Name] = v
		}
	}
	if strings.Contains(rawURL, "{") && strings.Contains(rawURL, "}") {
		return nil, fmt.Errorf("unresolved placeholder in url %q", spec.URL)
	}
	if len(query) > 0 {
		sep := "?"
		if strings.Contains(rawURL, "?") {
			sep = "&"
		}
		rawURL += sep + query.Encode()
	}

	var reader io.Reader
	if len(body) > 0 {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode body: %w", err)
		}
		reader = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, spec.Method, rawURL, reader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if authHeader != "" {
		req.Header.Set(authHeader, authValue)
	}

	// Re-check the host here as well as in the transport: the transport guards
	// redirects, this guards the manifest itself. Both compare names, not resolved
	// IPs; a host that rebinds to a private address after build time is not
	// covered, so allowedHosts must name hosts you trust.
	u, err := url.Parse(rawURL)
	if err != nil || !hostAllowed(u.Host, spec.AllowedHosts) {
		return nil, fmt.Errorf("host %q is not in allowedHosts", u.Host)
	}

	// Writes get a deterministic key so a retry after a timeout cannot double
	// apply the same order. The key is derived from the call identity, not from
	// noise, so it stays stable across processes.
	if spec.Method != http.MethodGet && spec.Method != http.MethodHead {
		req.Header.Set("Idempotency-Key", fmt.Sprintf("%s:%s:%s",
			ctx.SessionID(), spec.Name, Fingerprint(spec.Name, args)))
	}
	return req, nil
}

// hostAllowed shares the manifest's rule so validation and the runtime transport
// cannot disagree about a host with a port or an IPv6 literal.
func hostAllowed(host string, allowed []string) bool {
	return manifest.HostInAllowList(host, allowed)
}

// doWithRetry retries once on a transient failure. Permanent failures (4xx) are
// returned immediately: retrying a 401 only burns time.
func doWithRetry(client *http.Client, req *http.Request, spec manifest.HTTPSpec) (*http.Response, error) {
	resp, err := client.Do(req)
	if err != nil {
		if req.Body != nil && req.GetBody != nil {
			if r2, e2 := req.GetBody(); e2 == nil {
				req2 := req.Clone(req.Context())
				req2.Body = r2
				time.Sleep(retryDelay)
				return client.Do(req2)
			}
		}
		return resp, err
	}
	if resp.StatusCode >= 500 && req.Body != nil && req.GetBody != nil {
		if r2, e2 := req.GetBody(); e2 == nil {
			// Only close the first response when we actually retry. GET/HEAD have
			// no body to replay, and returning a response with a closed body used
			// to make the caller's read fail with "invalid Read on closed Body".
			resp.Body.Close()
			req2 := req.Clone(req.Context())
			req2.Body = r2
			time.Sleep(retryDelay)
			return client.Do(req2)
		}
	}
	return resp, nil
}

// hostGuardTransport refuses a request whose host is not on the list. It exists
// on the client so that redirects are checked too: a handler-level check only
// sees the URL the manifest wrote.
type hostGuardTransport struct {
	base    http.RoundTripper
	allowed []string
}

func (t hostGuardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !hostAllowed(req.URL.Host, t.allowed) {
		return nil, fmt.Errorf("host %q is not in allowedHosts", req.URL.Host)
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// NewHTTPClient builds a client for one HTTP tool. allowedHosts is that tool's
// own list, never the union of the project's lists: a redirect is checked against
// it both here and in the transport, so a response cannot steer a credential to a
// host the tool was not allowed to call.
func NewHTTPClient(allowedHosts []string) *http.Client {
	return &http.Client{
		Transport: hostGuardTransport{base: http.DefaultTransport, allowed: allowedHosts},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("stopped after 5 redirects")
			}
			if !hostAllowed(req.URL.Host, allowedHosts) {
				return fmt.Errorf("redirect to host %q is not in allowedHosts", req.URL.Host)
			}
			return nil
		},
	}
}
