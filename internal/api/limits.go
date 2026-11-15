package api

import "time"

// Budgets and body caps that belong to the HTTP surface live here.
//
// Turn-level budgets (confirmation wait, wall clock, layer shutdown, idle
// eviction) moved with the play service: a constant that only one subsystem reads
// belongs next to that subsystem, or the two drift back together.
const (
	maxBodyBytes = 4 << 20
	// tidyTimeout bounds `go mod tidy` in verify and export. Without it a hung
	// module proxy would hold the request open indefinitely.
	tidyTimeout = 60 * time.Second
	// sseHeartbeat is how often an idle trial stream emits a comment frame.
	//
	// The number is picked against proxies, not against us: nginx defaults to a
	// 60s read timeout and many load balancers use 30s, so 15s leaves three chances
	// per timeout window. Waiting for a human to approve a tool call is the case
	// that matters — the stream is silent for up to two minutes there.
	sseHeartbeat = 15 * time.Second
)
