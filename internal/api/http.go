package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/adks-dev/adks/internal/store"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("api: encode response: %v", err)
	}
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]any{"error": err.Error()})
}

// writeLoadErr is the one mapping every project endpoint agrees on: a project
// that does not exist is 404, whatever else went wrong keeps the status the
// endpoint would have used before. getProject is the deliberate exception — it
// turns a parse failure into 200 so the editor can open and fix the manifest.
func writeLoadErr(w http.ResponseWriter, err error, fallback int) {
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeErr(w, fallback, err)
}

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return fmt.Errorf("empty request body")
	}
	return json.Unmarshal(body, v)
}

// timeNow is a seam for tests.
var timeNow = time.Now

// readOptionalJSON makes "this endpoint accepts an empty body" explicit.
// verify and export fall back to their defaults when the client posts nothing;
// swallowing readJSON's error inline hid that intent.
func readOptionalJSON(r *http.Request, v any) {
	_ = readJSON(r, v)
}
