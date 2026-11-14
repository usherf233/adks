package manifest

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

var (
	reSlug        = regexp.MustCompile(`^[a-z][a-z0-9-]{1,40}$`)
	reAgentName   = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)
	reToolName    = reAgentName
	reMCPID       = regexp.MustCompile(`^[a-z][a-z0-9-]{1,20}$`)
	reEnvName     = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)
	reISODate     = regexp.MustCompile(`\b20\d{2}-\d{2}-\d{2}\b`)
	reUUID        = regexp.MustCompile(`\b[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}\b`)
	rePlaceholder = regexp.MustCompile(`\{([a-zA-Z_][a-zA-Z0-9_]*)\}`)
	reHeaderName  = regexp.MustCompile(`^[A-Za-z0-9!#$%&'*+\-.^_` + "`" + `|~]+$`)
)

// ValidateEndpoint checks a URL the runtime will connect to. It is shared by the
// validator and kit, so a project built without editor validation still cannot
// point a tool at a non-HTTP scheme or a cloud metadata service.
func ValidateEndpoint(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("endpoint url is empty")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return fmt.Errorf("endpoint %q is not an absolute URL", raw)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("endpoint %q must use http or https", raw)
	}
	if u.User != nil {
		return fmt.Errorf("endpoint %q must not embed credentials", raw)
	}
	host := strings.ToLower(u.Hostname())
	if host == "metadata.google.internal" || strings.HasSuffix(host, ".metadata.google.internal") {
		return fmt.Errorf("endpoint %q points at a cloud metadata service", raw)
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()) {
		return fmt.Errorf("endpoint %q points at a link-local address", raw)
	}
	return nil
}

// builtinTools is the closed set kit can wire without extra code.
var builtinTools = map[string]bool{
	"google_search":  true,
	"url_context":    true,
	"exit_loop":      true,
	"load_artifacts": true,
	"preload_memory": true,
}

// BuiltinToolNames lists the builtin tool identifiers, for UI dropdowns.
func BuiltinToolNames() []string {
	out := make([]string, 0, len(builtinTools))
	for k := range builtinTools {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ValidSlug is the single admission rule for a project slug. The HTTP layer,
// the store and Create all call it: paths are derived from this string, so a
// second copy of the rule would let one caller admit what another rejects.
func ValidSlug(s string) bool { return reSlug.MatchString(s) }

// HasErrors reports whether an issue list contains an error-level finding.
// Warnings are advice and never block a save, build or export.
func HasErrors(issues []Issue) bool {
	for _, i := range issues {
		if i.Level == LevelError {
			return true
		}
	}
	return false
}

// HostInAllowList compares a URL host against an allow list. A manifest is
// validated from a *url.URL (which knows Hostname()) while the runtime transport
// only sees req.URL.Host, so all three spellings -- with port, without port, and
// a bracketed IPv6 literal -- are accepted here.
func HostInAllowList(host string, allowed []string) bool {
	candidates := []string{host}
	h := host
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
		candidates = append(candidates, h)
	}
	if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		candidates = append(candidates, strings.TrimSuffix(strings.TrimPrefix(h, "["), "]"))
	}
	for _, a := range allowed {
		for _, c := range candidates {
			if strings.EqualFold(a, c) {
				return true
			}
		}
	}
	return false
}
