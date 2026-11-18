package kclrender

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"kcl-lang.io/kpm/pkg/downloader"
)

// A project in a directory with a space ("My Landing") rendered nothing: kpm
// percent-encodes the source and never decodes it, so the absolute path named
// `My%20Landing`. The source under workDir goes relative, which kpm resolves
// against workDir.
func TestKpmRunSource_SurvivesASpaceInTheProjectPath(t *testing.T) {
	root := filepath.Join(t.TempDir(), "My Landing")
	rel := filepath.Join("deploy", "kcl", "prod")
	if err := os.MkdirAll(filepath.Join(root, rel), 0o755); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, rel)

	got := kpmRunSource(root, source)
	if filepath.IsAbs(got) {
		t.Fatalf("a source with a space must be handed over relative to the work dir, got %q", got)
	}
	parsed, err := downloader.NewSourceFromStr(got)
	if err != nil || parsed.Local == nil {
		t.Fatalf("kpm cannot read %q as a local source: %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, parsed.Local.Path)); err != nil {
		t.Errorf("%q reaches kpm as %q, which names no file under the work dir: %v", got, parsed.Local.Path, err)
	}

	plain := filepath.Join(t.TempDir(), "landing", rel)
	if got := kpmRunSource(filepath.Dir(filepath.Dir(filepath.Dir(plain))), plain); got != kpmSourceURL(plain) {
		t.Errorf("a path kpm already handles must go as before: %q", got)
	}
	outside := filepath.Join(t.TempDir(), "My Other", "x")
	if got := kpmRunSource(root, outside); got != kpmSourceURL(outside) {
		t.Errorf("a source outside the work dir has no relative form: %q", got)
	}
	if (&url.URL{Path: "a b"}).EscapedPath() == "a b" {
		t.Fatal("the premise changed: a space no longer needs escaping")
	}
}
