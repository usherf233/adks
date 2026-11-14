package kit

import (
	"testing"

	"github.com/adks-dev/adks/manifest"
)

// The validator and the builder each hold a closed set of builtin tools. They
// have to be the same set, or a manifest passes validation and then fails at
// build time (or the reverse).
func TestBuiltinToolsMatchManifest(t *testing.T) {
	names := manifest.BuiltinToolNames()
	if len(names) != len(builtinTools) {
		t.Fatalf("manifest lists %d builtins, kit can build %d", len(names), len(builtinTools))
	}
	for _, name := range names {
		if f, ok := builtinTools[name]; !ok || f == nil {
			t.Errorf("manifest advertises %q but kit cannot build it", name)
		}
	}
	for name := range builtinTools {
		found := false
		for _, n := range names {
			if n == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("kit builds %q but the manifest does not list it", name)
		}
	}
}
