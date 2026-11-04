package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/spf13/cobra"
)

func daemonResolve(t *testing.T, args ...string) (*connection, error) {
	t.Helper()
	var got *connection
	var failure error
	root := NewRootCmd()
	project, _, err := root.Find([]string{"project"})
	if err != nil {
		t.Fatal(err)
	}
	project.AddCommand(&cobra.Command{Use: "session-probe", RunE: func(c *cobra.Command, _ []string) error { got, failure = resolveConnection(c); return nil }})
	root.SetOut(&bytes.Buffer{})
	root.SetErr(&bytes.Buffer{})
	root.SetArgs(append([]string{"project", "session-probe"}, args...))
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	return got, failure
}
func TestDaemonSessionExplicitAndServerBound(t *testing.T) {
	for _, tc := range []struct {
		name    string
		flag    bool
		server  string
		sub     string
		expired bool
		want    bool
	}{
		{name: "explicit", flag: true, server: hostedServerURL, sub: "owner", want: true},
		{name: "not automatic", server: hostedServerURL, sub: "owner"},
		{name: "different server", flag: true, server: "https://another.example", sub: "owner"},
		{name: "legacy single account server authenticates identity", flag: true, server: hostedServerURL, want: true},
		{name: "expired", flag: true, server: hostedServerURL, sub: "owner", expired: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateCLI(t)
			t.Setenv("RELIANT_TOKEN", "")
			expiry := time.Now().Add(time.Hour)
			if tc.expired {
				expiry = time.Now().Add(-time.Hour)
			}
			err := auth.WriteDaemonCredentials(&auth.DaemonCredentials{ServerURL: tc.server, Sub: tc.sub, PAT: "rlat_synthetic_never_print", ExpiresAt: &expiry})
			if err != nil {
				t.Fatal(err)
			}
			var args []string
			if tc.flag {
				args = append(args, "--daemon-session")
			}
			got, err := daemonResolve(t, args...)
			if tc.want {
				if err != nil || got == nil || got.Token != "rlat_synthetic_never_print" {
					t.Fatalf("expected server-bound session: %v", err)
				}
				if strings.Contains(got.describeCredential(), "rlat_synthetic") {
					t.Fatal("credential provenance leaks token")
				}
			} else if err == nil {
				t.Fatal("unexpected daemon fallback")
			}
		})
	}
}
func TestDaemonSessionAmbiguousAccountFailsClosed(t *testing.T) {
	isolateCLI(t)
	t.Setenv("RELIANT_TOKEN", "")
	path, err := auth.DaemonCredentialsFilePath()
	if err != nil {
		t.Fatal(err)
	}
	store := map[string]any{"version": 2, "origins": map[string]any{hostedServerURL: map[string]any{"a": &auth.DaemonCredentials{ServerURL: hostedServerURL, Sub: "a", PAT: "rlat_a"}, "b": &auth.DaemonCredentials{ServerURL: hostedServerURL, Sub: "b", PAT: "rlat_b"}}}}
	data, _ := json.Marshal(store)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := daemonResolve(t, "--daemon-session"); err == nil {
		t.Fatal("ambiguous account selection accepted")
	}
}

func TestDaemonSessionMissingAndConflictingToken(t *testing.T) {
	isolateCLI(t)
	t.Setenv("RELIANT_TOKEN", "")
	if _, err := daemonResolve(t, "--daemon-session"); err == nil {
		t.Fatal("missing session accepted")
	}
	t.Setenv("RELIANT_TOKEN", "rlat_other")
	if _, err := daemonResolve(t, "--daemon-session"); err == nil {
		t.Fatal("ambiguous explicit token accepted")
	}
}
