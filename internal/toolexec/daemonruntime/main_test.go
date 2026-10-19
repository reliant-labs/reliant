// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/reliant/internal/heavyjob"
)

// Start on a managed daemon (Host.DaemonType "managed") rewrites the GLOBAL
// git config. Run inside a Reliant workspace, where a real HOME is set, this
// package's tests that start a managed daemon would otherwise point the
// developer's own ~/.gitconfig at a test binary go deletes moments later,
// breaking every later git fetch/push on the machine.
//
// So TestMain gives every test a private HOME and git config before any test
// runs. (Whether Start behaves as a pod is its StartOptions.Host, never the
// environment the test binary inherited.)

func TestMain(m *testing.M) {
	// This test binary also stands in for the reliant binary as the
	// heavy-slot wrapper (cmd_exec_heavy_test.go).
	heavyjob.Dispatch()
	os.Exit(runIsolated(m))
}

func runIsolated(m *testing.M) int {
	root, err := os.MkdirTemp("", "daemonruntime-test-home-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "daemonruntime tests: cannot create isolated home:", err)
		return 1
	}
	defer os.RemoveAll(root)

	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "daemonruntime tests:", err)
		return 1
	}
	// A real workspace's repositories live here; the managed heal scans it.
	managedProjectsDir = filepath.Join(root, "managed-projects")

	for k, v := range map[string]string{
		"HOME":                home,
		"XDG_CONFIG_HOME":     filepath.Join(home, ".config"),
		"GIT_CONFIG_GLOBAL":   filepath.Join(home, ".gitconfig"),
		"GIT_CONFIG_SYSTEM":   os.DevNull,
		"GIT_CONFIG_NOSYSTEM": "1",
	} {
		os.Setenv(k, v)
	}
	return m.Run()
}
