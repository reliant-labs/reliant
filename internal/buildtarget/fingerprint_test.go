package buildtarget

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// writeTree creates files (slash paths relative to root) with the given
// contents.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// fixture lays out a build cwd ("app") beside a sibling checkout ("lib"),
// the shape a cross-repo ShellBuild has.
func fixture(t *testing.T) (cwd string) {
	t.Helper()
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"app/go.mod":                    "module app",
		"app/main.go":                   "package main",
		"app/cmd/tool/main.go":          "package main",
		"app/docs/readme.md":            "docs",
		"app/.git/HEAD":                 "ref: refs/heads/main",
		"app/web/node_modules/x/x.js":   "x",
		"app/web/package.json":          "{}",
		"app/bin/app":                   "binary",
		"lib/go.mod":                    "module lib",
		"lib/pkg/a.go":                  "package pkg",
		"lib/pkg/a_test.go":             "package pkg",
		"lib/.git/config":               "[core]",
		"lib/node_modules/dep/index.js": "dep",
	})
	return filepath.Join(root, "app")
}

// scopeOf is the scope a project build has: cwd, plus the directory holding
// it and its siblings.
func scopeOf(cwd string) InputScope {
	return InputScope{Cwd: cwd, Roots: []string{filepath.Dir(cwd)}}
}

func TestExpandInputs_Patterns(t *testing.T) {
	cwd := fixture(t)
	cases := []struct {
		name     string
		patterns []string
		want     []string
	}{
		{"literal file", []string{"go.mod"}, []string{"go.mod"}},
		{"literal dir selects everything beneath it", []string{"cmd"}, []string{"cmd/tool/main.go"}},
		{"single-segment star", []string{"*.go"}, []string{"main.go"}},
		{"doublestar prunes .git and node_modules", []string{"**"}, []string{
			"bin/app", "cmd/tool/main.go", "docs/readme.md", "go.mod", "main.go", "web/package.json",
		}},
		{"doublestar with suffix", []string{"**/*.go"}, []string{"cmd/tool/main.go", "main.go"}},
		{"sibling checkout via ../", []string{"../lib/**/*.go"}, []string{"../lib/pkg/a.go", "../lib/pkg/a_test.go"}},
		{"exclude applies regardless of order", []string{"!**/*_test.go", "../lib/**"}, []string{"../lib/go.mod", "../lib/pkg/a.go"}},
		{"exclude a directory", []string{"**", "!bin", "!docs/**"}, []string{
			"cmd/tool/main.go", "go.mod", "main.go", "web/package.json",
		}},
		{"a literal reaches into a pruned dir", []string{"web/node_modules/x"}, []string{"web/node_modules/x/x.js"}},
		{"dot is the whole cwd", []string{"."}, []string{
			"bin/app", "cmd/tool/main.go", "docs/readme.md", "go.mod", "main.go", "web/package.json",
		}},
		{"overlapping patterns select once", []string{"*.go", "main.go", "./main.go"}, []string{"main.go"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ExpandInputs(scopeOf(cwd), tc.patterns)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ExpandInputs(%q)\n got  %q\n want %q", tc.patterns, got, tc.want)
			}
		})
	}
}

func TestExpandInputs_Refusals(t *testing.T) {
	cwd := fixture(t)
	cases := []struct {
		name     string
		patterns []string
		want     string
	}{
		{"a pattern matching nothing names itself", []string{"go.mod", "missing/**"}, `"missing/**" matches no file`},
		{"absolute", []string{"/etc/passwd"}, "is absolute"},
		{"only exclusions", []string{"!bin"}, "only exclusions"},
		{"empty", []string{" "}, "is empty"},
		{"partial doublestar", []string{"src**"}, "whole path segment"},
		{"bad class", []string{"[a-"}, "syntax error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ExpandInputs(scopeOf(cwd), tc.patterns)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ExpandInputs(%q) error = %v, want one containing %q", tc.patterns, err, tc.want)
			}
		})
	}
}

