// Package verify runs the deterministic checks that decide whether a project is
// safe to export.
//
// Nothing here asks a model to grade anything. Compilation, vetting and starting
// the binary are pass/fail facts, and a model's opinion of its own output is not
// evidence.
package verify

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Check is one verifier result.
type Check struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"` // build | vet | smoke
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	DurMs  int64  `json:"durMs"`
}

// Options tunes a verification run.
type Options struct {
	// SkipSmoke omits the run check, which is the only one that needs a model
	// credential.
	SkipSmoke bool
	// Timeout bounds the whole run.
	Timeout time.Duration
}

// Run builds, vets and starts the project at dir.
func Run(ctx context.Context, dir string, opts Options) ([]Check, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = 4 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		return nil, fmt.Errorf("verify: %s does not look like a Go module: %w", dir, err)
	}

	var checks []Check
	build := runGo(ctx, dir, "build", "-mod=mod", "./...")
	checks = append(checks, build)
	if !build.OK {
		// vet and smoke would only repeat the same compile error.
		return checks, nil
	}
	checks = append(checks, runGo(ctx, dir, "vet", "-mod=mod", "./..."))
	if !opts.SkipSmoke {
		checks = append(checks, smoke(ctx, dir))
	}
	return checks, nil
}

func runGo(ctx context.Context, dir string, args ...string) Check {
	name := "go " + strings.Join(args, " ")
	start := time.Now()
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=", "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	detail := strings.TrimSpace(string(out))
	if err != nil {
		if detail == "" {
			detail = err.Error()
		}
		return Check{Name: name, Kind: kindOf(args), OK: false, Detail: clip(detail), DurMs: time.Since(start).Milliseconds()}
	}
	if detail == "" {
		detail = "ok"
	}
	return Check{Name: name, Kind: kindOf(args), OK: true, Detail: clip(detail), DurMs: time.Since(start).Milliseconds()}
}

func kindOf(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return "go"
}

// smoke builds the binary, starts it on a free loopback port and waits for the
// health endpoint.
//
// This is the only check that exercises the whole chain: manifest parse, kit
// build (model, tools, services, plugins) and server start. It stops before any
// model call, so it costs nothing and needs no prompt.
func smoke(ctx context.Context, dir string) Check {
	start := time.Now()
	fail := func(detail string) Check {
		return Check{Name: "smoke run", Kind: "smoke", OK: false, Detail: clip(detail), DurMs: time.Since(start).Milliseconds()}
	}

	bin := filepath.Join(dir, ".adks-smoke")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	defer os.Remove(bin)

	build := exec.CommandContext(ctx, "go", "build", "-mod=mod", "-o", bin, ".")
	build.Dir = dir
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		return fail("build binary: " + strings.TrimSpace(string(out)))
	}

	port, err := freePort()
	if err != nil {
		return fail(err.Error())
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Only "console" and "web" are top-level keywords of full.NewLauncher();
	// "api" is a web sublauncher and must follow the web flags. /health is
	// registered by web itself, so the probe works with or without it.
	cmd := exec.CommandContext(runCtx, bin, "web", "-port", fmt.Sprint(port), "api")
	cmd.Dir = dir
	cmd.Env = os.Environ()
	var stderr strings.Builder
	cmd.Stderr = &stderr
	cmd.Stdout = &stderr
	if err := cmd.Start(); err != nil {
		return fail(err.Error())
	}

	// One goroutine owns Wait; the checks below watch its channel. Calling Wait
	// twice would leak the process, and polling without it made a start-up failure
	// take the full 20s timeout before reporting a one-line cause.
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(exited)
	}()
	defer func() {
		cancel()
		select {
		case <-exited:
		default:
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			// A grandchild that inherited the pipes can keep cmd.Wait blocked
			// after Kill; never let the verify request hang on it.
			select {
			case <-exited:
			case <-time.After(3 * time.Second):
			case <-ctx.Done():
			}
		}
	}()

	url := fmt.Sprintf("http://127.0.0.1:%d/health", port)
	client := &http.Client{Timeout: 2 * time.Second}
	// The wait is bounded by whichever is shorter: the caller's deadline or the
	// start-up budget. A hardcoded 20s used to outlive a tighter Options.Timeout,
	// so a caller who asked for 5s still waited 20.
	startupBudget := 20 * time.Second
	if dl, ok := ctx.Deadline(); ok {
		if remaining := time.Until(dl); remaining < startupBudget {
			startupBudget = remaining
		}
	}
	deadline := time.Now().Add(startupBudget)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			detail := strings.TrimSpace(stderr.String())
			if detail == "" {
				detail = "the binary exited before serving"
			}
			return fail("process exited during start-up: " + detail)
		default:
		}
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return Check{
					Name: "smoke run", Kind: "smoke", OK: true,
					Detail: fmt.Sprintf("GET /health on port %d", port),
					DurMs:  time.Since(start).Milliseconds(),
				}
			}
		}
		select {
		case <-ctx.Done():
			return fail("timed out")
		case <-time.After(300 * time.Millisecond):
		}
	}
	return fail("server did not answer /health within 20s: " + strings.TrimSpace(stderr.String()))
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("find free port: %w", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// clip keeps a compiler error readable in a UI panel. The first lines carry the
// file and message; the tail is usually repeated context.
func clip(s string) string {
	const max = 4000
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n…(truncated)"
}
