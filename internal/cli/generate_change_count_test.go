// The completion line's "N changed" is the fixed-point check agents run
// generate against: zero means the tree already matched. It used to count
// only the writes that went through forge's journaled chokepoints, so every
// other write vanished from it. The P3b pilot saw "0 changed" while generate
// rewrote the scaffold ledger (F17), and the ownership state
// (.forge/hashes.json) was saved after the line was printed. Each test here
// is one write the line missed.
package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
)

// fixedPointWriter is a forge writer whose output already matches the tree
// after the first run: the shape of a steady-state generate.
func fixedPointWriter() GenStep {
	return GenStep{
		Name: "forge writer at its fixed point",
		Gate: always,
		Tag:  "codegen",
		Run: func(ctx *pipelineContext) error {
			_, err := checksums.WriteGeneratedFile(ctx.AbsPath, "internal/x_gen.go", []byte("package internal\n"), &checksums.FileChecksums{}, true)
			return err
		},
	}
}

func runPipelineOK(t *testing.T, dir string) string {
	t.Helper()
	out, err := runPipelineCapturingOutput(t, dir)
	if err != nil {
		t.Fatalf("generate failed: %v\n%s", err, out)
	}
	return out
}

// TestGenerate_ChangeCountIncludesTheScaffoldLedger is F17: a run whose only
// change is the birth ledger recording a newly absent scaffold printed "0
// changed".
func TestGenerate_ChangeCountIncludesTheScaffoldLedger(t *testing.T) {
	dir := t.TempDir()
	writeTreeFileT(t, filepath.Join(dir, "internal", "x.go"), "package internal\n")
	checksums.ResetScaffoldLedgerCache()
	t.Cleanup(checksums.ResetScaffoldLedgerCache)

	report := GenStep{
		Name: "report missing scaffolds",
		Gate: always,
		Tag:  "validate",
		Run: func(ctx *pipelineContext) error {
			reportMissingScaffolds(io.Discard, ctx.AbsPath)
			return nil
		},
	}
	withPipelineSteps(t, []GenStep{fixedPointWriter(), report})
	runPipelineOK(t, dir) // first run: creates internal/x_gen.go

	// A scaffold forge recorded is now gone, and no run has reported it:
	// the next run records it as reported, rewriting the ledger.
	entry := filepath.Join(dir, filepath.FromSlash(checksums.ScaffoldEntryPath("internal/gone.go")))
	writeTreeFileT(t, entry, "{}\n")
	checksums.ResetScaffoldLedgerCache()
	before, _ := os.ReadFile(entry)

	out := runPipelineOK(t, dir)
	after, _ := os.ReadFile(entry)
	if string(before) == string(after) {
		t.Fatalf("precondition: the run must rewrite %s:\n%s", entry, after)
	}
	if strings.Contains(out, "0 changed") {
		t.Errorf("generate rewrote %s and reported \"0 changed\":\n%s", entry, out)
	}
	if !strings.Contains(out, "1 changed (0 new, 1 updated)") {
		t.Errorf("want the ledger counted as 1 updated file:\n%s", out)
	}
}

// TestGenerate_ChangeCountIncludesOwnershipState: a comment-incapable output
// (JSON) records its hash in .forge/hashes.json, which was saved after the
// completion line — so the line counted the JSON but not the state file.
func TestGenerate_ChangeCountIncludesOwnershipState(t *testing.T) {
	dir := t.TempDir()
	writeTreeFileT(t, filepath.Join(dir, "internal", "x.go"), "package internal\n")

	jsonWriter := GenStep{
		Name: "forge writer of a JSON output",
		Gate: always,
		Tag:  "codegen",
		Run: func(ctx *pipelineContext) error {
			_, err := checksums.WriteGeneratedFile(ctx.AbsPath, "deploy/spec_gen.json", []byte("{\"a\": 1}\n"), ctx.Checksums, true)
			return err
		},
	}
	withPipelineSteps(t, []GenStep{productionStep(t, "load checksums"), jsonWriter})
	out := runPipelineOK(t, dir)
	cert := checksums.HashCertPath("deploy/spec_gen.json")
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(cert))); err != nil {
		t.Fatalf("precondition: the run must write %s: %v\n%s", cert, err, out)
	}
	if !strings.Contains(out, "2 changed (2 new, 0 updated)") {
		t.Errorf("want the JSON output and %s both counted as new:\n%s", cert, out)
	}

	// And the second run is a true fixed point.
	if out := runPipelineOK(t, dir); !strings.Contains(out, "0 changed") {
		t.Errorf("an idempotent re-run must report 0 changed:\n%s", out)
	}
}

// TestGenerate_ChangeCountIncludesRemovals: retiring a generated file is a
// change. It was counted as touched and nothing else.
func TestGenerate_ChangeCountIncludesRemovals(t *testing.T) {
	dir := t.TempDir()
	writeTreeFileT(t, filepath.Join(dir, "internal", "stale_gen.go"), "package internal\n")

	retire := GenStep{
		Name: "retire a stale output",
		Gate: always,
		Tag:  "codegen",
		Run: func(ctx *pipelineContext) error {
			return checksums.RemoveJournaled(filepath.Join(ctx.AbsPath, "internal", "stale_gen.go"))
		},
	}
	withPipelineSteps(t, []GenStep{retire})
	out := runPipelineOK(t, dir)
	if !strings.Contains(out, "1 changed (0 new, 0 updated, 1 removed)") {
		t.Errorf("want the retired file counted as removed:\n%s", out)
	}
}

// TestGenerate_ChangeCountNamesWritesOutsideForgeWriters: a step that writes
// a project file without going through forge's writers still changed the
// tree. It is counted, and named, so the reader can tell it apart.
func TestGenerate_ChangeCountNamesWritesOutsideForgeWriters(t *testing.T) {
	dir := t.TempDir()
	writeTreeFileT(t, filepath.Join(dir, "config", "app.yaml"), "a: 1\n")

	raw := GenStep{
		Name: "raw writer",
		Gate: always,
		Tag:  "codegen",
		Run: func(ctx *pipelineContext) error {
			return os.WriteFile(filepath.Join(ctx.AbsPath, "config", "app.yaml"), []byte("a: 2\n"), 0o644)
		},
	}
	withPipelineSteps(t, []GenStep{fixedPointWriter(), raw})
	out := runPipelineOK(t, dir)
	if !strings.Contains(out, "2 changed (1 new, 1 updated)") {
		t.Errorf("want internal/x_gen.go new and config/app.yaml updated:\n%s", out)
	}
	if !strings.Contains(out, "changed outside forge's writers: config/app.yaml") {
		t.Errorf("the write no forge writer made must be named:\n%s", out)
	}
}
