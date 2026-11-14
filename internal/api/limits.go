package api

import "time"

// Budgets and body caps live in one place. Two of these used to be bare
// 120*time.Second literals with different meanings, so changing one never
// hinted that the other existed.
const (
	maxBodyBytes     = 4 << 20
	confirmTimeout   = 120 * time.Second
	defaultWallClock = 120 * time.Second
	closeTimeout     = 10 * time.Second
	tidyTimeout      = 60 * time.Second
)
