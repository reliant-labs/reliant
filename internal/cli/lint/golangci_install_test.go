package lint

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/templates"
)

// fakeGolangciOnPath puts a golangci-lint on PATH that is not a Go binary
// and reports version ver built with goVersion when asked, as a release
// build or a wrapper script does.
func fakeGolangciOnPath(t *testing.T, ver, goVersion string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake")
	}
	bin := t.TempDir()
	script := "#!/bin/sh\necho 'golangci-lint has version " + strings.TrimPrefix(ver, "v") + " built with " + goVersion + " from abc on 2026-01-01'\n"
	if err := os.WriteFile(filepath.Join(bin, "golangci-lint"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
}

// TestGolangciLintOnPath is what `forge tools install golangci-lint` decides
// on: keep PATH's golangci-lint only when it is forge's pin built with a Go
// that can analyze the module.
func TestGolangciLintOnPath(t *testing.T) {
	pin := templates.GolangciLintVersion
	dir := moduleDir(t, "1.27")

	t.Run("missing", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		if path, why := GolangciLintOnPath(dir); path != "" || why == "" {
			t.Errorf("no golangci-lint: got path=%q why=%q", path, why)
		}
	})
	t.Run("the pin", func(t *testing.T) {
		fakeGolangciOnPath(t, pin, "go1.27.0")
		if path, why := GolangciLintOnPath(dir); path == "" || why != "" {
			t.Errorf("the pin built with go1.27.0: got path=%q why=%q", path, why)
		}
	})
	t.Run("another version", func(t *testing.T) {
		fakeGolangciOnPath(t, "v2.5.0", "go1.27.0")
		if _, why := GolangciLintOnPath(dir); !strings.Contains(why, "not forge's pin "+pin) {
			t.Errorf("v2.5.0: got why=%q", why)
		}
	})
	t.Run("built with an older Go", func(t *testing.T) {
		fakeGolangciOnPath(t, pin, "go1.26.2")
		if _, why := GolangciLintOnPath(dir); !strings.Contains(why, "go1.26.2") {
			t.Errorf("built with go1.26.2 for a go 1.27 module: got why=%q", why)
		}
	})
}
