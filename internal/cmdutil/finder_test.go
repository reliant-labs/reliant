// Copyright (c) 2025 Reliant Labs
package cmdutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// A command name comes from configuration (an MCP server's `command`), and the
// login-shell fallback interpolates it into a shell command line. A name that
// carries shell syntax must never reach that shell: it is looked up literally
// or not at all.
func TestFind_NeverRunsShellSyntaxInTheName(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the shell fallback is the POSIX login shell")
	}
	marker := filepath.Join(t.TempDir(), "pwned")
	for _, name := range []string{
		"nonexistent-cmd; touch " + marker,
		"nonexistent-cmd$(touch " + marker + ")",
		"nonexistent-cmd`touch " + marker + "`",
		"nonexistent-cmd && touch " + marker,
		"nonexistent-cmd | touch " + marker,
	} {
		finder := &CommandFinder{CommandName: name}
		if got, err := finder.Find(); err == nil {
			t.Errorf("Find(%q) = %q, want not found", name, got)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Fatalf("Find(%q) executed the shell syntax in the command name", name)
		}
	}
}

func TestIsPlainCommandName(t *testing.T) {
	for name, want := range map[string]bool{
		"npx":           true,
		"uvx":           true,
		"python3.12":    true,
		"g++":           true,
		"mcp-server_x":  true,
		"node@20":       true,
		"":              false,
		"-c":            false,
		"a b":           false,
		"a;b":           false,
		"$(id)":         false,
		"${HOME}/x":     false,
		"/usr/bin/node": false,
		"a'b":           false,
	} {
		if got := isPlainCommandName(name); got != want {
			t.Errorf("isPlainCommandName(%q) = %v, want %v", name, got, want)
		}
	}
}
