package codegen

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/usherf233/adks/manifest"
)

// This file is the contract behind "the manifest is the single source of truth".
//
// A generation-time constant is the one place where a field can be declared,
// validated, shown in the UI, and still never reach the running binary: the value
// has to be rendered into main.go's const block and then actually used by
// defaultArgs. Reading the field is not enough — host was read into mainData and
// the template simply never emitted it, which no "is this field referenced"
// check can see.
//
// So the contract is value-based: set a distinctive value, render, look for it.
// serverWiring additionally classifies every field of the section, so adding a
// seventh server field fails here until someone decides where it goes.

// serverWiring maps every spec.runtime.server field to how it reaches the export.
// A new field must be added here, which is the point: the alternative is a
// silently inert knob.
var serverWiring = map[string]string{
	"mode":       "codegen: selects the launcher keyword in defaultArgs",
	"host":       "codegen: -host constant, overridable with ADKS_HOST",
	"port":       "codegen: -port constant and the Dockerfile EXPOSE",
	"pathPrefix": "codegen: -path_prefix, a flag of the api sublauncher",
	"a2a":        "codegen: a2a sublauncher keyword",
	"auth":       "runtime: main.go reads the embedded manifest at start-up",
}

// TestServerSectionIsClassified is the anti-rot half. It does not prove the
// wiring works (the value tests below do); it proves nobody added a field to
// ServerSpec without deciding.
func TestServerSectionIsClassified(t *testing.T) {
	typ := reflect.TypeOf(manifest.ServerSpec{})
	seen := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		name := strings.Split(f.Tag.Get("yaml"), ",")[0]
		if name == "" {
			continue
		}
		seen[name] = true
		if _, ok := serverWiring[name]; !ok {
			t.Errorf("spec.runtime.server.%s is not classified: wire it and describe how, or it will be inert like host and a2a were", name)
		}
	}
	for name := range serverWiring {
		if !seen[name] {
			t.Errorf("serverWiring names %q, which is no longer a field of ServerSpec", name)
		}
	}
}

// TestServerSectionReachesExport is the value-injection half: each case sets one
// field to a value that cannot appear by accident and looks for it in the
// artifact that is supposed to carry it.
func TestServerSectionReachesExport(t *testing.T) {
	cases := []struct {
		name   string
		set    func(*manifest.Project)
		file   string
		wants  []string
		avoids []string
	}{
		{
			name:  "host",
			set:   func(p *manifest.Project) { p.Spec.Runtime.Server.Host = "10.9.8.7" },
			file:  "main.go",
			wants: []string{`host = "10.9.8.7"`, `"-host", bind`},
		},
		{
			name:  "port",
			set:   func(p *manifest.Project) { p.Spec.Runtime.Server.Port = 9099 },
			file:  "main.go",
			wants: []string{`port = "9099"`},
		},
		{
			name:  "port reaches EXPOSE",
			set:   func(p *manifest.Project) { p.Spec.Runtime.Server.Port = 9099 },
			file:  "Dockerfile",
			wants: []string{"EXPOSE 9099"},
		},
		{
			name:  "mode api",
			set:   func(p *manifest.Project) { p.Spec.Runtime.Server.Mode = "api" },
			file:  "main.go",
			wants: []string{`mode = "api"`},
		},
		{
			name:  "pathPrefix",
			set:   func(p *manifest.Project) { p.Spec.Runtime.Server.PathPrefix = "/v1/agent" },
			file:  "main.go",
			wants: []string{`pathPrefix = "/v1/agent"`, `"-path_prefix", pathPrefix`},
		},
		{
			name:  "a2a off by default",
			set:   func(p *manifest.Project) {},
			file:  "main.go",
			wants: []string{"a2a = false"},
		},
		{
			name:  "a2a on",
			set:   func(p *manifest.Project) { p.Spec.Runtime.Server.A2A = true },
			file:  "main.go",
			wants: []string{`a2a = true`, `"a2a"`},
		},
	}

	seed := os.DirFS("../..")
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := manifest.New("demo", "Demo")
			tc.set(p)
			b, err := Render(p, seed)
			if err != nil {
				t.Fatal(err)
			}
			got := string(b.Files[tc.file])
			if got == "" {
				t.Fatalf("%s is missing from the export", tc.file)
			}
			for _, want := range tc.wants {
				if !strings.Contains(got, want) {
					t.Errorf("%s does not contain %q:\n%s", tc.file, want, got)
				}
			}
			for _, avoid := range tc.avoids {
				if strings.Contains(got, avoid) {
					t.Errorf("%s contains %q, which should only appear when the field is set:\n%s", tc.file, avoid, got)
				}
			}
		})
	}
}

// TestConsoleModeIgnoresWebFlags keeps the two arg builders from leaking into each
// other: console takes no flags at all, so a stray -host there is a start-up error.
func TestConsoleModeIgnoresWebFlags(t *testing.T) {
	p := manifest.New("demo", "Demo")
	p.Spec.Runtime.Server.Mode = "console"
	p.Spec.Runtime.Server.PathPrefix = "/v1"
	p.Spec.Runtime.Server.A2A = true
	b, err := Render(p, os.DirFS("../.."))
	if err != nil {
		t.Fatal(err)
	}
	main := string(b.Files["main.go"])
	if !strings.Contains(main, `return []string{"console"}`) {
		t.Fatalf("console mode does not return a bare keyword:\n%s", main)
	}
}

// TestGeneratedMainWiresEval keeps the self-check reachable: the archive carries
// evals.yaml, so the binary it builds has to be able to run it. A generated main.go
// that silently lost -eval would ship a project that can only be checked from the
// builder that made it.
func TestGeneratedMainWiresEval(t *testing.T) {
	b, err := Render(manifest.New("demo", "Demo"), os.DirFS("../.."))
	if err != nil {
		t.Fatal(err)
	}
	main := string(b.Files["main.go"])
	for _, want := range []string{
		`os.Args[1] == "-eval"`,
		`kit.ParseEvals`,
		`kit.RunEvals`,
		`ADKS_EVALS`,
		`return 1`,
		`return 2`,
	} {
		if !strings.Contains(main, want) {
			t.Errorf("generated main.go lost %q", want)
		}
	}
}
