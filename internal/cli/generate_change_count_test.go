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
	"github.com/reliant-labs/forge/pkg/testkit"
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
	testkit.Slow(t, "execs git")
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
	testkit.Slow(t, "execs git")
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
	testkit.Slow(t, "execs git")
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
// a forge-owned file without going through forge's writers still changed the
// tree. It is counted, and named, so the reader can tell it apart.
func TestGenerate_ChangeCountNamesWritesOutsideForgeWriters(t *testing.T) {
	testkit.Slow(t, "execs git")
	dir := t.TempDir()
	const rel = "internal/raw_gen.go"
	writeTreeFileT(t, filepath.Join(dir, filepath.FromSlash(rel)), stampedForgeOwned(t, rel, "package internal\n"))

	raw := GenStep{
		Name: "raw writer",
		Gate: always,
		Tag:  "codegen",
		Run: func(ctx *pipelineContext) error {
			body := stampedForgeOwned(t, rel, "package internal\n\nconst v = 2\n")
			return os.WriteFile(filepath.Join(ctx.AbsPath, filepath.FromSlash(rel)), []byte(body), 0o644)
		},
	}
	withPipelineSteps(t, []GenStep{fixedPointWriter(), raw})
	out := runPipelineOK(t, dir)
	if !strings.Contains(out, "2 changed (1 new, 1 updated)") {
		t.Errorf("want internal/x_gen.go new and %s updated:\n%s", rel, out)
	}
	if !strings.Contains(out, "changed outside forge's writers: "+rel) {
		t.Errorf("the write no forge writer made must be named:\n%s", out)
	}
}

// TestGenerate_ChangeCountLeavesOutFilesForgeDoesNotOwn is the Ranger case:
// another agent saved its hand-written test file while generate ran, and the
// closing line counted it as written and listed it "changed outside forge's
// writers", as though generate had produced it. A file forge neither owns
// nor wrote is not the run's output: it is named apart, not counted.
func TestGenerate_ChangeCountLeavesOutFilesForgeDoesNotOwn(t *testing.T) {
	testkit.Slow(t, "execs git")
	dir := t.TempDir()
	const rel = "pkg/rangerclient/watcher_test.go"
	writeTreeFileT(t, filepath.Join(dir, filepath.FromSlash(rel)), "package rangerclient\n")

	// Stands in for the other agent: it is not forge, so it writes the
	// file directly, mid-run.
	otherAgent := GenStep{
		Name: "another process saves a hand-written file",
		Gate: always,
		Tag:  "codegen",
		Run: func(ctx *pipelineContext) error {
			return os.WriteFile(filepath.Join(ctx.AbsPath, filepath.FromSlash(rel)), []byte("package rangerclient\n\n// edited\n"), 0o644)
		},
	}
	withPipelineSteps(t, []GenStep{fixedPointWriter(), otherAgent})
	out := runPipelineOK(t, dir)
	if !strings.Contains(out, "1 changed (1 new, 0 updated)") {
		t.Errorf("want only internal/x_gen.go counted:\n%s", out)
	}
	if strings.Contains(out, "changed outside forge's writers") {
		t.Errorf("a file forge does not own was reported as a write outside forge's writers:\n%s", out)
	}
	if !strings.Contains(out, "1 file(s) changed while generate ran that no forge writer wrote, not counted above: "+rel) {
		t.Errorf("the foreign change must be named apart from the run's output:\n%s", out)
	}
}

// A run whose tree moved under it is not a fixed point, even when what moved
// is not counted as its output: a file another process CREATED mid-run is
// not in the pre-run fingerprint, so only this check refuses to record it.
func TestGenerateCacheRecord_RefusesARunWithForeignChanges(t *testing.T) {
	c := &generateCacheRun{mode: generateCacheMode{record: true}, pre: &generateFingerprint{}}
	var b strings.Builder
	pctx := &pipelineContext{foreignChanges: []string{"pkg/new_test.go"}}
	if c.record(&b, pctx, nil, checksums.WriteSummary{Touched: 3}) {
		t.Fatal("recorded a run another process changed the tree under as a verified fixed point")
	}
	if !strings.Contains(b.String(), "1 file(s) forge does not own changed while generate ran") {
		t.Errorf("the refusal does not say why: %q", b.String())
	}
}

// An unrelated generator's header must not let its mid-run write count as
// forge output or certify a fixed point, even when forge itself wrote nothing.
func TestGenerate_UnrelatedGeneratedChangeIsForeign(t *testing.T) {
	testkit.Slow(t, "execs git")
	dir := t.TempDir()
	const rel = "internal/other_gen.go"
	const header = "// Code generated by another-tool. DO NOT EDIT.\n\npackage internal\n"
	writeTreeFileT(t, filepath.Join(dir, filepath.FromSlash(rel)), header)
	pre := snapshotWorkTree(dir)
	if pre == nil {
		t.Fatal("could not snapshot the fixture")
	}
	writeTreeFileT(t, filepath.Join(dir, filepath.FromSlash(rel)), header+"\nconst changed = true\n")

	sum, outside, foreign := completeWriteSummary(checksums.WriteSummary{}, dir, pre, nil)
	if sum.Touched != 0 || sum.Changed() != 0 || len(outside) != 0 {
		t.Fatalf("unrelated generated output counted as forge's: %+v, outside %v", sum, outside)
	}
	if len(foreign) != 1 || foreign[0] != rel {
		t.Fatalf("want foreign change %s, got %v", rel, foreign)
	}
	c := &generateCacheRun{mode: generateCacheMode{record: true}, pre: &generateFingerprint{}}
	var out strings.Builder
	if c.record(&out, &pipelineContext{foreignChanges: foreign}, nil, sum) {
		t.Fatal("certified a fixed point despite an unrelated generator's mid-run write")
	}
}

func TestChangedFileIsForges(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		writeTreeFileT(t, filepath.Join(dir, filepath.FromSlash(rel)), body)
	}
	write("internal/x/mock_gen.go", stampedForgeOwned(t, "internal/x/mock_gen.go", "package x\n"))
	write("gen/a/a.pb.go", "// Code generated by protoc-gen-go. DO NOT EDIT.\n\npackage a\n")
	write("pkg/x/x_test.go", "package x\n")
	write("web/config.json", "{}\n")
	for rel, want := range map[string]bool{
		"internal/x/mock_gen.go": true,  // forge's certification marker
		"gen/a/a.pb.go":          false, // generated does not mean forge-owned
		"web/config.json":        true,  // certified by body hash (unstampable)
		"gone/removed.go":        true,  // cannot be judged: counted, as before
		"pkg/x/x_test.go":        false, // hand-written
	} {
		if got := changedFileIsForges(dir, rel, map[string]string{"web/config.json": "h"}); got != want {
			t.Errorf("changedFileIsForges(%s) = %v, want %v", rel, got, want)
		}
	}
}
