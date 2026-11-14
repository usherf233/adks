package kit

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// untrustedOpen and untrustedClose bracket third-party bytes before they reach
// the model.
//
// This is a weak defence. It lowers the chance that tool output is read as an
// instruction; it does not stop a determined injection. The value is in making
// the boundary visible to the model and greppable in a transcript.
const (
	untrustedOpen  = "[[UNTRUSTED_DATA src=%q]]"
	untrustedClose = "[[/UNTRUSTED_DATA]]"
)

// SanitizeToolOutput truncates a tool response to max bytes and wraps it in the
// untrusted-data markers. The returned map is what the tool hands back to the
// model, so the shape doubles as an audit record.
func SanitizeToolOutput(src string, b []byte, max int) map[string]any {
	original := len(b)
	truncated := false
	if max > 0 && len(b) > max {
		b = b[:max]
		truncated = true
		// Do not leave a half-written UTF-8 sequence at the cut.
		for len(b) > 0 && !utf8.Valid(b) {
			b = b[:len(b)-1]
		}
	}
	wrapped := fmt.Sprintf(untrustedOpen, src) + "\n" + string(b) + "\n" + untrustedClose
	out := map[string]any{
		"src":           src,
		"content":       wrapped,
		"originalBytes": original,
	}
	if truncated {
		out["truncated"] = true
		out["returnedBytes"] = len(b)
	}
	return out
}

// EstimateTokens gives a rough prompt size, splitting ASCII from non-ASCII.
//
// A single chars-per-token ratio underestimates Chinese by roughly a factor of
// two, which makes a capacity hint actively misleading. The coefficients are
// public rules of thumb, not official figures, so callers must label the result
// as an estimate.
func EstimateTokens(s string) int {
	var ascii, nonASCII int
	for _, r := range s {
		if r < utf8.RuneSelf {
			ascii++
		} else {
			nonASCII++
		}
	}
	return int(float64(ascii)/3.8 + float64(nonASCII)/1.7)
}

// Fingerprint hashes a tool call into a short, stable identity. It is used by the
// loop guard and written to the audit log instead of the arguments, which may
// carry personal data.
//
// Canonicalisation matters: Go map iteration is randomised, so hashing a
// rendered map without sorting keys would give the same call a different
// fingerprint on every run and the loop guard would never trip.
func Fingerprint(name string, args map[string]any) string {
	var sb strings.Builder
	sb.WriteString(name)
	writeCanonical(&sb, args)
	sum := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(sum[:8])
}

func writeCanonical(sb *strings.Builder, v any) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		sb.WriteByte('{')
		for _, k := range keys {
			sb.WriteString(k)
			sb.WriteByte(':')
			writeCanonical(sb, t[k])
			sb.WriteByte(',')
		}
		sb.WriteByte('}')
	case []any:
		sb.WriteByte('[')
		for _, e := range t {
			writeCanonical(sb, e)
			sb.WriteByte(',')
		}
		sb.WriteByte(']')
	case string:
		sb.WriteByte('"')
		sb.WriteString(t)
		sb.WriteByte('"')
	default:
		fmt.Fprintf(sb, "%v", t)
	}
}
