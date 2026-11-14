// Package adks holds the assets that both the builder and an exported project
// need: the compiler source itself and the web UI.
//
// kit and manifest are embedded from their real location rather than from a
// template copy. An export therefore contains the exact bytes the builder ran,
// which is the only way "what I tried" and "what I shipped" cannot drift.
package adks

import "embed"

// FS holds the embedded source tree.
//
//go:embed go.mod go.sum all:kit all:manifest all:web
var FS embed.FS
