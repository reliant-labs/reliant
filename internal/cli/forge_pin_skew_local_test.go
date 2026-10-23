package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fakePinnedForge installs a stand-in for the project's .tools/bin/forge that
// reports version.
func fakePinnedForge(t *testing.T, version string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stand-in for forge")
	}
	dir := filepath.Join(".tools", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\necho 'forge version " + version + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "forge"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// 2026-10-22: `reliant forge scaffold package appdb` in control-plane was
// refused with a `go run …@<pin>` line (a module download and a build) while
// the pinned forge sat in the project's own .tools/bin/forge. When that binary
// is the pin, the remedy is to run it, with the exact arguments.
func TestPinnedRunLine_PrefersTheProjectsPinnedBinary(t *testing.T) {
	skewedProject(t)
	fakePinnedForge(t, skewTestPin)
	root, _ := os.Getwd()

	got := pinnedRunLine(root, skewTestPin, []string{"scaffold", "package", "appdb"})
	if want := filepath.Join(root, ".tools", "bin", "forge") + " scaffold package appdb"; got != want {
		t.Errorf("pinnedRunLine = %q, want %q", got, want)
	}
}

// A .tools/bin/forge that is not the pin (task tools was not re-run after a
// pin bump) is the same stale forge; naming it would send the user in a circle.
func TestPinnedRunLine_FallsBackToGoRunWhenTheLocalBinaryIsNotThePin(t *testing.T) {
	skewedProject(t)
	root, _ := os.Getwd()
	want := "go run github.com/reliant-labs/forge/cmd/forge@" + skewTestPin + " scaffold package appdb"

	if got := pinnedRunLine(root, skewTestPin, []string{"scaffold", "package", "appdb"}); got != want {
		t.Errorf("no local binary: pinnedRunLine = %q, want %q", got, want)
	}
	fakePinnedForge(t, skewTestBinary)
	if got := pinnedRunLine(root, skewTestPin, []string{"scaffold", "package", "appdb"}); got != want {
		t.Errorf("stale local binary: pinnedRunLine = %q, want %q", got, want)
	}
}

// Every message that offers the remedy offers the faster one.
func TestForgePinSkew_MessagesNameThePinnedBinary(t *testing.T) {
	skewedProject(t)
	fakePinnedForge(t, skewTestPin)
	root, _ := os.Getwd()
	local := filepath.Join(root, ".tools", "bin", "forge") + " "

	if err := guardVerdict(t, "scaffold", "package", "appdb"); err == nil || !strings.Contains(err.Error(), local) {
		t.Errorf("the pin-gate refusal does not name %s:\n%v", local, err)
	}

	var warn strings.Builder
	rootCmd := newTestRootCmd(t)
	cmd, _, _ := rootCmd.Find([]string{"lint"})
	if err := checkForgePinSkew(cmd, rootCmd, &warn, false); err != nil || !strings.Contains(warn.String(), local) {
		t.Errorf("the skew warning does not name %s (err=%v):\n%s", local, err, warn.String())
	}

	if err := binaryBehindPinErr(root, skewTestPin, skewTestBinary); !strings.Contains(err.Error(), local) {
		t.Errorf("generate's behind-pin refusal does not name %s:\n%v", local, err)
	}
}
