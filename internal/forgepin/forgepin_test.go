// Copyright (c) 2025 Reliant Labs
package forgepin

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	forgecli "github.com/reliant-labs/forge/cli"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fakeSource(t *testing.T, key string, runs *[]string) *Source {
	t.Helper()
	cache := t.TempDir()
	return &Source{
		Resolve: func(context.Context, string, string, io.Writer) (forgecli.PinnedForge, bool, error) {
			return forgecli.PinnedForge{Path: "/pinned/forge", Key: key, Want: "forge_version v1.0.4"}, true, nil
		},
		Run: func(_, _ string, args ...string) ([]byte, error) {
			*runs = append(*runs, strings.Join(args, " "))
			if args[len(args)-1] == "--new-flag" {
				return nil, errors.New(`unknown flag: --new-flag`)
			}
			return []byte("out of " + strings.Join(args, " ")), nil
		},
		CacheDir: func() string { return cache },
	}
}

// The first command line the pinned forge accepts answers; the answer is kept
// in memory and, for a module version, on disk for the next process.
func TestOutput_FirstAcceptedTryIsCachedPerVersion(t *testing.T) {
	var runs []string
	s := fakeSource(t, "pin:v1.0.4", &runs)
	pf, ok, err := s.Pinned(t.TempDir(), "skill load")
	require.NoError(t, err)
	require.True(t, ok)

	out, err := s.Output(pf, "", "x.json", []string{"a", "--new-flag"}, []string{"a"})
	require.NoError(t, err)
	assert.Equal(t, "out of a", string(out))
	_, _ = s.Output(pf, "", "x.json", []string{"a"})
	next := &Source{Run: s.Run, CacheDir: s.CacheDir}
	again, err := next.Output(pf, "", "x.json", []string{"a"})
	require.NoError(t, err)
	assert.Equal(t, "out of a", string(again))
	assert.Equal(t, []string{"a --new-flag", "a"}, runs, "asked again for what the pinned forge already said")
}

// A linked checkout changes with every edit: its output is not kept on disk.
func TestOutput_ACheckoutIsNotCachedOnDisk(t *testing.T) {
	var runs []string
	s := fakeSource(t, "checkout:/src/forge@abc", &runs)
	pf, _, _ := s.Pinned(t.TempDir(), "skill load")
	_, err := s.Output(pf, "", "x.json", []string{"a"})
	require.NoError(t, err)
	entries, _ := os.ReadDir(s.CacheDir())
	assert.Empty(t, entries)
}

// One resolution per project and command is reused while forge.yaml stays.
func TestPinned_IsReusedWhileForgeYAMLIsUnchanged(t *testing.T) {
	calls := 0
	s := &Source{Resolve: func(context.Context, string, string, io.Writer) (forgecli.PinnedForge, bool, error) {
		calls++
		return forgecli.PinnedForge{}, false, errors.New("no such host")
	}}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("forge_version: v1.0.4\n"), 0o644))
	for range 3 {
		_, _, err := s.Pinned(dir, "project memory")
		require.Error(t, err)
	}
	assert.Equal(t, 1, calls, "a failure is reused too, so an unobtainable pin is not rebuilt per call")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "forge.yaml"), []byte("forge_version: v1.0.5\n"), 0o644))
	_, _, _ = s.Pinned(dir, "project memory")
	assert.Equal(t, 2, calls, "a moved pin was not resolved again")
}

// The pinned forge names itself as an agent types it, so what it prints says
// `reliant forge …` as the embedded forge's output does, and no hand-off
// marker reaches it.
func TestExecForge_NamesItselfAsReliantForge(t *testing.T) {
	t.Setenv("FORGEPIN_ARGV0_HELPER", "1")
	t.Setenv("FORGE_VERSION_DELEGATED", "1")
	out, err := execForge(os.Args[0], t.TempDir(), "-test.run=^TestArgv0Helper$")
	require.NoError(t, err)
	assert.Equal(t, CLIName(), strings.TrimSpace(string(out)))
}

// TestArgv0Helper is not a test: re-executed above, it prints its argv[0], and
// says so if a hand-off marker reached it.
func TestArgv0Helper(t *testing.T) {
	if os.Getenv("FORGEPIN_ARGV0_HELPER") != "1" {
		return
	}
	if _, ok := os.LookupEnv("FORGE_VERSION_DELEGATED"); ok {
		os.Stdout.WriteString("marker leaked\n")
	}
	os.Stdout.WriteString(os.Args[0] + "\n")
	os.Exit(0)
}
