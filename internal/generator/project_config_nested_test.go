// project_config_nested_test.go — the in-place nested setter keeps every other
// byte of a hand-written forge.yaml: comments, key order, unknown blocks.
//
// It reaches into an existing block and creates an absent one (the surgical
// write the forge-version pin relies on); a whole-struct marshal would have
// stripped every comment and every block NormalizeForWrite considers
// derivable.
package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// richManifest is a forge.yaml with the shapes a real one has: a comment
// header, comments inside blocks, a nested block to reach into (ci.lint), and
// room to create a new one.
const richManifest = `# My project. Please do not eat the comments.
name: app
module_path: example.com/app
forge_version: v0.0.1

# Hand-tuned; the defaults were wrong for us.
database:
    migration_safety:
        volatile_default: error

ci:
    provider: github
    lint:
        golangci: true   # we care about this one
        buf: false

# A trailing comment with nothing after it. Also must survive.
`

func TestSetProjectConfigScalarPath_PreservesUserContent(t *testing.T) {
	t.Parallel()

	p := filepath.Join(t.TempDir(), "forge.yaml")
	if err := os.WriteFile(p, []byte(richManifest), 0o644); err != nil {
		t.Fatal(err)
	}

	// Reach INTO an existing block…
	if err := SetProjectConfigScalarPath(p, []string{"ci", "lint", "buf"}, true); err != nil {
		t.Fatalf("set ci.lint.buf: %v", err)
	}
	// …and CREATE a nested one that does not exist yet.
	if err := SetProjectConfigScalarPath(p, []string{"lint", "frontend", "no_important"}, "error"); err != nil {
		t.Fatalf("set lint.frontend.no_important: %v", err)
	}

	gotRaw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	got := string(gotRaw)

	if !strings.Contains(got, "buf: true") || strings.Contains(got, "buf: false") {
		t.Errorf("ci.lint.buf was not flipped on:\n%s", got)
	}
	if !strings.Contains(got, "no_important: error") {
		t.Errorf("lint.frontend.no_important was not created:\n%s", got)
	}

	for _, survivor := range []string{
		"# My project. Please do not eat the comments.",
		"# Hand-tuned; the defaults were wrong for us.",
		"golangci: true   # we care about this one",
		"# A trailing comment with nothing after it. Also must survive.",
		"\nci:\n",
		"    provider: github",
		"        volatile_default: error",
	} {
		if !strings.Contains(got, survivor) {
			t.Errorf("lost user content %q from the document:\n%s", survivor, got)
		}
	}

	cfg, err := ReadProjectConfig(p)
	if err != nil {
		t.Fatalf("result does not load: %v\n%s", err, got)
	}
	if !cfg.CI.Lint.Buf {
		t.Errorf("ci.lint.buf did not read back as true")
	}
	if cfg.Lint.Frontend.NoImportant != "error" {
		t.Errorf("lint.frontend.no_important = %q, want error", cfg.Lint.Frontend.NoImportant)
	}
}
