package kit

import "time"

// Outbound HTTP budgets. Both the HTTP tool transport and the MCP HTTP/SSE
// transport used to carry their own copy of these numbers with different error
// text, so a change to one path did not reach the other.
const (
	maxRedirects = 5
	retryDelay   = 200 * time.Millisecond
)
