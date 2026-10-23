// A `forge generate` that REFUSES must leave the tree byte-identical, and
// whatever it says about the tree must be computed from what it wrote.
//
// The observed defect: a forge binary newer than the project's pin ran
// generate, which correctly failed its "forge version compatibility" step —
// but only AFTER "sync forge KCL module vendor" had already rewritten
// .forge-kcl/ (and kcl.mod) from the binary's embedded module. Those writes
// bypassed the rollback journal, so the failure handler found nothing to
// revert and printed "no forge-written files needed reverting (tree is
// unchanged)" over a tree it had just modified, and the refusal itself
// claimed "No files were changed".
package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/kclvendor"
)

// legacyVendorKclMod is a deploy/kcl/kcl.mod in the pre-module-from-binary
// shape, so the KCL migration step would rewrite it and delete .forge-kcl/.
const legacyVendorKclMod = `[package]
name = "app-deploy"
edition = "v0.11.0"
version = "0.0.1"

[dependencies]
forge = { path = "../../.forge-kcl" }
`

// seedStaleVendoredProject lays down a forge consumer pinned to pin, carrying
// the legacy project-local .forge-kcl/ copy an older forge wrote and a
// kcl.mod that declares it. The migration step rewrites the one and deletes
// the other, so anything that runs it changes bytes.
func seedStaleVendoredProject(t *testing.T, dir, pin string) {
	t.Helper()
	writeForgeConsumer(t, dir, pin)
	mustMkdirAllT(t, filepath.Join(dir, "deploy", "kcl"))
	mustWrite(t, filepath.Join(dir, "deploy", "kcl", "kcl.mod"), legacyVendorKclMod)
	mustMkdirAllT(t, filepath.Join(dir, kclvendor.LegacyVendorDirName))
	mustWrite(t, filepath.Join(dir, kclvendor.LegacyVendorDirName, "kcl.mod"), "# vendored by an older forge\n")
	mustWrite(t, filepath.Join(dir, kclvendor.LegacyVendorDirName, ".forge-version"), pin+"\n")
}

// snapshotTree maps every regular file under root (except .forge/, which
// holds the run's lock and is not project content) to its bytes.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			if rel == ".forge" {
				return filepath.SkipDir
			}
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		out[filepath.ToSlash(rel)] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot %s: %v", root, err)
	}
	return out
}

func diffTrees(before, after map[string]string) []string {
	var diffs []string
	for p, b := range before {
		a, ok := after[p]
		switch {
		case !ok:
			diffs = append(diffs, "deleted "+p)
		case a != b:
			diffs = append(diffs, "modified "+p)
		}
	}
	for p := range after {
		if _, ok := before[p]; !ok {
			diffs = append(diffs, "added "+p)
		}
	}
	return diffs
}

func TestGenerate_RefusedVersionCompatWritesNothing(t *testing.T) {
	dir := t.TempDir()
	seedStaleVendoredProject(t, dir, "v0.1.15")

	// A released forge newer than the pin: the compat step must refuse.
	t.Cleanup(func() { buildinfo.Set("dev", "", "unknown") })
	buildinfo.Set("v0.9.9", "", "deadbeef")
	t.Cleanup(checksums.ResetPerRunState)
	t.Cleanup(checksums.CommitRollback)

	before := snapshotTree(t, dir)

	stderr, restore := captureStderr(t)
	var runErr error
	stdout := captureStdout(t, func() {
		runErr = runGeneratePipelineFlags(dir, pipelineFlags{})
	})
	restore()
	out := stdout + stderr.String()

	if runErr == nil || !strings.Contains(runErr.Error(), "forge version compatibility") {
		t.Fatalf("expected the forge version compatibility step to refuse, got: %v\nstderr:\n%s", runErr, out)
	}
	if diffs := diffTrees(before, snapshotTree(t, dir)); len(diffs) > 0 {
		t.Errorf("a refused generate must not touch the tree, but it changed:\n  %s\nstderr:\n%s",
			strings.Join(diffs, "\n  "), out)
	}
	// Byte-identical is not enough: a tree that was rewritten and then
	// rolled back also ends up identical. A refusal must not write at all.
	if strings.Contains(out, "no longer declares the forge KCL module") || strings.Contains(out, "Removed the legacy project-local") {
		t.Errorf("the KCL module migration must not run before the compatibility refusal:\n%s", out)
	}
	if strings.Contains(out, "reverted") {
		t.Errorf("a refusal must have nothing to revert:\n%s", out)
	}
	// The reassurance is allowed only because it is TRUE here (asserted
	// above), and it must be the pipeline's computed line, not refusal prose.
	if !strings.Contains(out, "generate stopped before any step that writes had run. No files were changed.") {
		t.Errorf("a refusal before any writing step should say so:\n%s", out)
	}
	if strings.Contains(runErr.Error(), "No files were changed") {
		t.Errorf("the compat error cannot know what the tree looks like and must not assert it:\n%v", runErr)
	}
}

