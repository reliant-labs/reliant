package ciguard

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

var guardTestsScript = filepath.Join("..", "..", "scripts", "ci-guard-tests.sh")

// guardTestsStep is the merge tier's run of the guards' whole-repo checks
// that fit its budget.
const guardTestsStep = "scripts/ci-guard-tests.sh HEAD^1"

// TestGuardTestsRunInTheMergeTier holds the merge tier's full-mode guard
// step (five of forge main's red episodes in the week to 2026-11-25 were a
// guard whose whole-repo check skips under -short): test-short, the merge
// tier's job, runs scripts/ci-guard-tests.sh against HEAD^1 after its -short
// tier, whose compile it reuses; the script runs no -short and no -race; and
// it names no internal/tierguard test, whose renders are main's.
func TestGuardTestsRunInTheMergeTier(t *testing.T) {
	wf := readWorkflow(t)
	if got := strings.TrimSpace(wf.Jobs["test-short"].If); got != queueTier {
		t.Fatalf("test-short runs if %q, want the merge tier's %q", got, queueTier)
	}
	steps := jobStepRuns(t, "test-short")
	at, shortAt := slices.Index(steps, guardTestsStep), slices.Index(steps, "scripts/ci-test-shard.sh ${{ matrix.args }}")
	switch {
	case at < 0:
		t.Errorf("test-short does not run %q: the merge tier would never run the guards' whole-repo checks", guardTestsStep)
	case shortAt < 0 || at < shortAt:
		t.Errorf("test-short runs %q before its -short tier: the guard tests reuse that tier's compile", guardTestsStep)
	}

	script := readText(t, guardTestsScript)
	ran := false
	for _, line := range strings.Split(script, "\n") {
		code := strings.TrimSpace(line)
		if strings.HasPrefix(code, "#") || !strings.Contains(code, "ci-go-test.sh") {
			continue
		}
		ran = true
		for _, banned := range []string{"-short", "-race"} {
			if strings.Contains(code, banned) {
				t.Errorf("%s runs %s (%q): the guard tests are full mode, and -race is main's", guardTestsScript, banned, code)
			}
		}
		if !strings.Contains(code, "-count=1") {
			t.Errorf("%s runs %q without -count=1: the guards read files through git and `go list`, which the test cache cannot see", guardTestsScript, code)
		}
	}
	if !ran {
		t.Errorf("%s never runs scripts/ci-go-test.sh: the step would test nothing", guardTestsScript)
	}
	for _, entry := range bashArray(t, guardTestsScript, script, "tests") {
		if strings.HasPrefix(entry, "internal/tierguard.") {
			t.Errorf("%s names %s: tierguard renders whole projects, which is main's work, not the merge tier's", guardTestsScript, entry)
		}
	}
}

// TestGuardTestsNameDeclaredTests proves every test the script names is a
// top-level test its package still declares, and every trigger names a path
// that exists. Cache-sound: the files are read by this process.
func TestGuardTestsNameDeclaredTests(t *testing.T) {
	checkNamedTestsAndTriggers(t, guardTestsScript)
}
