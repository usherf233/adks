package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/usherf233/adks/internal/play"
)

// playTurn streams one turn as server-sent events.
//
// This handler is deliberately thin: it validates the request, asks the play
// service to pin a compiled project, and turns events into SSE frames. Everything
// stateful — the compiled-project cache, the pending confirmations, the
// confirmation resume loop — lives in the service, which is what keeps this type
// from growing a second responsibility.
func (s *Server) playTurn(w http.ResponseWriter, r *http.Request) {
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

	// Preparation happens before the first byte, so a missing project is still a
	// 404 and a manifest that does not compile is still a 400. Once the stream
	// starts the status is already committed.
	sess, err := s.play.Prepare(r.Context(), slug)
	if err != nil {
		writeLoadErr(w, err, http.StatusBadRequest)
		return
	}
	defer sess.Close()

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
	send := func(ev play.Event) {
		if _, err := fmt.Fprint(w, "data: "); err != nil {
			return
		}
		_ = enc.Encode(ev)
		_, _ = w.Write([]byte("\n"))
		flusher.Flush()
	}

	ctx, cancel := context.WithTimeout(r.Context(), sess.WallClock())
	defer cancel()

	sess.Converse(ctx, play.Request{UserID: req.UserID, SessionID: req.SessionID, Message: req.Message}, send)
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
	if !s.play.Confirm(slugOf(r), req.SessionID, req.CallID, req.Approved) {
		// Not an error worth a red banner: the run may already have timed out.
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "reason": "no pending confirmation for that callId"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
