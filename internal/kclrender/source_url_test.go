package kclrender

import (
	"os"
	"path/filepath"
	"testing"

	"kcl-lang.io/kpm/pkg/downloader"
)

// TestKpmSourceURL_NamesTheFileKpmWillOpen pins that a render source still
// names the same file after kpm's URL parse, for both shapes forge passes: a
// project-relative path (`forge env render` hands over deploy/kcl/<env>) and
// an absolute one.
//
// On Windows a native path is backslash-separated, and kpm re-serializes the
// source as a URL, which percent-encodes every backslash — deploy\kcl\prod
// arrived as deploy%5Ckcl%5Cprod and every relative render failed with
// "Cannot find the kcl file". On POSIX this passes trivially; the Windows job
// is where it bites.
func TestKpmSourceURL_NamesTheFileKpmWillOpen(t *testing.T) {
	root := t.TempDir()
	rel := filepath.Join("deploy", "kcl", "prod", "main.k")
	if err := os.MkdirAll(filepath.Join(root, filepath.Dir(rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, rel), []byte("a = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Relative sources resolve against the process cwd, as they do for the CLI.
	t.Chdir(root)

	for _, source := range []string{rel, filepath.Join(root, rel)} {
		parsed, err := downloader.NewSourceFromStr(kpmSourceURL(source))
		if err != nil {
			t.Fatalf("kpm cannot parse the source for %q: %v", source, err)
		}
		if parsed.Local == nil {
			t.Fatalf("kpm did not read %q as a local source: %+v", source, parsed)
		}
		if _, err := os.Stat(parsed.Local.Path); err != nil {
			t.Errorf("source %q reaches kpm as %q, which names no file: %v", source, parsed.Local.Path, err)
		}
	}
}
