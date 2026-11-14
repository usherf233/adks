// Package codegen renders a project into a standalone Go module.
//
// The generator copies kit/ and manifest/ byte for byte out of the builder's own
// embedded source instead of rendering them from templates. A template copy would
// be a second implementation, and the only reliable way to keep two
// implementations in sync is to not have two.
package codegen

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/adks-dev/adks/manifest"
)

// ModulePath is the import path an exported project declares.
//
// It is identical to the builder's, which is what lets kit's own imports resolve
// inside the exported module without rewriting a single import line. The exported
// module is self-contained, so nothing is ever fetched from this path.
const ModulePath = "github.com/adks-dev/adks"

// Bundle is an exported project in memory: relative path to content.
type Bundle struct {
	Files map[string][]byte
}

// Paths returns the file list in a stable order.
func (b *Bundle) Paths() []string {
	out := make([]string, 0, len(b.Files))
	for p := range b.Files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// Bytes is the total size of the bundle.
func (b *Bundle) Bytes() int {
	n := 0
	for _, f := range b.Files {
		n += len(f)
	}
	return n
}

type mainData struct {
	Slug    string
	Agent   string
	Mode    string
	Host    string
	Port    string
	Audit   bool
	Version string
}

// Render builds the bundle for one project.
func Render(p *manifest.Project, seed fs.FS) (*Bundle, error) {
	if p == nil {
		return nil, fmt.Errorf("codegen: nil project")
	}
	yamlBytes, err := manifest.Encode(p)
	if err != nil {
		return nil, err
	}

	b := &Bundle{Files: map[string][]byte{}}
	b.Files["agent.yaml"] = yamlBytes

	data := mainData{
		Slug:    p.Metadata.Slug,
		Agent:   p.Spec.Agent.Name,
		Mode:    p.Spec.Runtime.Server.Mode,
		Host:    p.Spec.Runtime.Server.Host,
		Port:    strconv.Itoa(p.Spec.Runtime.Server.Port),
		Audit:   true,
		Version: Version,
	}
	var mainBuf bytes.Buffer
	if err := mainTmpl.Execute(&mainBuf, data); err != nil {
		return nil, fmt.Errorf("codegen: render main.go: %w", err)
	}
	b.Files["main.go"] = mainBuf.Bytes()

	readmeBuf := new(bytes.Buffer)
	if err := readmeTmpl.Execute(readmeBuf, map[string]any{
		"Slug":      p.Metadata.Slug,
		"Name":      p.Metadata.Name,
		"Agent":     p.Spec.Agent.Name,
		"Provider":  p.Spec.Agent.Model.Provider,
		"Model":     p.Spec.Agent.Model.Name,
		"APIKeyEnv": p.Spec.Agent.Model.APIKeyEnv,
		"Mode":      p.Spec.Runtime.Server.Mode,
		"Port":      p.Spec.Runtime.Server.Port,
		"Host":      p.Spec.Runtime.Server.Host,
		"Session":   p.Spec.Runtime.Session.Backend,
		"Artifact":  p.Spec.Runtime.Artifact.Backend,
		"Tools":     len(p.Spec.Tools.Builtin) + len(p.Spec.Tools.HTTP) + len(p.Spec.Tools.MCP),
		"Version":   Version,
	}); err != nil {
		return nil, fmt.Errorf("codegen: render README.md: %w", err)
	}
	b.Files["README.md"] = readmeBuf.Bytes()
	b.Files[".gitignore"] = []byte(gitignore)

	// The compiler sources travel with the project.
	for _, dir := range []string{"kit", "manifest"} {
		err := fs.WalkDir(seed, dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if strings.HasSuffix(p, "_test.go") {
				return nil
			}
			if !strings.HasSuffix(p, ".go") {
				return nil
			}
			content, err := fs.ReadFile(seed, p)
			if err != nil {
				return err
			}
			b.Files[path.Join(dir, strings.TrimPrefix(p, dir+"/"))] = content
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("codegen: copy %s: %w", dir, err)
		}
	}

	// Dependencies travel too: without go.mod and go.sum the project only builds
	// on a machine that can reach the module proxy with the right versions.
	for _, name := range []string{"go.mod", "go.sum"} {
		content, err := fs.ReadFile(seed, name)
		if err != nil {
			return nil, fmt.Errorf("codegen: copy %s: %w", name, err)
		}
		b.Files[name] = content
	}

	// The image must carry at least the toolchain version go.mod asks for. A
	// hardcoded tag silently drifts whenever the builder upgrades Go.
	goVersion, err := goVersionFromMod(b.Files["go.mod"])
	if err != nil {
		return nil, err
	}
	b.Files["Dockerfile"] = []byte(strings.Replace(dockerfileTmpl, "{{GO_VERSION}}", goVersion, 1))
	return b, nil
}

func goVersionFromMod(goMod []byte) (string, error) {
	for _, line := range strings.Split(string(goMod), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "go ") {
			continue
		}
		v := strings.TrimSpace(strings.TrimPrefix(line, "go "))
		if v != "" {
			return v, nil
		}
	}
	return "", fmt.Errorf("codegen: go.mod has no go directive; cannot choose a builder image")
}