func mustFingerprint(t *testing.T, cwd string, patterns []string, recipe string) Fingerprint {
	t.Helper()
	fp, err := FingerprintInputs(scopeOf(cwd), patterns, []byte(recipe))
	if err != nil {
		t.Fatal(err)
	}
	return fp
}

func TestFingerprintInputs_StableAndSensitive(t *testing.T) {
	cwd := fixture(t)
	patterns := []string{"**/*.go", "go.mod", "../lib/**"}
	base := mustFingerprint(t, cwd, patterns, "recipe")

	if again := mustFingerprint(t, cwd, patterns, "recipe"); again.Digest != base.Digest {
		t.Fatalf("same inputs, different fingerprints: %s vs %s", base.Digest, again.Digest)
	}
	reordered := []string{"../lib/**", "go.mod", "**/*.go"}
	if got := mustFingerprint(t, cwd, reordered, "recipe"); got.Digest != base.Digest {
		t.Fatal("pattern order changed the fingerprint; selection is order-independent")
	}
	if base.Files != 6 || !strings.HasPrefix(base.Digest, "sha256:") {
		t.Fatalf("fingerprint = %+v, want 6 files and a sha256 digest", base)
	}

	// Touching a file without changing its bytes is not a change: mtimes are
	// rewritten by every checkout.
	past := time.Unix(1_000_000_000, 0)
	if err := os.Chtimes(filepath.Join(cwd, "main.go"), past, past); err != nil {
		t.Fatal(err)
	}
	if got := mustFingerprint(t, cwd, patterns, "recipe"); got.Digest != base.Digest {
		t.Fatal("an mtime-only touch changed the fingerprint")
	}

	if got := mustFingerprint(t, cwd, patterns, "other recipe"); got.Digest == base.Digest {
		t.Fatal("a different recipe (command, env, platform) left the fingerprint unchanged")
	}

	// A file outside every pattern is not an input.
	writeTree(t, cwd, map[string]string{"docs/readme.md": "edited"})
	if got := mustFingerprint(t, cwd, patterns, "recipe"); got.Digest != base.Digest {
		t.Fatal("editing an undeclared file changed the fingerprint")
	}

	writeTree(t, cwd, map[string]string{"../lib/pkg/a.go": "package pkg // edited"})
	edited := mustFingerprint(t, cwd, patterns, "recipe")
	if edited.Digest == base.Digest {
		t.Fatal("editing a sibling-checkout input left the fingerprint unchanged")
	}
	if got := edited.Changed(base); !reflect.DeepEqual(got, []string{"../lib/pkg/a.go"}) {
		t.Fatalf("Changed = %q, want the edited file", got)
	}

	writeTree(t, cwd, map[string]string{"cmd/new.go": "package cmd"})
	added := mustFingerprint(t, cwd, patterns, "recipe")
	if added.Digest == edited.Digest {
		t.Fatal("adding a matching file left the fingerprint unchanged")
	}
	if got := added.Changed(edited); !reflect.DeepEqual(got, []string{"cmd/new.go"}) {
		t.Fatalf("Changed = %q, want the added file", got)
	}

	// Windows has no executable bit: chmod 0755 changes nothing there to
	// fingerprint.
	if runtime.GOOS == "windows" {
		return
	}
	if err := os.Chmod(filepath.Join(cwd, "main.go"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := mustFingerprint(t, cwd, patterns, "recipe"); got.Digest == added.Digest {
		t.Fatal("making an input executable left the fingerprint unchanged")
	}
}

// TestExpandInputs_StaysInsideTheRoots pins the reach policy: a pattern may
// leave cwd only into a declared root (the directory of sibling checkouts a
// cwd can already be), and no symlink may carry a read outside it.
func TestExpandInputs_StaysInsideTheRoots(t *testing.T) {
	cwd := fixture(t)
	root := filepath.Dir(cwd)
	outside := t.TempDir() // not beneath root
	writeTree(t, outside, map[string]string{"secret.txt": "s", "tree/x.go": "package x"})

	t.Run("a sibling inside the root is reachable", func(t *testing.T) {
		if _, err := ExpandInputs(scopeOf(cwd), []string{"../lib/go.mod"}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("traversal past the root is refused", func(t *testing.T) {
		for _, pat := range []string{"../../x/**", "../../" + filepath.Base(outside) + "/secret.txt", "../lib/../../etc"} {
			_, err := ExpandInputs(scopeOf(cwd), []string{pat})
			if err == nil || !strings.Contains(err.Error(), "outside the allowed roots") {
				t.Errorf("ExpandInputs(%q) error = %v, want a refusal naming the roots", pat, err)
			}
		}
	})
	t.Run("without roots only cwd is reachable", func(t *testing.T) {
		_, err := ExpandInputs(InputScope{Cwd: cwd}, []string{"../lib/**"})
		if err == nil || !strings.Contains(err.Error(), "outside the allowed roots") {
			t.Fatalf("error = %v, want a refusal", err)
		}
	})
	t.Run("a symlinked file pointing outside is refused", func(t *testing.T) {
		dir := filepath.Join(root, "linkfile")
		writeTree(t, dir, map[string]string{"real.go": "package l"})
		if err := os.Symlink(filepath.Join(outside, "secret.txt"), filepath.Join(dir, "secret.go")); err != nil {
			t.Fatal(err)
		}
		for _, pat := range []string{"../linkfile/**", "../linkfile/secret.go"} {
			_, err := ExpandInputs(scopeOf(cwd), []string{pat})
			if err == nil || !strings.Contains(err.Error(), "outside the allowed roots") {
				t.Errorf("ExpandInputs(%q) error = %v, want a symlink-escape refusal", pat, err)
			}
		}
		// Excluded, it is never read.
		if got, err := ExpandInputs(scopeOf(cwd), []string{"../linkfile/real.go"}); err != nil || len(got) != 1 {
			t.Fatalf("a sibling of the escaping link: %q, %v", got, err)
		}
	})
	t.Run("a symlinked directory pointing outside is refused", func(t *testing.T) {
		if err := os.Symlink(filepath.Join(outside, "tree"), filepath.Join(root, "linkdir")); err != nil {
			t.Fatal(err)
		}
		_, err := ExpandInputs(scopeOf(cwd), []string{"../linkdir/**"})
		if err == nil || !strings.Contains(err.Error(), "outside the allowed roots") {
			t.Fatalf("error = %v, want a symlink-escape refusal", err)
		}
	})
	t.Run("a symlink that stays inside the root is followed", func(t *testing.T) {
		if err := os.Symlink(filepath.Join(root, "lib"), filepath.Join(root, "liblink")); err != nil {
			t.Fatal(err)
		}
		got, err := ExpandInputs(scopeOf(cwd), []string{"../liblink/pkg/*.go"})
		want := []string{"../liblink", "../liblink/pkg/a.go", "../liblink/pkg/a_test.go"}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("ExpandInputs through an in-root link = %q, %v; want its two files and the link itself", got, err)
		}
	})
}

// TestFingerprintInputs_RetargetedSymlink: an input that is a symlink to a
// regular file is keyed on its link text as well as its target's content, so
// pointing it at another file with IDENTICAL content still changes the
// fingerprint.
func TestFingerprintInputs_RetargetedSymlink(t *testing.T) {
	cwd := fixture(t)
	writeTree(t, cwd, map[string]string{"cfg/a.yaml": "same", "cfg/b.yaml": "same"})
	link := filepath.Join(cwd, "cfg", "current.yaml")
	if err := os.Symlink("a.yaml", link); err != nil {
		t.Fatal(err)
	}
	patterns := []string{"cfg/current.yaml"}
	before := mustFingerprint(t, cwd, patterns, "recipe")

	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("b.yaml", link); err != nil {
		t.Fatal(err)
	}
	after := mustFingerprint(t, cwd, patterns, "recipe")
	if after.Digest == before.Digest {
		t.Fatal("retargeting an input symlink between equal-content files left the fingerprint unchanged")
	}
	if got := after.Changed(before); !reflect.DeepEqual(got, []string{"cfg/current.yaml"}) {
		t.Fatalf("Changed = %q, want the retargeted link", got)
	}

	// Its target's content still counts, through an unchanged link.
	writeTree(t, cwd, map[string]string{"cfg/b.yaml": "edited"})
	if edited := mustFingerprint(t, cwd, patterns, "recipe"); edited.Digest == after.Digest {
		t.Fatal("editing a symlinked input's target left the fingerprint unchanged")
	}
}

// TestFingerprintInputs_DirectorySymlinks: a broad walk descends an in-root
// directory symlink under its lexical path, so an edit reachable only through
// it changes the fingerprint; retargeting it changes it too; a cycle
// terminates; and one leading outside the roots refuses the fingerprint
// unless excluded or out of every pattern's reach.
func TestFingerprintInputs_DirectorySymlinks(t *testing.T) {
	cwd := fixture(t)
	root := filepath.Dir(cwd)
	writeTree(t, root, map[string]string{
		"shared/v1/api.go": "package api",
		"shared/v2/api.go": "package api",
	})
	if err := os.Symlink(filepath.Join(root, "shared", "v1"), filepath.Join(cwd, "vendored")); err != nil {
		t.Fatal(err)
	}
	patterns := []string{"**"}

	t.Run("an edit beneath a directory symlink changes the fingerprint", func(t *testing.T) {
		got, err := ExpandInputs(scopeOf(cwd), patterns)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(got, "vendored/api.go") {
			t.Fatalf("ExpandInputs(**) = %q, want the file reached through the link", got)
		}
		before := mustFingerprint(t, cwd, patterns, "recipe")
		writeTree(t, root, map[string]string{"shared/v1/api.go": "package api // edited"})
		after := mustFingerprint(t, cwd, patterns, "recipe")
		if after.Digest == before.Digest {
			t.Fatal("editing a file beneath a directory symlink left the fingerprint unchanged")
		}
		if c := after.Changed(before); !reflect.DeepEqual(c, []string{"vendored/api.go"}) {
			t.Fatalf("Changed = %q, want the file under its lexical path", c)
		}
	})
	t.Run("retargeting a directory symlink between equal trees changes it", func(t *testing.T) {
		writeTree(t, root, map[string]string{"shared/v1/api.go": "same", "shared/v2/api.go": "same"})
		before := mustFingerprint(t, cwd, patterns, "recipe")
		link := filepath.Join(cwd, "vendored")
		if err := os.Remove(link); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "shared", "v2"), link); err != nil {
			t.Fatal(err)
		}
		if after := mustFingerprint(t, cwd, patterns, "recipe"); after.Digest == before.Digest {
			t.Fatal("retargeting a directory symlink left the fingerprint unchanged")
		}
	})
	t.Run("a symlink cycle terminates", func(t *testing.T) {
		loopy := filepath.Join(root, "loopy")
		writeTree(t, loopy, map[string]string{"a/f.txt": "f"})
		if err := os.Symlink("..", filepath.Join(loopy, "a", "up")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("a", filepath.Join(loopy, "self")); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		var got []string
		var err error
		go func() {
			defer close(done)
			got, err = ExpandInputs(scopeOf(loopy), []string{"**"})
		}()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("ExpandInputs did not terminate on a symlink cycle")
		}
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(got, "a/f.txt") {
			t.Fatalf("ExpandInputs over a cycle = %q, want a/f.txt", got)
		}
		again, _ := ExpandInputs(scopeOf(loopy), []string{"**"})
		if !reflect.DeepEqual(got, again) {
			t.Fatalf("selection over a cycle is not deterministic: %q vs %q", got, again)
		}
	})
	t.Run("a directory symlink leading outside the roots", func(t *testing.T) {
		outside := t.TempDir()
		writeTree(t, outside, map[string]string{"x.go": "package x"})
		dir := filepath.Join(root, "escapes")
		writeTree(t, dir, map[string]string{"main.go": "package main"})
		if err := os.MkdirAll(filepath.Join(dir, "deep"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(dir, "deep", "out")); err != nil {
			t.Fatal(err)
		}
		if _, err := ExpandInputs(scopeOf(dir), []string{"**"}); err == nil || !strings.Contains(err.Error(), "through the directory symlink deep/out") {
			t.Fatalf("a broad walk over an escaping directory link: error = %v, want a refusal naming it", err)
		}
		if _, err := ExpandInputs(scopeOf(dir), []string{"**", "!deep/out/**"}); err != nil {
			t.Fatalf("an excluded escaping link should not refuse: %v", err)
		}
		if _, err := ExpandInputs(scopeOf(dir), []string{"*.go"}); err != nil {
			t.Fatalf("a pattern that cannot reach beneath the link should not refuse: %v", err)
		}
	})
}

// TestFingerprintInputs_SymlinkChains: a symlink input is keyed on its FULLY
// resolved target, so retargeting a link further down its chain changes the
// fingerprint even when the selected link's own text and the bytes it reaches
// are unchanged — for a file link and for a directory link alike. The target
// is keyed relative to its root, so the same tree checked out elsewhere keeps
// its fingerprint.
func TestFingerprintInputs_SymlinkChains(t *testing.T) {
	relink := func(t *testing.T, link, target string) {
		t.Helper()
		if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("file link chain", func(t *testing.T) {
		cwd := fixture(t)
		writeTree(t, cwd, map[string]string{"cfg/a.yaml": "same", "cfg/b.yaml": "same"})
		relink(t, filepath.Join(cwd, "cfg", "mid.yaml"), "a.yaml")
		relink(t, filepath.Join(cwd, "cfg", "current.yaml"), "mid.yaml")
		patterns := []string{"cfg/current.yaml"}
		before := mustFingerprint(t, cwd, patterns, "recipe")
		relink(t, filepath.Join(cwd, "cfg", "mid.yaml"), "b.yaml")
		if after := mustFingerprint(t, cwd, patterns, "recipe"); after.Digest == before.Digest {
			t.Fatal("retargeting the middle of a file-link chain left the fingerprint unchanged")
		}
	})
	t.Run("directory link chain", func(t *testing.T) {
		cwd := fixture(t)
		root := filepath.Dir(cwd)
		writeTree(t, root, map[string]string{"shared/v1/api.go": "same", "shared/v2/api.go": "same"})
		relink(t, filepath.Join(root, "shared", "mid"), "v1")
		relink(t, filepath.Join(cwd, "vendored"), filepath.Join("..", "shared", "mid"))
		patterns := []string{"vendored/**"}
		before := mustFingerprint(t, cwd, patterns, "recipe")
		relink(t, filepath.Join(root, "shared", "mid"), "v2")
		if after := mustFingerprint(t, cwd, patterns, "recipe"); after.Digest == before.Digest {
			t.Fatal("retargeting the middle of a directory-link chain left the fingerprint unchanged")
		}
	})
	t.Run("independent of the checkout path", func(t *testing.T) {
		fp := func() string {
			t.Helper()
			cwd := fixture(t)
			writeTree(t, filepath.Dir(cwd), map[string]string{"shared/v1/api.go": "same", "cfg.yaml": "x"})
			relink(t, filepath.Join(cwd, "vendored"), filepath.Join("..", "shared", "v1"))
			relink(t, filepath.Join(cwd, "cfg.yaml"), filepath.Join("..", "cfg.yaml"))
			return mustFingerprint(t, cwd, []string{"vendored/**", "cfg.yaml"}, "recipe").Digest
		}
		if a, b := fp(), fp(); a != b {
			t.Fatalf("the same tree at two paths fingerprints differently: %s vs %s", a, b)
		}
	})
}
