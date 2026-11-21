package main

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestBuildUnifiedArgsScript runs scripts/test-build-unified-args.sh: every
// `pnpm run dist ...` in scripts/build-unified.sh reaches electron-builder with
// --publish=never and its platform switches, and no `--` that pnpm (unlike npm)
// would forward in front of them.
func TestBuildUnifiedArgsScript(t *testing.T) {
	testkit.Slow(t, "runs scripts/test-build-unified-args.sh (pnpm and node subprocesses)",
		testkit.Inputs("scripts/build-unified.sh", "scripts/test-build-unified-args.sh", "electron/package.json"))
	if _, err := exec.LookPath("pnpm"); err != nil {
		t.Fatal("pnpm is not on PATH: the electron scripts the test drives run through it")
	}
	cmd := exec.Command("bash", filepath.Join(repoRoot, "scripts", "test-build-unified-args.sh"))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("scripts/test-build-unified-args.sh: %v\n%s", err, out)
	}
}