// The KCL module migration's writes belong to the same rollback set as every
// other generate write: a later failure must restore kcl.mod and the legacy
// .forge-kcl/ it deleted.
func TestSyncForgeKCL_WritesAreJournaledAndRestored(t *testing.T) {
	dir := t.TempDir()
	seedStaleVendoredProject(t, dir, "v0.1.15")
	before := snapshotTree(t, dir)

	checksums.ResetPerRunState()
	t.Cleanup(checksums.ResetPerRunState)
	checksums.BeginRollbackJournal(dir)
	t.Cleanup(checksums.CommitRollback)

	captureStdout(t, func() {
		if err := syncForgeKCL(dir); err != nil {
			t.Fatalf("sync: %v", err)
		}
	})
	if len(diffTrees(before, snapshotTree(t, dir))) == 0 {
		t.Fatal("precondition: the migration should have rewritten kcl.mod and removed .forge-kcl/")
	}

	// The write ledger records rewrites (it has no deletion category); the
	// deletion is proven by the rollback below restoring it.
	sum := checksums.SummarizeWrites(dir)
	if !containsPath(sum.Updated, "deploy/kcl/kcl.mod") {
		t.Errorf("write ledger must record the kcl.mod rewrite, got %+v", sum)
	}

	checksums.RestoreRollback(dir)
	if diffs := diffTrees(before, snapshotTree(t, dir)); len(diffs) > 0 {
		t.Errorf("rollback must restore kcl.mod and .forge-kcl/, still changed:\n  %s", strings.Join(diffs, "\n  "))
	}
}

func containsPath(paths []string, want string) bool {
	for _, p := range paths {
		if filepath.ToSlash(p) == want {
			return true
		}
	}
	return false
}

// With an empty journal, the rollback report may claim an unchanged tree
// only when every step that ran is read-only: the journal vouches for
// forge's own writers, not for every write a step can make.
func TestRollbackGeneratedTree_UnchangedClaimRequiresReadOnlySteps(t *testing.T) {
	for _, tc := range []struct {
		name            string
		onlyReadOnlyRan bool
		want, notWant   string
	}{
		{"only read-only steps ran", true, "No files were changed", "nothing needed reverting"},
		{"a writing step ran", false, "nothing needed reverting", "No files were changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			checksums.ResetPerRunState()
			t.Cleanup(checksums.ResetPerRunState)
			checksums.BeginRollbackJournal(root)
			t.Cleanup(checksums.CommitRollback)

			stderr, restore := captureStderr(t)
			rollbackGeneratedTree(root, os.ErrInvalid, tc.onlyReadOnlyRan)
			restore()
			out := stderr.String()
			if !strings.Contains(out, tc.want) {
				t.Errorf("report must say %q:\n%s", tc.want, out)
			}
			if strings.Contains(out, tc.notWant) || strings.Contains(out, "tree is unchanged") {
				t.Errorf("report must not say %q or claim an unchanged tree:\n%s", tc.notWant, out)
			}
		})
	}
}

// ReadOnly is what licenses "No files were changed", so the set of steps
// carrying it is pinned: adding one is a claim that the step writes nothing
// to the project tree by ANY route, and deserves review.
func TestGenerateStepsReadOnlySet(t *testing.T) {
	want := map[string]bool{
		"load project config":         true,
		"forge version compatibility": true,
		"pre-codegen contract check":  true,
		"retired ShellBuild tokens":   true,
		"legacy config proto layout":  true,
		"announce project":            true,
	}
	got := map[string]bool{}
	for _, s := range generateSteps() {
		if s.ReadOnly {
			got[s.Name] = true
		}
	}
	for name := range want {
		if !got[name] {
			t.Errorf("step %q should be ReadOnly", name)
		}
	}
	for name := range got {
		if !want[name] {
			t.Errorf("step %q is marked ReadOnly but not in the reviewed set", name)
		}
	}
	// Every refusal must precede the first step that can write.
	firstWriter := -1
	for i, s := range generateSteps() {
		if !s.ReadOnly {
			firstWriter = i
			break
		}
	}
	for i, s := range generateSteps() {
		if (s.Name == "forge version compatibility" || s.Name == "pre-codegen contract check" ||
			s.Name == "retired ShellBuild tokens") && i > firstWriter {
			t.Errorf("refusal %q runs at %d, after the first writing step at %d", s.Name, i, firstWriter)
		}
	}
}
