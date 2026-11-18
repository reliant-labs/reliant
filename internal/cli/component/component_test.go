package component

import (
	"io"
	"os"
	"path/filepath"
	"strings"
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

// writeFrontend lays out the markers the inventory scan and the install
// resolution look for: a framework config, a package.json and src/.
func writeFrontend(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "src", "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"package.json", "next.config.ts"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func writeProject(t *testing.T, frontends ...string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: demo\nmodule_path: example.com/demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/demo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, fe := range frontends {
		writeFrontend(t, filepath.Join(root, "frontends", fe))
	}
	return root
}

// Auto-detect resolves the project's frontend from wherever the command runs:
// the project root, inside the frontend, or below either. growth hit
// "could not auto-detect components directory" with exactly one frontend.
func TestDetectComponentsDir(t *testing.T) {
	t.Run("the project's only frontend, from the root and from inside it", func(t *testing.T) {
		root := writeProject(t, "web")
		want := filepath.Join(root, "frontends", "web", "src", "components", "ui")
		for _, cwd := range []string{
			root,
			filepath.Join(root, "frontends", "web"),
			filepath.Join(root, "frontends", "web", "src", "app"),
			filepath.Join(root, "frontends"),
		} {
			t.Chdir(cwd)
			got, err := detectComponentsDir()
			if err != nil {
				t.Fatalf("from %s: %v", cwd, err)
			}
			if got != want {
				t.Errorf("from %s: got %q, want %q", cwd, got, want)
			}
		}
	})

	t.Run("several frontends: the one cwd is in, else an error listing each", func(t *testing.T) {
		root := writeProject(t, "admin", "web")
		t.Chdir(filepath.Join(root, "frontends", "web", "src"))
		got, err := detectComponentsDir()
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(root, "frontends", "web", "src", "components", "ui"); got != want {
			t.Errorf("got %q, want %q", got, want)
		}

		t.Chdir(root)
		_, err = detectComponentsDir()
		if err == nil {
			t.Fatal("want an error naming both frontends, got none")
		}
		for _, want := range []string{"--dir frontends/admin", "--dir frontends/web"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error does not name %q:\n%v", want, err)
			}
		}
	})

	t.Run("a forge.yaml that does not load is reported, not treated as no frontend", func(t *testing.T) {
		root := writeProject(t, "web")
		if err := os.WriteFile(filepath.Join(root, "forge.yaml"), []byte("name: [unclosed\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Chdir(filepath.Join(root, "frontends", "web"))
		got, err := detectComponentsDir()
		if err == nil {
			t.Fatalf("want an error naming forge.yaml, got %q", got)
		}
		if !strings.Contains(err.Error(), "forge.yaml") {
			t.Errorf("error does not name forge.yaml:\n%v", err)
		}
	})

	t.Run("no project: a frontend-shaped cwd is used, anything else names --dir", func(t *testing.T) {
		fe := t.TempDir()
		writeFrontend(t, fe)
		t.Chdir(fe)
		got, err := detectComponentsDir()
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(fe, "src", "components", "ui"); got != want {
			t.Errorf("got %q, want %q", got, want)
		}

		t.Chdir(t.TempDir())
		if _, err := detectComponentsDir(); err == nil || !strings.Contains(err.Error(), "--dir") {
			t.Errorf("want an error naming --dir, got %v", err)
		}
	})
}
