//go:build e2e

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2EFailedGenerateRevertConsistencyAndPreservation is the
// acceptance gate for the failed-generate UX fixes, driven through the
// REAL pipeline (buf compile + embedded postgres shadow-apply), with the
// validation failure forced by a deliberately conflicting hand-written
// file (independent of any codegen bug: the user file redeclares a
// symbol the generated ops file also declares, so `go build ./...`
// fails only when both are on disk).
//
// Pinned contract, in one run:
//
//  1. Revert-set consistency: the failed run reverts EVERYTHING it
//     first-wrote — the Tier-1 ops file AND the scaffold-once shim
//     written the same run. No mixed tree (the old defect: the shim
//     survived, its ops dependency didn't, and `go build` emitted 20+
//     red-herring "undefined" errors).
//  2. Preservation: both reverted files land under
//     .forge/failed-generate/ (same relative paths) plus error.txt, so
//     the compiler error's file:line coordinates stay inspectable.
//  3. Tail visibility: the compiler output is REPEATED near the END of
//     the run's output — a `tail` of the failed run shows the error,
//     not just revert bookkeeping.
//  4. Cleanup: the next SUCCESSFUL generate removes the preserve dir.
func TestE2EFailedGenerateRevertConsistencyAndPreservation(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin, "project", "new", "failapp", "--mod", "example.com/failapp", "--service", "widget")
	projectDir := filepath.Join(dir, "failapp")
	addCorpusForgePkgReplace(t, projectDir)

	// Author a CRUD entity so the projection will emit the ops file +
	// scaffold-once shim pair.
	protoPath := filepath.Join(projectDir, "proto", "services", "widget", "v1", "widget.proto")
	proto := readFileE2E(t, protoPath)
	proto += "\n// forge:entity\nmessage Gadget { string id = 1; string name = 2; }\n"
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("author widget proto: %v", err)
	}

	// The conflict: a hand-written file in the handler package declaring
	// gadgetToProto, which handlers_crud_ops_gen.go also declares. Valid
	// Go on its own; a redeclaration error the moment the ops file lands.
	widgetDir := filepath.Join(projectDir, "internal", "handlers", "widget")
	conflictPath := filepath.Join(widgetDir, "conflict.go")
	if err := os.MkdirAll(widgetDir, 0o755); err != nil {
		t.Fatal(err)
	}
	conflict := "package widget\n\n// Hand-written pre-existing user file: collides with the generated ops.\nfunc gadgetToProto() {}\n"
	if err := os.WriteFile(conflictPath, []byte(conflict), 0o644); err != nil {
		t.Fatal(err)
	}

	opsPath := filepath.Join(widgetDir, "handlers_crud_ops_gen.go")
	shimPath := filepath.Join(widgetDir, "handlers_crud.go")

	// ── The failing run ───────────────────────────────────────────────
	cmd := exec.Command(forgeBin, "scaffold")
	cmd.Dir = projectDir
	outBytes, err := cmd.CombinedOutput()
	out := string(outBytes)
	if err == nil {
		t.Fatalf("scaffold should FAIL its go-build validation (gadgetToProto redeclared), output:\n%s", out)
	}

	// (1) Revert-set consistency: neither the Tier-1 ops file nor the
	// scaffold-once shim written this run survives.
	assertPathNotExistsE2E(t, opsPath)
	assertPathNotExistsE2E(t, shimPath)
	// The pre-existing hand-written file is untouched.
	if got := readFileE2E(t, conflictPath); got != conflict {
		t.Errorf("pre-existing user file must survive the revert byte-for-byte:\n%s", got)
	}

	// (2) Preservation: both reverted files (same relative paths) plus
	// error.txt with the compiler output.
	preserveDir := filepath.Join(projectDir, ".forge", "failed-generate")
	assertPathExistsE2E(t, filepath.Join(preserveDir, "internal", "handlers", "widget", "handlers_crud_ops_gen.go"))
	assertPathExistsE2E(t, filepath.Join(preserveDir, "internal", "handlers", "widget", "handlers_crud.go"))
	errTxt := readFileE2E(t, filepath.Join(preserveDir, "error.txt"))
	if !strings.Contains(errTxt, "redeclared") {
		t.Errorf("error.txt should carry the compiler output, got:\n%s", errTxt)
	}
	// The preserved ops file really is the source the error cites.
	preservedOps := readFileE2E(t, filepath.Join(preserveDir, "internal", "handlers", "widget", "handlers_crud_ops_gen.go"))
	if !strings.Contains(preservedOps, "func gadgetToProto(") {
		t.Errorf("preserved ops file should contain the colliding declaration:\n%s", preservedOps)
	}

	// The failure report names the preserve dir.
	if !strings.Contains(out, ".forge/failed-generate") {
		t.Errorf("failure output must point at the preserve directory:\n%s", out)
	}

	// (3) Tail visibility: the compiler error appears again NEAR THE END
	// of the output — after the reverted-file list — so `tail` shows it.
	tail := out
	if len(tail) > 2500 {
		tail = tail[len(tail)-2500:]
	}
	if !strings.Contains(tail, "redeclared") {
		t.Errorf("the compiler error must be repeated in the LAST part of the output (tail-visible); tail was:\n%s", tail)
	}
	if lastReverted, lastCompiler := strings.LastIndex(out, "reverted"), strings.LastIndex(out, "redeclared"); lastCompiler < lastReverted {
		t.Errorf("compiler output must print AFTER the revert bookkeeping:\n%s", out)
	}

	// ── The recovery run ──────────────────────────────────────────────
	if err := os.Remove(conflictPath); err != nil {
		t.Fatal(err)
	}
	runCmd(t, projectDir, forgeBin, "scaffold")

	// The pair exists again, and (4) the preserve dir is cleaned up.
	assertPathExistsE2E(t, opsPath)
	assertPathExistsE2E(t, shimPath)
	assertPathNotExistsE2E(t, preserveDir)

	runCmd(t, projectDir, "go", "build", "./...")
}
