package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// An interrupted `forge generate` is a failed one: it must leave the tree as
// it found it. It used to die where it stood — Ctrl-C, or a harness's SIGTERM
// at its timeout — and the tree kept whatever the run had written so far:
// generated files in their pre-goimports form, an ORM file beside hand code
// that did not match it.
//
// The interrupt is delivered to the run's own channel during a step, so the
// test knows the run has it before the step ends. A self-signal does not give
// that guarantee, and the test that used one was deleted as flaky (forge#1633).
func TestGenerate_InterruptRollsTheTreeBack(t *testing.T) {
	testkit.Slow(t, "execs git")
	dir := t.TempDir()
	mockRel := "internal/store/mock_gen.go"
	stamped, _ := checksums.Stamp(mockRel, []byte("package store\n\n// the committed render\n"))
	writeTreeFileT(t, filepath.Join(dir, filepath.FromSlash(mockRel)), string(stamped))
	before := snapshotTree(t, dir)

	var runCh chan<- os.Signal
	stopped := 0
	old := notifyGenerateInterrupts
	notifyGenerateInterrupts = func(c chan<- os.Signal) func() {
		runCh = c
		return func() { stopped++ }
	}
	t.Cleanup(func() { notifyGenerateInterrupts = old })

	writer := GenStep{
		Name: "internal package contracts", Gate: always, Tag: "codegen",
		Run: func(ctx *pipelineContext) error {
			_, err := checksums.WriteDerivedFileOutcome(ctx.AbsPath, mockRel, []byte("package store\n\n// this run's render\n"), &checksums.FileChecksums{}, true)
			return err
		},
	}
	interrupt := GenStep{
		Name: "a step during which the user presses Ctrl-C", Gate: always, Tag: "tools",
		Run: func(*pipelineContext) error {
			if runCh == nil {
				return errors.New("the run never asked for its interrupts")
			}
			runCh <- syscall.SIGINT
			return nil
		},
	}
	ranAfter := false
	after := GenStep{
		Name: "a step that must not run", Gate: always, Tag: "codegen",
		Run: func(*pipelineContext) error { ranAfter = true; return nil },
	}
	withPipelineSteps(t, []GenStep{writer, interrupt, after})
	out, runErr := runPipelineCapturingOutput(t, dir)

	var ie *generateInterruptedError
	if !errors.As(runErr, &ie) {
		t.Fatalf("an interrupted run must fail as interrupted, got %v\n%s", runErr, out)
	}
	if ie.signal != syscall.SIGINT {
		t.Errorf("interrupted by %v, want SIGINT", ie.signal)
	}
	if ranAfter {
		t.Error("the run must stop at the step it was interrupted in")
	}
	if stopped == 0 {
		t.Error("the run never stopped listening for interrupts")
	}
	if diffs := diffTrees(before, snapshotTree(t, dir)); len(diffs) > 0 {
		t.Errorf("an interrupted generate must leave the tree byte-identical, changed:\n  %s\n%s", strings.Join(diffs, "\n  "), out)
	}
	if !strings.Contains(out, "your tree is back to its pre-run state") {
		t.Errorf("the rollback must report the restore:\n%s", out)
	}
}
