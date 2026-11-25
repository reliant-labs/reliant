package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/pathshadow"
)

// stubForgeOnPath makes the PATH scan report a stale forge first on PATH.
func stubForgeOnPath(t *testing.T) {
	t.Helper()
	prev := forgeOnPathScan
	t.Cleanup(func() { forgeOnPathScan = prev })
	forgeOnPathScan = func(_ context.Context, running string) pathshadow.Report {
		return pathshadow.Report{Running: running, Binaries: []pathshadow.Binary{
			{Path: "/home/u/go/bin/forge", Version: "v0.0.1"},
		}}
	}
}

// runRoot runs args through the real root command and returns stdout and
// stderr apart.
func runRoot(t *testing.T, args ...string) (string, string) {
	t.Helper()
	root := newTestRootCmd(t)
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("forge %s: %v\nstderr:\n%s", strings.Join(args, " "), err, errOut.String())
	}
	return out.String(), errOut.String()
}

// TestVersionWarnsOnStderrWhenPATHHoldsAnotherForge: the warning names the
// other forge and its version on stderr; stdout, which scripts parse, keeps
// its shape (the version line first).
func TestVersionWarnsOnStderrWhenPATHHoldsAnotherForge(t *testing.T) {
	t.Setenv(pathshadow.DisableEnv, "")
	stubForgeOnPath(t)
	out, errOut := runRoot(t, "version")
	if !strings.HasPrefix(out, Name()+" version ") || strings.Contains(out, "warning") {
		t.Errorf("stdout changed shape:\n%s", out)
	}
	for _, want := range []string{"warning: PATH holds a forge", "/home/u/go/bin/forge", "v0.0.1", "first on PATH", buildinfo.Current().Version} {
		if !strings.Contains(errOut, want) {
			t.Errorf("stderr is missing %q:\n%s", want, errOut)
		}
	}
}

func TestVersionPathCheckHonoursDisableEnv(t *testing.T) {
	t.Setenv(pathshadow.DisableEnv, "1")
	stubForgeOnPath(t)
	if _, errOut := runRoot(t, "version"); errOut != "" {
		t.Errorf("%s=1 still warned:\n%s", pathshadow.DisableEnv, errOut)
	}
}

// TestStartNamesTheRunningForge: `forge start` says which forge answered,
// right under its heading, and carries the same PATH warning.
func TestStartNamesTheRunningForge(t *testing.T) {
	t.Setenv(pathshadow.DisableEnv, "")
	stubForgeOnPath(t)
	out, errOut := runRoot(t, "start")
	heading, _, _ := strings.Cut(out, "\n")
	if want := "# Reliant Forge: start here (" + Name() + " " + buildinfo.Current().Version + ")"; heading != want {
		t.Errorf("heading = %q, want %q", heading, want)
	}
	if !strings.HasPrefix(out, heading+"\n\n") || !strings.Contains(out, Name()+" start app") {
		t.Errorf("`forge start` lost the text under its heading:\n%s", out)
	}
	if !strings.Contains(errOut, "/home/u/go/bin/forge") {
		t.Errorf("`forge start` did not warn about the other forge on PATH:\n%s", errOut)
	}
}
