// Command adks is the ADK Go scaffold: a local builder for one-agent projects
// plus the exporter that turns a project into a standalone module.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	adks "github.com/usherf233/adks"
	"github.com/usherf233/adks/internal/api"
	"github.com/usherf233/adks/internal/store"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	log.SetFlags(log.Ltime)
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(os.Args[2:])
	case "version", "-v", "--version":
		fmt.Printf("adks %s\n", version)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `adks — Google ADK (Go) scaffold

Usage:
  adks serve [-host 127.0.0.1] [-port 7788] [-home DIR]
  adks version

The builder stores projects under ~/.adks (override with -home or ADKS_HOME).
Binding to a non-loopback address requires ADKS_TOKEN, because the builder has no
user accounts: a single bearer token is the only thing standing between the
network and your API keys.
`)
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	host := fs.String("host", "127.0.0.1", "address to bind")
	port := fs.Int("port", 7788, "port to bind")
	home := fs.String("home", "", "builder home directory (default ~/.adks)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// An empty host would make net.JoinHostPort produce ":7788", which listens on
	// every interface. Normalise it instead of letting it look like loopback.
	*host = normalizeHost(*host)

	root := *home
	if root == "" {
		root = os.Getenv("ADKS_HOME")
	}
	if root == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("locate home directory: %w", err)
		}
		root = filepath.Join(userHome, ".adks")
	}

	st, err := store.New(root)
	if err != nil {
		return err
	}

	token := os.Getenv("ADKS_TOKEN")
	if err := requireTokenForExposure(*host, token); err != nil {
		return err
	}

	srv := api.New(st, adks.FS, version, token)
	defer srv.Close()

	addr := net.JoinHostPort(*host, fmt.Sprint(*port))
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// The trial run streams; a write timeout would cut a long turn.
		WriteTimeout: 0,
		IdleTimeout:  120 * time.Second,
	}

	fmt.Printf("adks %s\n", version)
	fmt.Printf("home   %s\n", st.Root())
	fmt.Printf("listen http://%s\n", addr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// normalizeHost resolves the one value net.Listen treats as "everything".
// net.JoinHostPort("", port) yields ":port", which binds every interface, so an
// empty -host would silently turn the safest default into the most exposed one.
func normalizeHost(host string) string {
	if host == "" {
		return "127.0.0.1"
	}
	return host
}

// requireTokenForExposure is the single gate between "local builder" and "an
// unauthenticated HTTP API that can write files and spend model credits".
//
// It is a pure function so the refusal itself can be tested without starting a
// server: this is the one security assertion in the binary, and it used to have
// no test at all. host must already be normalised.
func requireTokenForExposure(host, token string) error {
	if token != "" || isLoopback(host) {
		return nil
	}
	return fmt.Errorf("refusing to bind %s without ADKS_TOKEN: the builder exposes model credentials and file writes", host)
}
