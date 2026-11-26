// The closing summary names the files the run changed, not only how many:
// a Ranger agent read "5 changed (2 new, 3 updated)" and had to grep hashes
// to learn whether its package's contract_gen.go and mock_gen.go were among
// them.
package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

func TestGenerateOutcome_NamesEveryChangedFile(t *testing.T) {
	sum := checksums.WriteSummary{
		Touched: 64,
		Created: []string{".forge/scaffolded/x.json", "internal/assigner/mock_gen.go"},
		Updated: []string{"AGENTS.md", "internal/assigner/contract_gen.go"},
		Deleted: []string{"internal/old/old_gen.go"},
	}
	var b bytes.Buffer
	printGenerateOutcome(&b, sum, nil, pipelineFlags{}, 10, 10, runCaveats{})
	got := b.String()
	if !strings.Contains(got, "64 file(s) written, 5 changed (2 new, 2 updated, 1 removed).") {
		t.Fatalf("the counts line changed:\n%s", got)
	}
	for _, want := range []string{
		"   new      internal/assigner/mock_gen.go\n",
		"   updated  AGENTS.md\n",
		"   updated  internal/assigner/contract_gen.go\n",
		"   removed  internal/old/old_gen.go\n",
		"   + 1 forge state file(s) under .forge/\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary does not name %q:\n%s", strings.TrimSpace(want), got)
		}
	}
	if strings.Contains(got, ".forge/scaffolded/x.json") {
		t.Errorf(".forge/ state is counted, not named, without --verbose:\n%s", got)
	}
	// `project new` reads the count off the closing line; the list under it
	// must not change what it reads.
	if s := generateSummary(got); s != "64 files written" {
		t.Errorf("generateSummary = %q, want %q", s, "64 files written")
	}
}

func TestPrintChangedFiles_CapsTheListUnlessVerbose(t *testing.T) {
	var created []string
	for i := range changedFilesShown + 5 {
		created = append(created, fmt.Sprintf("internal/p%02d/x_gen.go", i))
	}
	sum := checksums.WriteSummary{Created: created, Updated: []string{".forge/hashes.json"}}

	var b bytes.Buffer
	printChangedFiles(&b, sum, false)
	got := b.String()
	if n := strings.Count(got, "   new      "); n != changedFilesShown {
		t.Errorf("named %d paths, want the cap of %d:\n%s", n, changedFilesShown, got)
	}
	if !strings.Contains(got, "… and 5 more changed file(s) (--verbose lists every one)") {
		t.Errorf("the paths past the cap are not counted:\n%s", got)
	}

	b.Reset()
	printChangedFiles(&b, sum, true)
	got = b.String()
	if n := strings.Count(got, "   new      "); n != len(created) {
		t.Errorf("--verbose named %d new paths, want all %d:\n%s", n, len(created), got)
	}
	if !strings.Contains(got, "   updated  .forge/hashes.json\n") || strings.Contains(got, "more changed file(s)") {
		t.Errorf("--verbose names every path, .forge/ state included, and hides none:\n%s", got)
	}
}

func TestGenerateOutcome_UpToDateNamesNoFiles(t *testing.T) {
	var b bytes.Buffer
	printGenerateOutcome(&b, checksums.WriteSummary{Touched: 60}, nil, pipelineFlags{}, 10, 10, runCaveats{})
	if got := b.String(); got != "✅ Code generation complete — 60 file(s) up to date, 0 changed.\n" {
		t.Errorf("an up-to-date run prints one line, got:\n%s", got)
	}
}
