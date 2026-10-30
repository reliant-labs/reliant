// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestForgeArgs(t *testing.T) {
	root := NewRootCmd()
	for name, tc := range map[string]struct {
		args []string
		want []string
		ok   bool
	}{
		"a forge command":            {[]string{"forge", "generate", "-C", "x"}, []string{"generate", "-C", "x"}, true},
		"bare forge":                 {[]string{"forge"}, []string{}, true},
		"after a reliant flag":       {[]string{"-v", "forge", "lint"}, []string{"lint"}, true},
		"a command forge lacks":      {[]string{"forge", "a-command-from-the-future"}, []string{"a-command-from-the-future"}, true},
		"another reliant command":    {[]string{"daemon", "status"}, nil, false},
		"forge as another's operand": {[]string{"help", "forge"}, nil, false},
		"nothing":                    {nil, nil, false},
	} {
		got, ok := forgeArgs(root, tc.args)
		if ok != tc.ok || (ok && strings.Join(got, " ") != strings.Join(tc.want, " ")) {
			t.Errorf("%s: forgeArgs(%q) = %q, %v; want %q, %v", name, tc.args, got, ok, tc.want, tc.ok)
		}
	}
}

// Only `reliant forge …` asks forge which forge runs it, with forge's own
// arguments; forge's refusal is the command's failure.
func TestHandOffToPinnedForge_AsksForgeForForgeCommandsOnly(t *testing.T) {
	var asked [][]string
	refuse := error(nil)
	prev := forgeHandOff
	t.Cleanup(func() { forgeHandOff = prev })
	forgeHandOff = func(_ context.Context, args []string, _ io.Writer) (bool, error) {
		asked = append(asked, args)
		return false, refuse
	}
	root := NewRootCmd()

	require.NoError(t, handOffToPinnedForge(root, []string{"daemon", "status"}, io.Discard))
	assert.Empty(t, asked, "a reliant command was handed to forge's pin check")

	require.NoError(t, handOffToPinnedForge(root, []string{"forge", "generate", "--no-cache"}, io.Discard))
	assert.Equal(t, [][]string{{"generate", "--no-cache"}}, asked)

	refuse = errors.New("the pinned forge could not be obtained")
	assert.ErrorIs(t, handOffToPinnedForge(root, []string{"forge", "generate"}, io.Discard), refuse)
}

// The binary end to end, as main runs it, against forge's real decision.
func TestReliantForgeRunsTheProjectsForge(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in pinned forge is a shell script")
	}
	// A pseudo-version pin: never the forge this binary embeds, and never the
	// "local forge against a release tag" pairing forge tolerates.
	const pin = "v0.1.45-0.20261012195649-377b61532c0e"

	t.Run("a project pinned to X runs X", func(t *testing.T) {
		project := forgeProject(t, pin)
		cache := t.TempDir()
		standIn := filepath.Join(cache, pin, runtime.GOOS+"-"+runtime.GOARCH, "forge")
		require.NoError(t, os.MkdirAll(filepath.Dir(standIn), 0o755))
		require.NoError(t, os.WriteFile(standIn, []byte("#!/bin/sh\n"+
			`echo "pinned forge ran: $*"`+"\n"+
			`echo "delegated=$FORGE_VERSION_DELEGATED session=${FORGE_CREDENTIAL_HELPER:+set}"`+"\n"), 0o755))

		out, code := runReliant(t, project, map[string]string{"FORGE_PINNED_CACHE": cache}, "forge", "generate", "--no-cache")
		require.Equal(t, 0, code, "output:\n%s", out)
		assert.Contains(t, out, "pinned forge ran: generate --no-cache")
		// The marker is addressed to the exec'd process alone: its pid, then the forge it is.
		assert.Regexp(t, `delegated=[0-9]+:forge_version `+regexp.QuoteMeta(pin)+` session=set`, out, "the pinned forge runs as a delegate, signed in as reliant forge is")
	})

	t.Run("with no pin the embedded forge runs, and says which", func(t *testing.T) {
		project := forgeProject(t, "")
		out, code := runReliant(t, project, map[string]string{"FORGE_PINNED_CACHE": t.TempDir()}, "forge", "skill", "list")
		require.Equal(t, 0, code, "output:\n%s", out)
		assert.Contains(t, out, "pins no forge_version — running the forge built into reliant, ")
	})

	t.Run("an unobtainable pin errors and runs nothing", func(t *testing.T) {
		testkit.Slow(t, "the failed build of the pinned forge execs `go install`")
		const missing = "v0.0.1-0.20200101000000-000000000000"
		project := forgeProject(t, missing)
		out, code := runReliant(t, project, map[string]string{
			"FORGE_PINNED_CACHE": t.TempDir(),
			// Fail the build at once, as offline does: no proxy, and forge's
			// module not private, so nothing is fetched from anywhere.
			"GOPROXY": "off", "GOPRIVATE": "example.invalid", "GONOSUMDB": "example.invalid",
			"GOFLAGS": "", "GOTOOLCHAIN": "local",
		}, "forge", "generate")
		assert.NotEqual(t, 0, code, "output:\n%s", out)
		for _, want := range []string{missing, "could not be obtained", "Nothing ran"} {
			assert.Contains(t, out, want)
		}
		entries, err := os.ReadDir(project)
		require.NoError(t, err)
		assert.Len(t, entries, 1, "the embedded forge ran generate anyway: %v", entries)
	})
}

// forgeProject is a directory whose forge.yaml pins pin ("" for no pin).
func forgeProject(t *testing.T, pin string) string {
	t.Helper()
	dir := t.TempDir()
	yaml := "name: demo\nmodule_path: example.com/demo\n"
	if pin != "" {
		yaml += "forge_version: " + pin + "\n"
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte(yaml), 0o644))
	return dir
}

// runReliant runs Execute on args in dir, as the binary would, and returns its
// combined output and exit code.
func runReliant(t *testing.T, dir string, env map[string]string, args ...string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	run := exec.CommandContext(ctx, os.Args[0], append([]string{"-test.run=^TestExecuteAsHelperProcess$", "--", executeHelperMarker}, args...)...)
	run.Dir = dir
	// GOWORK off: a workspace in this process's environment would read to
	// forge as the project linking a forge checkout, which it then builds.
	set := map[string]string{"GOWORK": "off"}
	for k, v := range env {
		set[k] = v
	}
	run.Env = environWith(set, "FORGE_ALLOW_VERSION_SKEW", "FORGE_VERSION_DELEGATED", "FORGE_CREDENTIAL_HELPER")
	out, err := run.CombinedOutput()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return string(out), exit.ExitCode()
	}
	require.NoError(t, err, "output:\n%s", out)
	return string(out), 0
}
