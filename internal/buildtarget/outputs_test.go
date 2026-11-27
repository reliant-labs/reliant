package buildtarget

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeOutputTree(t *testing.T, files ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestMatchOutputs(t *testing.T) {
	dir := writeOutputTree(t,
		"dist/acme_linux_amd64.tar.gz",
		"dist/acme_darwin_arm64.tar.gz",
		"dist/checksums.txt",
		"dist/nested/deep/acme.dmg",
		"out/acme.app/Contents/Info.plist",
		"out/acme.app/Contents/MacOS/acme",
		"README.md",
	)
	if err := os.Symlink(filepath.Join(dir, "README.md"), filepath.Join(dir, "dist", "link.tar.gz")); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		pattern string
		want    []string
	}{
		{"dist/acme_linux_amd64.tar.gz", []string{"dist/acme_linux_amd64.tar.gz"}},
		{"./dist/checksums.txt", []string{"dist/checksums.txt"}},
		// A glob within one segment; the symlink is never selected.
		{"dist/*.tar.gz", []string{"dist/acme_darwin_arm64.tar.gz", "dist/acme_linux_amd64.tar.gz"}},
		{"dist/**/*.dmg", []string{"dist/nested/deep/acme.dmg"}},
		// A directory selects every file beneath it.
		{"out/acme.app", []string{"out/acme.app/Contents/Info.plist", "out/acme.app/Contents/MacOS/acme"}},
		{"*.md", []string{"README.md"}},
		// Nothing matches: no error, no files.
		{"dist/*.zip", nil},
		{"missing/acme.zip", nil},
	}
	for _, c := range cases {
		got, err := MatchOutputs(dir, c.pattern)
		if err != nil {
			t.Errorf("MatchOutputs(%q): %v", c.pattern, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("MatchOutputs(%q) = %q, want %q", c.pattern, got, c.want)
		}
	}
}

func TestMatchOutputsRefusesPathsOutsideTheWorkingDirectory(t *testing.T) {
	dir := writeOutputTree(t, "dist/acme.zip")
	for pattern, want := range map[string]string{
		"/etc/passwd":         "absolute",
		"C:/dist/acme.zip":    "absolute",
		"../acme.zip":         "leaves the build's working directory",
		"dist/../../acme.zip": "leaves the build's working directory",
		"!dist/acme.zip":      "no `!` exclusion",
		".":                   "whole working directory",
		"  ":                  "empty",
	} {
		_, err := MatchOutputs(dir, pattern)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("MatchOutputs(%q) error = %v, want one naming %q", pattern, err, want)
		}
	}
}

func TestMatchOutputsMissingWorkingDirectoryMatchesNothing(t *testing.T) {
	got, err := MatchOutputs(filepath.Join(t.TempDir(), "absent"), "dist/*")
	if err != nil || got != nil {
		t.Fatalf("MatchOutputs over a missing dir = %q, %v; want nothing and no error", got, err)
	}
}
