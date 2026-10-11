// Copyright (c) 2025 Reliant Labs
package daemonruntime

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Start on a managed daemon (RELIANT_DAEMON_TYPE=managed) rewrites the GLOBAL
// git config. Run inside a Reliant workspace, where that variable and a real
// HOME are both set, this package's tests that call Start would otherwise
// point the developer's own ~/.gitconfig at a test binary go deletes moments
// later, breaking every later git fetch/push on the machine.
//
// So TestMain gives every test a private HOME and git config and strips the
// variables that make Start behave as a pod, before any test runs.

func TestMain(m *testing.M) {
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
	// What makes Start behave as a pod: the managed git wiring, and the
	// preview forwarder binding a fixed port.
	for _, k := range []string{DaemonTypeEnvVar, "DAEMON_RUNTIME_TYPE", "DAEMON_FRONTEND_PORT"} {
		os.Unsetenv(k)
	}
	return m.Run()
}
