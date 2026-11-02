package cli

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/forgedelegate"
)

// handOffForTest runs the entrypoint's decision on args, restoring the state
// HandOffToPinned leaves behind for the next test.
func handOffForTest(t *testing.T, args ...string) (bool, string, error) {
	t.Helper()
	prev := handOffDecided
	defer func() { handOffDecided = prev }()
	var out strings.Builder
	handed, err := HandOffToPinned(context.Background(), args, &out)
	return handed, out.String(), err
}

// embeddedIn makes this forge look compiled into host at version.
func embeddedIn(t *testing.T, host, version string) {
	t.Helper()
	prev := forgeEmbedding
	t.Cleanup(func() { forgeEmbedding = prev })
	forgeEmbedding = func() (string, string) { return host, version }
}

// localBuild says whether this forge is built from a local tree.
func localBuild(t *testing.T, local bool) {
	t.Helper()
	prev := forgeIsLocalBuild
	t.Cleanup(func() { forgeIsLocalBuild = prev })
	forgeIsLocalBuild = func() bool { return local }
}

// A project pinned to X runs X: the command, its arguments untouched, goes to
// the pinned forge before this forge's command tree runs any of it.
func TestHandOffToPinned_APinnedProjectRunsThePinnedForge(t *testing.T) {
	skewedProject(t)
	t.Setenv(forgedelegate.MarkerEnv, "")
	h, installs := armDelegation(t)

	handed, out, err := handOffForTest(t, "generate", "--no-cache")
	if err != nil || !handed {
		t.Fatalf("handed %v, err %v", handed, err)
	}
	if want := "github.com/reliant-labs/forge/cmd/forge@" + skewTestPin; len(*installs) != 1 || (*installs)[0] != want {
		t.Fatalf("built %v, want [%s]", *installs, want)
	}
	if strings.Join(h.args, " ") != "generate --no-cache" {
		t.Errorf("args %v, want the original invocation", h.args)
	}
	if !strings.Contains(out, skewTestPin) {
		t.Errorf("the notice does not name the pin:\n%s", out)
	}
}

// Forge report 86c6fd64, the incident: reliant embedded forge at a pseudo-version, the
// project pinned a tag, and `reliant forge generate` ran the embedded forge
// with a warning — the pairing passed for "a development forge against a tag
// pin". A forge that is a module version is handed over like any other; only
// a local build keeps the tolerance.
func TestHandOff_AnEmbeddedPseudoVersionAgainstATagPinIsHandedOver(t *testing.T) {
	dir := t.TempDir()
	writeForgeYAML(t, dir, "name: control-plane\nmodule_path: github.com/example/cp\nforge_version: v1.0.4\n")
	t.Chdir(dir)
	prevVersion, prevCommit := buildinfo.Version(), buildinfo.GitCommit()
	buildinfo.Set("v0.1.47-0.20261027041513-dd9a973c5fbf", "", "")
	t.Cleanup(func() { buildinfo.Set(prevVersion, "", prevCommit) })
	t.Setenv(forgedelegate.MarkerEnv, "")

	t.Run("module build", func(t *testing.T) {
		localBuild(t, false)
		for _, args := range [][]string{{"generate"}, {"skill", "load", "forge"}} {
			h, installs := armDelegation(t)
			handed, _, err := handOffForTest(t, args...)
			if err != nil || !handed || h.path == "" {
				t.Errorf("forge %v ran under the embedded forge: handed %v, err %v", args, handed, err)
			}
			if len(*installs) != 1 || !strings.HasSuffix((*installs)[0], "@v1.0.4") {
				t.Errorf("forge %v built %v, want the v1.0.4 pin", args, *installs)
			}
			h2, _ := armDelegation(t)
			if _, err := runCheck(t, args...); err != nil || h2.path == "" {
				t.Errorf("forge %v, through the root's own check: err %v, handed %q", args, err, h2.path)
			}
		}
	})
	t.Run("local build", func(t *testing.T) {
		localBuild(t, true)
		h, installs := armDelegation(t)
		if handed, _, err := handOffForTest(t, "generate"); err != nil || handed || len(*installs) != 0 {
			t.Errorf("a checkout build against a tag pin was handed over: handed %v, err %v, built %v", handed, err, *installs)
		}
		if handed, _, err := handOffForTest(t, "build"); err != nil || !handed || h.path == "" {
			t.Errorf("an artifact command under a checkout build stayed here: handed %v, err %v", handed, err)
		}
	})
}

