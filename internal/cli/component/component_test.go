package component

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// The install command documents that components land in the project's
// src/components/ui/. Auto-detect always appended that suffix; --dir was taken
// literally, so `--dir web` from a repo root dropped loose .tsx files beside
// package.json. These pin the resolution so the flag and the auto-detect path
// agree on where a frontend's components live.
// An install writes the component's sibling imports too (input imports
// ./form_field_context), and never overwrites one already there: a scaffold's
// link.tsx is framework-aware, the library's is a plain anchor.
func TestInstallWritesMissingSiblingImports(t *testing.T) {
	dir := t.TempDir()
	own := []byte("// the project's own link\n")
	if err := os.WriteFile(filepath.Join(dir, "link.tsx"), own, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newInstallCmd(nil)
	cmd.SetArgs([]string{"--dir", dir, "input", "page_header"})
	cmd.SetOut(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("install: %v", err)
	}

	for _, name := range []string{"input", "form_field_context", "page_header"} {
		if _, err := os.Stat(filepath.Join(dir, name+".tsx")); err != nil {
			t.Errorf("%s.tsx not installed: %v", name, err)
		}
	}
	got, err := os.ReadFile(filepath.Join(dir, "link.tsx"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(own) {
		t.Errorf("install overwrote the project's link.tsx:\n%s", got)
	}
}

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
