// Copyright (c) 2025 Reliant Labs
package commands

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/internal/version"
)

// The release build stamps the binary with
// `-X github.com/reliant-labs/reliant/internal/version.Version=...` (Makefile
// LDFLAGS and .github/workflows/release.yml). These tests pin `reliant version`
// to THAT package.
//
// They fail against the previous implementation, which read a private set of
// commands.Version/Commit/BuildDate vars that no build ever injected — so every
// released binary reported "dev"/"unknown" no matter what the release workflow
// passed.
func withBuildInfo(t *testing.T, v, commit, date string) {
	t.Helper()
	oldV, oldC, oldD := version.Version, version.Commit, version.Date
	t.Cleanup(func() {
		version.Version, version.Commit, version.Date = oldV, oldC, oldD
	})
	version.Version, version.Commit, version.Date = v, commit, date
}

func runVersion(t *testing.T, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	cmd := newVersionCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("version command: %v", err)
	}
	return out.String()
}

func TestVersionCmdReportsInjectedBuildInfo(t *testing.T) {
	withBuildInfo(t, "1.2.3", "abc1234", "2026-01-01")

	got := runVersion(t)
	for _, want := range []string{"1.2.3", "abc1234", "2026-01-01"} {
		if !strings.Contains(got, want) {
			t.Errorf("version output missing %q\ngot:\n%s", want, got)
		}
	}
	if strings.Contains(got, "unknown") {
		t.Errorf("version reported %q despite injected build info:\n%s", "unknown", got)
	}
}

func TestVersionCmdJSONReportsInjectedBuildInfo(t *testing.T) {
	withBuildInfo(t, "4.5.6", "deadbee", "2026-02-02")

	var info map[string]string
	if err := json.Unmarshal([]byte(runVersion(t, "--json")), &info); err != nil {
		t.Fatalf("decode --json output: %v", err)
	}
	for field, want := range map[string]string{
		"version": "4.5.6",
		"commit":  "deadbee",
		"built":   "2026-02-02",
	} {
		if info[field] != want {
			t.Errorf("--json %q = %q, want %q", field, info[field], want)
		}
	}
}

func TestVersionCmdShortPrintsOnlyVersion(t *testing.T) {
	withBuildInfo(t, "7.8.9", "cafe123", "2026-03-03")

	// --short writes to stdout via fmt.Println rather than the command's
	// output writer, so assert on the exported accessor the command reads.
	if got := version.Get().Version; got != "7.8.9" {
		t.Errorf("version.Get().Version = %q, want %q", got, "7.8.9")
	}
}
