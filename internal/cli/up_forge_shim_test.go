package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/toolcache"
)

// TestForgeShimRunsTheRunningForge pins NS FG-12: the shim a host process
// finds first on its PATH hands every argument to the command it was written
// for, quoting included, so the scaffolded .air.toml's bare `forge storage
// check ./tmp` runs the forge running the env.
func TestForgeShimRunsTheRunningForge(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the POSIX shim is exercised here; the .cmd body is pinned by TestForgeShimScriptWindows")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "argv")
	// A stand-in "forge" that records its argv, at a path with a quote in it.
	recorder := filepath.Join(dir, "it's forge")
	if err := os.WriteFile(recorder, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > "+out+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	name, body := forgeShimScript("linux", []string{recorder, "forge"})
	if name != "forge" {
		t.Fatalf("shim name = %q, want forge", name)
	}
	shim := filepath.Join(dir, "bin", name)
	if err := os.MkdirAll(filepath.Dir(shim), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(shim, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", "forge storage check './t mp'")
	cmd.Env = prependPathDir([]string{"PATH=/usr/bin:/bin"}, filepath.Dir(shim))
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("shim: %v\n%s", err, b)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if want := "forge\nstorage\ncheck\n./t mp\n"; string(got) != want {
		t.Fatalf("argv = %q, want %q", got, want)
	}
}

func TestForgeShimScriptWindows(t *testing.T) {
	name, body := forgeShimScript("windows", []string{`C:\Program Files\reliant.exe`, "forge"})
	if name != "forge.cmd" {
		t.Fatalf("name = %q", name)
	}
	if !strings.Contains(body, `"C:\Program Files\reliant.exe" "forge" %*`) {
		t.Fatalf("body = %q", body)
	}
}

func TestEnsureForgeShimWritesOnceIntoTheToolCache(t *testing.T) {
	t.Setenv(toolcache.EnvRoot, t.TempDir())
	dir, err := ensureForgeShim()
	if err != nil {
		t.Fatal(err)
	}
	name, body := forgeShimScript(runtime.GOOS, mustForgeExec(t))
	got, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil || string(got) != body {
		t.Fatalf("shim = %q, %v; want %q", got, err, body)
	}
	again, err := ensureForgeShim()
	if err != nil || again != dir {
		t.Fatalf("second ensure = %q, %v; want %q", again, err, dir)
	}
}

func mustForgeExec(t *testing.T) []string {
	t.Helper()
	tokens, err := forgeExecCommand()
	if err != nil {
		t.Fatal(err)
	}
	return tokens
}

func TestPrependPathDir(t *testing.T) {
	sep := string(os.PathListSeparator)
	got := prependPathDir([]string{"A=1", "PATH=/usr/bin"}, "/shim")
	if want := []string{"A=1", "PATH=/shim" + sep + "/usr/bin"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
	if got := prependPathDir([]string{"A=1"}, "/shim"); got[len(got)-1] != "PATH=/shim" {
		t.Fatalf("no PATH: got %v", got)
	}
}
