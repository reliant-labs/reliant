package lint

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBufLaneFailsWithoutBuf pins the buf half of "a gating lane that did not
// run cannot pass": a project with a buf.yaml on a machine with no buf used to
// SKIP the buf lint lane, and `forge lint` passed without linting a proto. The
// lane must report that it could NOT run, naming the fix, and that fails the
// run (lint_skip.go).
func TestBufLaneFailsWithoutBuf(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "buf.yaml"), []byte("version: v2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	t.Setenv("PATH", t.TempDir())

	step := findStep(t, "buf lint")
	rc := &lintRunCtx{ctx: context.Background(), cwd: dir}
	if run, skip := step.shouldRun(rc); !run {
		t.Fatalf("buf lint skipped with no buf on PATH (%q): a gating lane must never skip for a missing tool", skip)
	}
	fs, gated, cerr := collectGraded(t, step, rc)
	if cerr != nil || !gated || len(fs) != 1 || fs[0].Severity != lintSevError {
		t.Fatalf("want one gating error finding, got gated=%v err=%v %+v", gated, cerr, fs)
	}
	if !strings.Contains(fs[0].Message, "buf lint lane cannot run") || !strings.Contains(fs[0].FixHint, "forge tools install buf") {
		t.Errorf("want the reason and the fix, got %+v", fs[0])
	}
}

// TestBufLaneWithoutBufYAMLNeedsNoBuf: a project with no buf.yaml has no
// proto to lint, so a missing buf is not a failure there.
func TestBufLaneWithoutBufYAMLNeedsNoBuf(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("PATH", t.TempDir())
	step := findStep(t, "buf lint")
	rc := &lintRunCtx{ctx: context.Background()}
	if fs, gated, err := step.collect(rc); err != nil || gated || len(fs) != 0 {
		t.Errorf("no buf.yaml: want no findings, got gated=%v err=%v %+v", gated, err, fs)
	}
}
