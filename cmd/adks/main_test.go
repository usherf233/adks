package main

import (
	"strings"
	"testing"
)

// TestServeRefusesExposureWithoutToken exercises the wiring, not just the pure
// gate: serve must reach the refusal before it binds a socket. The check runs
// with a real flag set and a throwaway home directory, so a refactor that moves
// the guard after ListenAndServe is caught here.
func TestServeRefusesExposureWithoutToken(t *testing.T) {
	t.Setenv("ADKS_TOKEN", "")
	err := serve([]string{"-host", "0.0.0.0", "-port", "0", "-home", t.TempDir()})
	if err == nil {
		t.Fatal("serve bound 0.0.0.0 with no token")
	}
	if !strings.Contains(err.Error(), "ADKS_TOKEN") {
		t.Fatalf("refusal does not name the missing token: %v", err)
	}
}

// TestServeRejectsUnknownFlag keeps a typo from silently starting a server on
// the default port with the operator's intended settings ignored.
func TestServeRejectsUnknownFlag(t *testing.T) {
	if err := serve([]string{"-prot", "9000"}); err == nil {
		t.Fatal("serve accepted an unknown flag")
	}
}

// TestNormalizeHost pins the one input that flips the builder from "reachable by
// me" to "reachable by the network": an empty host becomes ":port" once it goes
// through net.JoinHostPort.
func TestNormalizeHost(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "127.0.0.1"},
		{"127.0.0.1", "127.0.0.1"},
		{"0.0.0.0", "0.0.0.0"},
		{"::1", "::1"},
		{"localhost", "localhost"},
	}
	for _, tc := range cases {
		if got := normalizeHost(tc.in); got != tc.want {
			t.Errorf("normalizeHost(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestRequireTokenForExposure is the security assertion of the whole binary: a
// non-loopback bind without a token would hand the network an API that writes
// files and spends model credits on the operator's key.
func TestRequireTokenForExposure(t *testing.T) {
	cases := []struct {
		name    string
		host    string
		token   string
		wantErr bool
	}{
		{"loopback without token", "127.0.0.1", "", false},
		{"ipv6 loopback without token", "::1", "", false},
		{"localhost without token", "localhost", "", false},
		{"all interfaces without token", "0.0.0.0", "", true},
		{"private address without token", "192.168.1.7", "", true},
		{"public address without token", "203.0.113.9", "", true},
		{"all interfaces with token", "0.0.0.0", "s3cret", false},
		{"private address with token", "192.168.1.7", "s3cret", false},
		{"loopback with token", "127.0.0.1", "s3cret", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := requireTokenForExposure(tc.host, tc.token)
			if tc.wantErr && err == nil {
				t.Fatalf("host %q with no token must be refused", tc.host)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("host %q should be allowed: %v", tc.host, err)
			}
		})
	}
}

// TestIsLoopback covers the spellings an operator actually types. "0.0.0.0" and
// "::" are deliberately not loopback: they are the listen-everything addresses.
func TestIsLoopback(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.0.0.53", true},
		{"::1", true},
		{"localhost", true},
		{"0.0.0.0", false},
		{"::", false},
		{"example.com", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isLoopback(tc.host); got != tc.want {
			t.Errorf("isLoopback(%q) = %v, want %v", tc.host, got, tc.want)
		}
	}
}