// A command, subcommand or flag the pinned forge has and this one lacks still
// reaches the pinned forge: the decision is made before this forge's tree can
// reject it.
func TestHandOffToPinned_WhatThisForgeLacksReachesThePin(t *testing.T) {
	skewedProject(t)
	t.Setenv(forgedelegate.MarkerEnv, "")
	for _, args := range [][]string{
		{"a-command-from-the-future", "--with", "flags"},
		{"project", "a-subcommand-from-the-future"},
		{"generate", "--a-flag-from-the-future"},
	} {
		h, _ := armDelegation(t)
		handed, _, err := handOffForTest(t, args...)
		if err != nil || !handed {
			t.Errorf("forge %v: handed %v, err %v", args, handed, err)
		}
		if strings.Join(h.args, " ") != strings.Join(args, " ") {
			t.Errorf("forge %v: handed over as %v", args, h.args)
		}
	}
}

// -C names the project, so it names the pin.
func TestHandOffToPinned_ProjectDirChoosesThePin(t *testing.T) {
	skewedProject(t)
	project, _ := os.Getwd()
	t.Chdir(t.TempDir())
	t.Setenv(forgedelegate.MarkerEnv, "")

	h, _ := armDelegation(t)
	if handed, _, err := handOffForTest(t, "generate"); err != nil || handed || h.path != "" {
		t.Fatalf("outside the project: handed %v, err %v", handed, err)
	}
	for _, args := range [][]string{{"generate", "-C", project}, {"--project-dir=" + project, "generate"}, {"a-command-from-the-future", "-C", project}} {
		h, _ := armDelegation(t)
		if handed, _, err := handOffForTest(t, args...); err != nil || !handed || h.path == "" {
			t.Errorf("forge %v: handed %v, err %v", args, handed, err)
		}
	}
}

// With no pin the forge in hand runs — and an embedded one says which it is,
// since nothing else on screen does. A standalone forge is the binary the
// user chose, and stays quiet.
func TestHandOffToPinned_NoPinRunsThisForge(t *testing.T) {
	t.Setenv(forgedelegate.MarkerEnv, "")
	unpinned := t.TempDir()
	writeForgeYAML(t, unpinned, "name: demo\nmodule_path: github.com/example/demo\n")
	for name, dir := range map[string]string{"no project": t.TempDir(), "no forge_version": unpinned} {
		t.Run(name, func(t *testing.T) {
			t.Chdir(dir)
			h, installs := armDelegation(t)

			embeddedIn(t, "reliant", "v1.0.5")
			handed, out, err := handOffForTest(t, "skill", "list")
			if err != nil || handed || h.path != "" || len(*installs) != 0 {
				t.Fatalf("handed %v, err %v, built %v", handed, err, *installs)
			}
			if !strings.Contains(out, "running the forge built into reliant, v1.0.5") {
				t.Errorf("the embedded forge did not say which forge runs:\n%q", out)
			}
			if _, out, _ := handOffForTest(t, "version"); out != "" {
				t.Errorf("`version` already says which forge it is, and printed:\n%q", out)
			}

			embeddedIn(t, "", "")
			if _, out, _ := handOffForTest(t, "skill", "list"); out != "" {
				t.Errorf("a standalone forge printed:\n%q", out)
			}
		})
	}
}

