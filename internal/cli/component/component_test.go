package component

import (
	"os"
	"path/filepath"
	"testing"
)

// The install command documents that components land in the project's
// src/components/ui/. Auto-detect always appended that suffix; --dir was taken
// literally, so `--dir web` from a repo root dropped loose .tsx files beside
// package.json. These pin the resolution so the flag and the auto-detect path
// agree on where a frontend's components live.
func TestResolveInstallDir(t *testing.T) {
	frontend := t.TempDir()
	if err := os.WriteFile(filepath.Join(frontend, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(frontend, "src"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("frontend root gains the components/ui suffix", func(t *testing.T) {
		want := filepath.Join(frontend, "src", "components", "ui")
		if got := resolveInstallDir(frontend); got != want {
			t.Errorf("resolveInstallDir(%q) = %q, want %q", frontend, got, want)
		}
	})

	t.Run("a src-only frontend is still a frontend", func(t *testing.T) {
		srcOnly := t.TempDir()
		if err := os.MkdirAll(filepath.Join(srcOnly, "src"), 0o755); err != nil {
			t.Fatal(err)
		}
		want := filepath.Join(srcOnly, "src", "components", "ui")
		if got := resolveInstallDir(srcOnly); got != want {
			t.Errorf("resolveInstallDir(%q) = %q, want %q", srcOnly, got, want)
		}
	})

	t.Run("an explicit components/ui path is used verbatim", func(t *testing.T) {
		explicit := filepath.Join(frontend, "src", "components", "ui")
		if got := resolveInstallDir(explicit); got != explicit {
			t.Errorf("resolveInstallDir(%q) = %q, want it unchanged", explicit, got)
		}
	})

	t.Run("a non-frontend directory stays literal", func(t *testing.T) {
		scratch := t.TempDir()
		if got := resolveInstallDir(scratch); got != scratch {
			t.Errorf("resolveInstallDir(%q) = %q, want it unchanged", scratch, got)
		}
	})
}
