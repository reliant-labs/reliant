// Copyright (c) 2026 Reliant Labs
package daemonpolicy

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func childPath(env []string) string {
	path := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			path = strings.TrimPrefix(kv, "PATH=")
		}
	}
	return path
}

// `reliant machine login` links vendor CLIs into $HOME/.local/bin, which a
// cloud machine's daemon PATH lacks. Every child — first-party, a connector's,
// and a `credentials: none` one — gets it at the END of its PATH, so it never
// shadows a system binary.
func TestChildEnv_AppendsUserLocalBin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no ~/.local/bin convention on Windows")
	}
	home := t.TempDir()
	lbin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(lbin, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/local/bin:/usr/bin:/bin")
	want := "/usr/local/bin:/usr/bin:/bin:" + lbin

	confined := NewContext(context.Background(), &Policy{GrantID: "grant-1"})
	for name, env := range map[string][]string{
		"first-party":      ChildEnv(context.Background(), nil, CredentialsInherit),
		"confined":         ChildEnv(confined, nil, CredentialsInherit),
		"credentials none": ChildEnv(context.Background(), nil, CredentialsNone),
	} {
		if got := childPath(env); got != want {
			t.Errorf("%s: PATH = %q, want %q", name, got, want)
		}
	}
	if got := os.Getenv("PATH"); got != "/usr/local/bin:/usr/bin:/bin" {
		t.Errorf("the daemon's own PATH changed: %q", got)
	}
}

func TestChildEnv_UserLocalBinOnlyWhenNeeded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no ~/.local/bin convention on Windows")
	}
	home := t.TempDir()
	lbin := filepath.Join(home, ".local", "bin")
	t.Setenv("HOME", home)
	t.Setenv("PATH", "/usr/bin:/bin")

	if got := childPath(ChildEnv(context.Background(), nil, CredentialsInherit)); got != "/usr/bin:/bin" {
		t.Errorf("no ~/.local/bin directory: PATH = %q, want it unchanged", got)
	}

	if err := os.MkdirAll(lbin, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", lbin+"/:/usr/bin")
	if got := childPath(ChildEnv(context.Background(), nil, CredentialsInherit)); got != lbin+"/:/usr/bin" {
		t.Errorf("already on PATH: PATH = %q, want it unchanged", got)
	}

	t.Setenv("PATH", "/usr/bin:/bin")
	env := ChildEnv(context.Background(), map[string]string{"PATH": "/opt/only"}, CredentialsInherit)
	if got := childPath(env); got != "/opt/only" {
		t.Errorf("a request's own PATH: got %q, want exactly /opt/only", got)
	}
	if slices.ContainsFunc(env, func(kv string) bool { return strings.HasPrefix(kv, "PATH=") && strings.Contains(kv, lbin) }) {
		t.Error("a request that sets PATH must not get ~/.local/bin added")
	}
}
