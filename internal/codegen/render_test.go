package codegen

import (
	"context"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/usherf233/adks/manifest"
)

func TestGoVersionFromMod(t *testing.T) {
	got, err := goVersionFromMod([]byte("module example.com/x\n\ngo 1.27.1\n\nrequire (\n)"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "1.27.1" {
		t.Fatalf("version = %q", got)
	}
	if _, err := goVersionFromMod([]byte("module example.com/x\n")); err == nil {
		t.Fatal("a go.mod without a go directive must not silently pick an image")
	}
}

func TestRenderDockerfileUsesGoModVersion(t *testing.T) {
	seed := os.DirFS("../..")
	mod, err := fs.ReadFile(seed, "go.mod")
	if err != nil {
		t.Fatal(err)
	}
	want, err := goVersionFromMod(mod)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Render(manifest.New("demo", "Demo"), seed)
	if err != nil {
		t.Fatal(err)
	}
	docker := string(b.Files["Dockerfile"])
	if !strings.Contains(docker, "FROM golang:"+want+" AS build") {
		t.Fatalf("Dockerfile uses a different toolchain than go.mod (%s):\n%s", want, docker)
	}
	if strings.Contains(docker, "{{GO_VERSION}}") {
		t.Fatal("Dockerfile placeholder was not substituted")
	}
}

// TestExportedModuleBuilds is the end-to-end guarantee behind "export is a
// runnable project": kit and manifest are copied verbatim, so any change to them
// must still compile in the smaller module.
func TestExportedModuleBuilds(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles a generated module")
	}
	// Deliberately serial with respect to the other build test. Both drive the
	// same Go build cache; running them concurrently measured slower (26s wall
	// against 21s), so the parallelism that looks obvious on paper is a loss here.
	seed := os.DirFS("../..")
	b, err := Render(manifest.New("demo", "Demo"), seed)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := WriteDir(b, dir); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-mod=mod", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("exported module does not build: %v\n%s", err, out)
	}
}

// TestDefaultArgsMatchesLauncherKeywords guards the generated argv on two
// counts: the pre-ADK-v2 launcher syntax (mode as a keyword) must not come back,
// and -host must be passed. Omitting -host does not fail to build, it silently
// pins every exported binary to 127.0.0.1 no matter what the manifest says.
func TestDefaultArgsMatchesLauncherKeywords(t *testing.T) {
	seed := os.DirFS("../..")
	for _, mode := range []string{"full", "web", "api", "console"} {
		p := manifest.New("demo", "Demo")
		p.Spec.Runtime.Server.Mode = mode
		p.Spec.Runtime.Server.Host = "0.0.0.0"
		b, err := Render(p, seed)
		if err != nil {
			t.Fatal(err)
		}
		main := string(b.Files["main.go"])
		if strings.Contains(main, `[]string{mode,`) {
			t.Fatalf("mode %q: defaultArgs still uses the launcher's old argv", mode)
		}
		if mode == "console" {
			continue
		}
		if !strings.Contains(main, `"-host"`) || !strings.Contains(main, `host = "0.0.0.0"`) {
			t.Fatalf("mode %q: the manifest host does not reach the launcher argv", mode)
		}
		if !strings.Contains(main, `ADKS_HOST`) {
			t.Fatalf("mode %q: no run-time escape hatch for a container bind address", mode)
		}
	}
}

// TestRenderDockerfileExposesManifestPort keeps EXPOSE in step with the port the
// binary actually binds. A fixed 8080 told container users to map a port nobody
// was listening on whenever the manifest chose another one.
func TestRenderDockerfileExposesManifestPort(t *testing.T) {
	p := manifest.New("demo", "Demo")
	p.Spec.Runtime.Server.Port = 9099
	b, err := Render(p, os.DirFS("../.."))
	if err != nil {
		t.Fatal(err)
	}
	docker := string(b.Files["Dockerfile"])
	if !strings.Contains(docker, "EXPOSE 9099") {
		t.Fatalf("EXPOSE does not follow runtime.server.port:\n%s", docker)
	}
	if strings.Contains(docker, "{{PORT}}") {
		t.Fatal("Dockerfile port placeholder was not substituted")
	}
}

// TestExportedBinaryStartsWithoutArgs is the real check behind defaultArgs:
// compile the exported module, start it with no argv, and wait for /health.
func TestExportedBinaryStartsWithoutArgs(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles and starts a generated binary")
	}
	// The placeholder credential is passed to the child process rather than
	// through t.Setenv, so this test does not mutate process-global state that
	// the parallel tests in this package would race with.

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	p := manifest.New("demo", "Demo")
	p.Spec.Runtime.Server.Port = port
	b, err := Render(p, os.DirFS("../.."))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := WriteDir(b, dir); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	bin := filepath.Join(dir, "smoke")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.CommandContext(ctx, "go", "build", "-mod=mod", "-o", bin, ".")
	build.Dir = dir
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build exported binary: %v\n%s", err, out)
	}

	run := exec.CommandContext(ctx, bin)
	run.Dir = dir
	run.Env = append(os.Environ(), "GOOGLE_API_KEY=dummy")
	var output strings.Builder
	run.Stdout = &output
	run.Stderr = &output
	if err := run.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = run.Wait() }()

	url := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(25 * time.Second)
	ok := false
	for time.Now().Before(deadline) && !ok {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			ok = resp.StatusCode == http.StatusOK
		}
		if !ok {
			time.Sleep(300 * time.Millisecond)
		}
	}
	if !ok {
		t.Fatalf("exported binary did not serve /health: %s", output.String())
	}
}
