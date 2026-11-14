package api

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strings"

	"github.com/adks-dev/adks/manifest"
)

// guard is the browser-facing boundary. Binding to loopback does not protect a
// local HTTP API: a web page can still issue a cross-origin POST that the browser
// considers a "simple" request, and no preflight is sent for text/plain. We
// therefore require a same-origin hint plus a JSON content type on every request
// that changes state, and validate the slug before it reaches a filesystem path.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if slug := requestSlug(r); slug != "" && !manifest.ValidSlug(slug) {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid project slug %q", slug))
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/") || !stateChanging(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		if !sameOrigin(r) {
			writeErr(w, http.StatusForbidden, errors.New("cross-origin request rejected"))
			return
		}
		// A browser cannot send application/json as a CORS-safelisted content
		// type, so this forces a preflight that the API never grants. DELETE has
		// no body and is already non-safelisted as a method.
		if r.Method != http.MethodDelete && !isJSONContentType(r.Header.Get("Content-Type")) {
			writeErr(w, http.StatusUnsupportedMediaType, errors.New("Content-Type must be application/json"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func stateChanging(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// sameOrigin accepts requests without browser metadata (curl, CLI, tests) but
// rejects any explicit cross-origin signal. An opaque "null" origin is treated
// as untrusted.
func sameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	if origin == "null" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func isJSONContentType(v string) bool {
	mt, _, err := mime.ParseMediaType(v)
	return err == nil && mt == "application/json"
}

// auth gates the API when a token is configured. It is a second layer for
// deliberately exposed instances; guard above applies even without a token.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.token == "" || !strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/api/health" {
			next.ServeHTTP(w, r)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
			writeErr(w, http.StatusUnauthorized, errors.New("missing or invalid bearer token"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// slugFromEscapedPath reads the {slug} segment before ServeMux has matched.
// PathValue is only populated inside a handler, which is too late for the guard.
func slugFromEscapedPath(r *http.Request) string {
	parts := strings.Split(strings.TrimPrefix(r.URL.EscapedPath(), "/"), "/")
	if len(parts) < 3 || parts[0] != "api" || parts[1] != "projects" {
		return ""
	}
	v, err := url.PathUnescape(parts[2])
	if err != nil {
		return parts[2]
	}
	return v
}

// requestSlug is the guard's view: no handler has run, so only the escaped path
// is available.
func requestSlug(r *http.Request) string { return slugFromEscapedPath(r) }

// slugOf is the handler's view: prefer the matched path value and fall back to
// the same parser for requests built without a ServeMux match.
func slugOf(r *http.Request) string {
	if v := r.PathValue("slug"); v != "" {
		return v
	}
	return slugFromEscapedPath(r)
}
