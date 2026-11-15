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
)
