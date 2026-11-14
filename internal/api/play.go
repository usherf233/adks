package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// PlayEvent is one server-sent event of a trial run.
type PlayEvent struct {
	Type   string         `json:"type"` // text | tool_call | tool_result | confirm_required | error | done
	Text   string         `json:"text,omitempty"`
	Tool   string         `json:"tool,omitempty"`
	CallID string         `json:"callId,omitempty"`
	Args   map[string]any `json:"args,omitempty"`
	OK     *bool          `json:"ok,omitempty"`
	Code   string         `json:"code,omitempty"`
	DurMs  int64          `json:"durMs,omitempty"`
}

func (s *Server) play(w http.ResponseWriter, r *http.Request) {
	slug := slugOf(r)
	var req struct {
		SessionID string `json:"sessionId"`
		Message   string `json:"message"`
		UserID    string `json:"userId"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.SessionID == "" {
		writeErr(w, http.StatusBadRequest, errors.New("sessionId is required"))
		return
	}
	if req.UserID == "" {
		req.UserID = "builder"
	}

	rnr, p, release, err := s.layers.Get(r.Context(), slug)
	if err != nil {
		// A missing project is 404; a manifest that does not compile is a 400.
		writeLoadErr(w, err, http.StatusBadRequest)
		return
	}
	defer release()

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, errors.New("streaming is not supported by this connection"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	enc := json.NewEncoder(w)
	send := func(ev PlayEvent) {
		if _, err := fmt.Fprintf(w, "data: "); err != nil {
			return
		}
		_ = enc.Encode(ev)
		_, _ = w.Write([]byte("\n"))
		flusher.Flush()
	}

	wallClock := time.Duration(p.Spec.Policy.Limits.MaxWallClockSec) * time.Second
	if wallClock <= 0 {
		wallClock = defaultWallClock
	}
	ctx, cancel := context.WithTimeout(r.Context(), wallClock)
	defer cancel()

	s.converse(ctx, rnr, slug, req.UserID, req.SessionID, req.Message, send)
}

// confirm receives a human decision.
func (s *Server) confirm(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CallID    string `json:"callId"`
		SessionID string `json:"sessionId"`
		Approved  bool   `json:"approved"`
	}
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.CallID == "" || req.SessionID == "" {
		writeErr(w, http.StatusBadRequest, errors.New("callId and sessionId are required"))
		return
	}
	if !s.plays.deliver(req.CallID, req.Approved, slugOf(r), req.SessionID) {
		// Not an error worth a red banner: the run may already have timed out.
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "reason": "no pending confirmation for that callId"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