// WriteDir materialises the bundle. Existing files are overwritten, which is what
// re-exporting into the same directory should do.
// validBundlePath rejects absolute paths and any .. traversal, not merely the
// substring ".." (which also occurs in perfectly ordinary filenames).
func validBundlePath(rel string) bool {
	if rel == "" || strings.HasPrefix(rel, "/") || filepath.IsAbs(rel) {
		return false
	}
	clean := path.Clean(rel)
	return clean == rel && clean != ".." && !strings.HasPrefix(clean, "../")
}

func WriteDir(b *Bundle, dir string) error {
	for _, rel := range b.Paths() {
		if !validBundlePath(rel) {
			return fmt.Errorf("codegen: refusing to write %q", rel)
		}
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, b.Files[rel], 0o644); err != nil {
			return fmt.Errorf("codegen: write %s: %w", rel, err)
		}
	}
	return nil
}

// WriteZip streams the bundle as an archive.
func WriteZip(b *Bundle, w *zip.Writer) error {
	for _, rel := range b.Paths() {
		if !validBundlePath(rel) {
			return fmt.Errorf("codegen: refusing to archive %q", rel)
		}
		f, err := w.Create(rel)
		if err != nil {
			return err
		}
		if _, err := f.Write(b.Files[rel]); err != nil {
			return err
		}
	}
	return nil
}