// A pin that cannot be obtained is an error, and nothing runs — never a
// fallback to this forge.
func TestHandOffToPinned_AnUnobtainablePinErrors(t *testing.T) {
	skewedProject(t)
	t.Setenv(forgedelegate.MarkerEnv, "")
	h, _ := armDelegation(t)
	unobtainablePin(t)

	handed, _, err := handOffForTest(t, "skill", "load", "forge")
	if err == nil || handed || h.path != "" {
		t.Fatalf("handed %v, err %v: a command ran without the forge its project pins", handed, err)
	}
	for _, want := range []string{"no such host", skewTestPin, "Nothing ran", "--allow-version-skew", "project upgrade"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure does not mention %q:\n%v", want, err)
		}
	}

	// The commands that must work offline still run here.
	for _, args := range [][]string{{"version"}, {"project", "upgrade"}, {"report-bug"}, {"env", "down"}} {
		if handed, _, err := handOffForTest(t, args...); err != nil || handed {
			t.Errorf("forge %v: handed %v, err %v", args, handed, err)
		}
	}
	// So does the explicit override.
	t.Setenv(allowVersionSkewEnv, "1")
	if handed, _, err := handOffForTest(t, "generate"); err != nil || handed {
		t.Errorf("%s=1: handed %v, err %v", allowVersionSkewEnv, handed, err)
	}
}

// The entrypoint's decision is the decision: the root's own check does not
// make it a second time.
func TestHandOffToPinned_TheRootCheckDoesNotDecideAgain(t *testing.T) {
	skewedProject(t)
	t.Setenv(forgedelegate.MarkerEnv, "")
	prev := handOffDecided
	t.Cleanup(func() { handOffDecided = prev })
	t.Setenv(allowVersionSkewEnv, "1")
	if handed, err := HandOffToPinned(context.Background(), []string{"generate"}, io.Discard); handed || err != nil {
		t.Fatalf("an overridden run was handed over: %v, %v", handed, err)
	}
	t.Setenv(allowVersionSkewEnv, "")
	h, installs := armDelegation(t)
	if _, err := runCheck(t, "lint"); err != nil {
		t.Fatalf("lint: %v", err)
	}
	if h.path != "" || len(*installs) != 0 {
		t.Errorf("the root check handed over after the entrypoint decided (built %v)", *installs)
	}
}

// A host that runs the project's forge itself (reliant reading its skills)
// gets the same decision, resolved and not exec'd — and the same refusal to
// substitute this forge when the pin cannot be had.
func TestResolvePinnedForge(t *testing.T) {
	skewedProject(t)
	root, _ := os.Getwd()
	h, installs := armDelegation(t)

	got, ok, err := ResolvePinnedForge(context.Background(), root, "skill load", io.Discard)
	if err != nil || !ok || got.Path == "" {
		t.Fatalf("got %+v, ok %v, err %v", got, ok, err)
	}
	if got.Key != "pin:"+skewTestPin || !strings.Contains(got.Want, skewTestPin) {
		t.Errorf("key %q, want %q", got.Key, got.Want)
	}
	if len(*installs) != 1 || h.path != "" {
		t.Errorf("built %v, exec'd %q: resolve must build, never exec", *installs, h.path)
	}

	if _, ok, err := ResolvePinnedForge(context.Background(), t.TempDir(), "skill load", io.Discard); ok || err != nil {
		t.Errorf("outside a project: ok %v, err %v", ok, err)
	}
	if _, ok, _ := ResolvePinnedForge(context.Background(), root, "version", io.Discard); ok {
		t.Error("an exempt command resolved a pinned forge")
	}
	unobtainablePin(t)
	t.Setenv(forgedelegate.CacheDirEnv, t.TempDir())
	if _, ok, err := ResolvePinnedForge(context.Background(), root, "skill load", io.Discard); ok || err == nil || !strings.Contains(err.Error(), "could not be obtained") {
		t.Errorf("unobtainable pin: ok %v, err %v", ok, err)
	}
}
