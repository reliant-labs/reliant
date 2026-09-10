// Copyright (c) 2025 Reliant Labs
package tools

import (
	"runtime"
	"strings"
	"testing"
)

// The whole point of this change: the description is chosen by the platform it
// is HANDED, not by the platform this test process happens to be running on.
// This test is meaningful precisely because it runs on a non-Windows machine
// (CI, and every dev box here) and still demands PowerShell guidance.
func TestShellDescriptionWindowsFromNonWindowsHost(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("the interesting case is a Windows daemon described from a NON-Windows host")
	}

	desc := shellDescription(ShellPlatformWindows)

	for _, want := range []string{
		"PowerShell",
		"powershell -NoProfile -NonInteractive -Command",
		"WINDOWS",
	} {
		if !strings.Contains(desc, want) {
			t.Errorf("windows description missing %q\ngot:\n%s", want, desc)
		}
	}
	if strings.Contains(desc, "Uses bash -c") {
		t.Error("windows description claims bash -c; that is the bug this fixes")
	}
}

func TestShellDescriptionUnix(t *testing.T) {
	t.Parallel()
	desc := shellDescription(ShellPlatformUnix)
	if !strings.Contains(desc, "Uses bash -c") {
		t.Errorf("unix description missing bash guidance\ngot:\n%s", desc)
	}
	if strings.Contains(desc, "PowerShell") {
		t.Error("unix description mentions PowerShell")
	}
}

// An unresolved platform must not silently masquerade as bash. It should admit
// the uncertainty and tell the model to probe — a confidently wrong dialect is
// worse than an honest unknown, because the model commits to it.
func TestShellDescriptionUnknownDoesNotAssumeBash(t *testing.T) {
	t.Parallel()
	desc := shellDescription(ShellPlatformUnknown)

	if strings.Contains(desc, "Uses bash -c") {
		t.Error("unknown platform silently assumed bash")
	}
	for _, want := range []string{"NOT KNOWN", "uname -s", "PSVersionTable"} {
		if !strings.Contains(desc, want) {
			t.Errorf("unknown description missing probe guidance %q\ngot:\n%s", want, desc)
		}
	}
}

// Every description, whatever the platform, keeps the shared body.
func TestShellDescriptionAlwaysCarriesCommonGuidance(t *testing.T) {
	t.Parallel()
	for _, p := range []ShellPlatform{ShellPlatformUnix, ShellPlatformWindows, ShellPlatformUnknown} {
		if !strings.Contains(shellDescription(p), "WHEN NOT TO USE THIS TOOL") {
			t.Errorf("platform %q lost the common description body", p)
		}
	}
}

func TestShellPlatformFromGOOS(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		goos string
		want ShellPlatform
	}{
		{"windows", ShellPlatformWindows},
		{"linux", ShellPlatformUnix},
		{"darwin", ShellPlatformUnix},
		{"freebsd", ShellPlatformUnix},
		// Unrecognized and absent values degrade to unknown rather than being
		// lumped in with "not windows, therefore bash".
		{"", ShellPlatformUnknown},
		{"plan9", ShellPlatformUnknown},
		{"Windows", ShellPlatformUnknown},
		{"garbage", ShellPlatformUnknown},
	} {
		if got := ShellPlatformFromGOOS(tc.goos); got != tc.want {
			t.Errorf("ShellPlatformFromGOOS(%q) = %q, want %q", tc.goos, got, tc.want)
		}
	}
}

// The NAME is stable across platforms. A per-OS name would change underneath a
// chat that migrates between daemons and break persisted tool_calls rows.
func TestShellToolNameIsPortableAndStable(t *testing.T) {
	t.Parallel()
	if ShellToolName != "shell" {
		t.Fatalf("ShellToolName = %q, want %q", ShellToolName, "shell")
	}
	for _, p := range []ShellPlatform{ShellPlatformUnix, ShellPlatformWindows, ShellPlatformUnknown} {
		if got := NewShellTool(p).Name(); got != ShellToolName {
			t.Errorf("platform %q changed the tool name to %q", p, got)
		}
	}
}

// The factory is the seam that carries daemon platform to tool construction.
// This pins that a Windows platform set on the factory actually reaches the
// description of the tool the registry builds.
func TestFactoryCarriesShellPlatformIntoDescription(t *testing.T) {
	t.Parallel()
	f := NewToolsFactory(&ToolsOptions{}).WithShellPlatform(ShellPlatformWindows)

	if !strings.Contains(f.Shell().Description(), "PowerShell") {
		t.Error("factory did not carry ShellPlatformWindows into the shell description")
	}

	// A factory with no platform set must degrade to unknown, never to the
	// server's own OS.
	if got := NewToolsFactory(&ToolsOptions{}).ShellPlatform(); got != ShellPlatformUnknown {
		t.Errorf("default factory platform = %q, want unknown", got)
	}
}

// The With* clones previously enumerated fields by hand, which silently drops
// any option added later. A dropped ShellPlatform reintroduces the original bug
// with no compile error, so pin that the clones preserve it.
func TestFactoryClonesPreserveShellPlatform(t *testing.T) {
	t.Parallel()
	base := NewToolsFactory(&ToolsOptions{}).WithShellPlatform(ShellPlatformWindows)

	if got := base.WithMCPProjectPath("/tmp/project").ShellPlatform(); got != ShellPlatformWindows {
		t.Errorf("WithMCPProjectPath dropped shell platform: got %q", got)
	}
	if got := base.WithSkills(nil).ShellPlatform(); got != ShellPlatformWindows {
		t.Errorf("WithSkills dropped shell platform: got %q", got)
	}
}