// Tidy resolves the module requirements in dir.
//
// The copied go.mod is the builder's, so it describes the builder's build list.
// The exported module is smaller and its `// indirect` markers are stale, which
// makes plain `go build` refuse to run. One tidy fixes both, and it is allowed to
// fail: without a module proxy the copied files still build with -mod=mod.
func Tidy(ctx context.Context, dir string) error {
	cmd := exec.CommandContext(ctx, "go", "mod", "tidy")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go mod tidy: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Version is stamped into generated files so a reader can tell which builder
// produced them.
var Version = "dev"

const dockerfileTmpl = `# Build stage
FROM golang:{{GO_VERSION}} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /out/agent .

# Run stage
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/agent /app/agent
COPY agent.yaml /app/agent.yaml
USER nonroot:nonroot
EXPOSE 8080
# The manifest is embedded, so the binary is self-describing: no config mount.
ENTRYPOINT ["/app/agent"]
`

const gitignore = `.adks/
sessions.db*
audit.jsonl
agent
*.exe
`

var mainTmpl = template.Must(template.New("main").Parse(`// Code generated by adks {{ .Version }}. DO NOT EDIT main.go.
//
// Edit agent.yaml instead: it is embedded next to this file and is the only
// source of truth for the agent's behaviour.
package main

import (
	"context"
	_ "embed"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"

	"google.golang.org/adk/v2/cmd/launcher/full"
	"google.golang.org/adk/v2/server/authn"

	"` + ModulePath + `/kit"
	"` + ModulePath + `/manifest"
)

//go:embed agent.yaml
var manifestYAML []byte

const (
	// mode selects the launcher sub-command. ADK v2's web launcher has no
	// -host flag: it listens on :port on every interface.
	mode = {{ printf "%q" .Mode }}
	port = {{ printf "%q" .Port }}
)

func main() {
	ctx := context.Background()

	p, issues, err := manifest.Parse(manifestYAML)
	if err != nil {
		log.Fatalf("agent.yaml: %v", err)
	}
	for _, issue := range issues {
		if issue.Level == manifest.LevelError {
			log.Fatalf("agent.yaml: %s", issue)
		}
		log.Printf("agent.yaml: %s", issue)
	}

	layer, err := kit.BuildWith(ctx, p, kit.Options{AuditPath: auditPath(p)})
	if err != nil {
		log.Fatalf("build agent: %v", err)
	}
	defer func() {
		if err := layer.Close(ctx); err != nil {
			log.Printf("close: %v", err)
		}
	}()

	cfg := layer.LauncherConfig()
	if p.Spec.Runtime.Server.Auth == "bearer" {
		token := os.Getenv("ADKS_TOKEN")
		if token == "" {
			log.Fatal("runtime.server.auth is bearer but ADKS_TOKEN is empty")
		}
		cfg.Authenticator = authn.NewCustom(func(r *http.Request) (*authn.Caller, error) {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if got != token {
				return nil, authn.ErrUnauthenticated
			}
			return &authn.Caller{UserID: "bearer"}, nil
		})
	}

	args := os.Args[1:]
	if len(args) == 0 {
		args = defaultArgs()
	}

	l := full.NewLauncher()
	if err := l.Execute(ctx, cfg, args); err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		log.Fatalf("run: %v\n\n%s", err, l.CommandLineSyntax())
	}
}

// defaultArgs turns the manifest's server section into launcher arguments.
//
// full.NewLauncher() is a universal launcher whose only top-level keywords are
// "console" and "web". "api" and "webui" are web sublaunchers: they have to
// come after web's own flags, and there is no -host flag at all.
func defaultArgs() []string {
	switch mode {
	case "console":
		return []string{"console"}
	case "api":
		return []string{"web", "-port", port, "api"}
	default: // full | web
		return []string{"web", "-port", port, "webui", "api"}
	}
}

// auditPath keeps the audit log next to the working directory unless the
// operator points ADKS_AUDIT somewhere else. An empty value disables it.
func auditPath(p *manifest.Project) string {
	if v, ok := os.LookupEnv("ADKS_AUDIT"); ok {
		return v
	}
	return "./.adks/audit.jsonl"
}
`))

var readmeTmpl = template.Must(template.New("readme").Parse(`# {{ .Name }}

Generated by adks {{ .Version }}. The agent is described entirely by
` + "`agent.yaml`" + `; this README explains how to run it and what to change.

## What it is

| | |
|---|---|
| Agent | ` + "`{{ .Agent }}`" + ` |
| Model | ` + "`{{ .Provider }}/{{ .Model }}`" + ` |
| Credential | ` + "`${{ .APIKeyEnv }}`" + ` |
| Tools | {{ .Tools }} |
| Session store | ` + "`{{ .Session }}`" + ` |
| Artifacts | ` + "`{{ .Artifact }}`" + ` |
| Server | ` + "`{{ .Mode }}`" + ` on ` + "`{{ .Host }}:{{ .Port }}`" + ` |

## Run

` + "```bash" + `
export {{ .APIKeyEnv }}=your-key-here
go run .                           # uses the mode configured in agent.yaml
go run . web -port 8080 webui api  # full web: chat UI + REST API
go run . web -port 8080 api        # REST API only
go run . console                   # terminal chat, no browser
` + "```" + `

The manifest is embedded in the binary with ` + "`go:embed`" + `, so the agent behaves
identically wherever the binary runs. Editing ` + "`agent.yaml`" + ` requires a rebuild.

## Layout

| Path | Meaning |
|---|---|
| ` + "`agent.yaml`" + ` | The agent definition. This is the file to edit. |
| ` + "`kit/`" + ` | Compiler: manifest to ADK objects. Copied verbatim from the builder. |
| ` + "`manifest/`" + ` | Manifest types and validation. Also copied verbatim. |
| ` + "`.adks/`" + ` | Runtime state: sessions, artifacts, audit log. Git-ignored. |

` + "`kit`" + ` and ` + "`manifest`" + ` are copies, not dependencies. They are the same code the
builder ran when you clicked "try", which is what makes the trial and the
deployment the same program. Edit them if you need behaviour the manifest cannot
express, but be aware a re-export overwrites them.

## Environment

| Variable | Purpose |
|---|---|
| ` + "`{{ .APIKeyEnv }}`" + ` | Model credential. Required. |
| ` + "`ADKS_AUDIT`" + ` | Audit log path. Default ` + "`./.adks/audit.jsonl`" + `; empty disables. |
| ` + "`ADKS_TOKEN`" + ` | Bearer token, only when ` + "`runtime.server.auth: bearer`" + `. |

Tool credentials referenced by ` + "`auth.secretEnv`" + ` or MCP ` + "`${VAR}`" + ` headers are read
from the environment at start-up. They are never written into ` + "`agent.yaml`" + `.
`))
