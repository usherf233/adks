package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
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
