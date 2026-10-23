package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/internal/doctor"
	"github.com/reliant-labs/forge/internal/hostlaunch"
)

func toolByName(t *testing.T, checks []toolCheck, name string) toolCheck {
	t.Helper()
	for _, tc := range checks {
		if tc.Name == name {
			return tc
		}
	}
	t.Fatalf("no %q in the tool list", name)
	return toolCheck{}
}

// TestToolFloorsArePerOS: the Windows-only floors (air's [build.windows],
// Task's core utils) must not warn a macOS or Linux user whose older air or
// Task works fine there.
func TestToolFloorsArePerOS(t *testing.T) {
	for _, tt := range []struct{ goos, tool, want string }{
		{"windows", "air", "1.65.3"},
		{"darwin", "air", "1.63.2"}, // entrypoint, which the scaffolded .air.toml needs
		{"linux", "air", "1.63.2"},
		{"windows", "task", "3.45.5"},
		{"darwin", "task", ""},
		{"windows", "go", "1.22.0"}, // no windows floor: the general one stands
	} {
		got := toolByName(t, defaultToolChecks(tt.goos), tt.tool).MinVersion
		if got != tt.want {
			t.Errorf("%s on %s: floor = %q, want %q", tt.tool, tt.goos, got, tt.want)
		}
	}
}

// TestOSFloorOnlyRaises: a platform floor below the general one must not
// LOWER the bar.
func TestOSFloorOnlyRaises(t *testing.T) {
	tc := toolCheck{MinVersion: "2.0.0", MinVersionByOS: map[string]string{"windows": "1.5.0"}}
	if got := tc.forOS("windows").MinVersion; got != "2.0.0" {
		t.Fatalf("floor = %q, want the higher general floor 2.0.0", got)
	}
}

// TestOldAirWarnsOnWindows is the failure the floor exists for: air v1.64.5
// cannot read [build.windows], so on Windows it would run the unix cmd. Its
// version banner also names the Go it was built with, which must not be
// mistaken for air's own version.
func TestOldAirWarnsOnWindows(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, hostlaunch.DefaultAirConfig), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	air := toolByName(t, defaultToolChecks("windows"), "air")
	lookup := func(string) (string, error) { return `C:\go\bin\air.exe`, nil }
	banner := func(context.Context, string, []string) (string, error) {
		return "\n  __    _   ___\n / /\\  | | | |_)\n/_/--\\ |_| |_| \\_ v1.64.5, built with Go go1.25.0\n", nil
	}
	got := runToolChecks(context.Background(), []toolCheck{air}, nil, dir, lookup, banner)
	if len(got) != 1 || got[0].Status != doctor.StatusWarn {
		t.Fatalf("air v1.64.5 on windows: %+v, want a warn", got)
	}

	newer := func(context.Context, string, []string) (string, error) {
		return "/_/--\\ |_| |_| \\_ v1.65.3, built with Go go1.27.0\n", nil
	}
	got = runToolChecks(context.Background(), []toolCheck{air}, nil, dir, lookup, newer)
	if got[0].Status != doctor.StatusPass {
		t.Fatalf("air v1.65.3 on windows: %+v, want pass", got)
	}
}

func TestAirRequiredOnlyWithAnAirConfig(t *testing.T) {
	dir := t.TempDir()
	if requiredForAirConfig(nil, dir) {
		t.Error("a project with no .air.toml never launches air")
	}
	if err := os.WriteFile(filepath.Join(dir, hostlaunch.DefaultAirConfig), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if !requiredForAirConfig(nil, dir) {
		t.Error("a project with .air.toml needs air")
	}
}
