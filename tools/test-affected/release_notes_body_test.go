package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestReleaseWorkflowCapsReleaseNotes holds release.yml's "Publish the GitHub
// Release" step to a body it built and measured itself. v1.33.0 and v1.34.0
// passed --generate-notes, GitHub appended every PR since v1.21.3, the body
// went over its 125000-character limit (HTTP 422), and the CLI publish behind
// the Release never ran.
func TestReleaseWorkflowCapsReleaseNotes(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	wf := string(b)
	for _, want := range []string{
		`releases/generate-notes`,
		`previous_tag_name="$PREV"`,
		`scripts/release-notes-body.py "$VERSION" \` + "\n" + `            --generated /tmp/release-generated.md --compare-url "$COMPARE_URL"`,
		`--verify-tag --notes-file /tmp/release-body.md`,
	} {
		if !strings.Contains(wf, want) {
			t.Errorf("release.yml no longer contains %q", want)
		}
	}
	for i, line := range strings.Split(wf, "\n") {
		if code, _, _ := strings.Cut(line, "#"); strings.Contains(code, "--generate-notes") {
			t.Errorf("release.yml:%d passes --generate-notes: GitHub appends a list nobody measured, and a body over 125000 characters is refused", i+1)
		}
	}
}

// TestReleaseNotesBodyCapsAtLimit runs scripts/release-notes-body.py over a
// generated list far past GitHub's limit and checks the body it prints fits,
// ends at a whole line and links the full comparison.
func TestReleaseNotesBodyCapsAtLimit(t *testing.T) {
	script := filepath.Join(repoRoot, "scripts", "release-notes-body.py")
	// Read here so the test cache keys on the script the child runs.
	if _, err := os.ReadFile(script); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Fatal("python3 is not on PATH: release.yml's runners have it")
	}

	var gen strings.Builder
	gen.WriteString("## What's Changed\n")
	for i := 0; gen.Len() < 300000; i++ {
		fmt.Fprintf(&gen, "* fix(thing): a change with a long enough title — é ü 🚀 by @someone in https://github.com/reliant-labs/reliant/pull/%d\n", i)
	}
	genFile := filepath.Join(t.TempDir(), "generated.md")
	if err := os.WriteFile(genFile, []byte(gen.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	const compare = "https://github.com/reliant-labs/reliant/compare/v1.0.0...v1.0.1"
	// A tag with no docs/data/releases file: the body is the generated list alone.
	cmd := exec.Command("python3", script, "v0.0.0-test", "--generated", genFile, "--compare-url", compare)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("release-notes-body.py: %v", err)
	}
	body := string(out)
	if len(out) > 120000 {
		t.Errorf("body is %d bytes, over the 120000 cap", len(out))
	}
	if !strings.HasSuffix(body, compare+"_\n") {
		t.Errorf("body does not end with the compare link: ...%q", body[max(0, len(body)-200):])
	}
	kept, _, _ := strings.Cut(body, "\n_Truncated")
	if !strings.HasPrefix(gen.String(), kept) || !strings.HasSuffix(kept, "\n") {
		t.Errorf("the kept part is not whole lines of the generated list: ...%q", kept[max(0, len(kept)-200):])
	}

	// A list under the cap is passed through whole.
	small := filepath.Join(t.TempDir(), "small.md")
	if err := os.WriteFile(small, []byte("## What's Changed\n* one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = exec.Command("python3", script, "v0.0.0-test", "--generated", small, "--compare-url", compare).Output()
	if err != nil {
		t.Fatalf("release-notes-body.py: %v", err)
	}
	if got := string(out); got != "## What's Changed\n* one\n" {
		t.Errorf("a body under the cap changed: %q", got)
	}
}
