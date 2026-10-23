package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// `forge generate` renders KCL only to READ from it, but evaluating a module
// fires every file.write in it. When such a write lands on a TRACKED file,
// generate's committed output stops being a pure function of committed
// inputs — see internal/cli/kcl_render_purity.go for the control-plane
// incident that motivated the guard (a gitignored .forge/blocks.json key
// leaking a junk NATS account into a tracked deploy/nats/nats.conf).
//
// These tests exercise the guard's own logic — detect a tracked file that
// went clean → dirty across a render, restore it, leave everything else
// alone — against a real git repo, without needing the KCL toolchain to
// produce the side effect.

// initGitRepo creates a git repo at dir with one commit containing files.
// Identity is set locally (never --global): this must not touch the machine's
// git config, which is shared with everything else running here.
func initGitRepo(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		abs := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"config", "user.email", "forge-test@example.com"},
		{"config", "user.name", "forge test"},
		{"add", "."},
		{"commit", "--quiet", "-m", "fixture"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func writeGuardFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	abs := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func readGuardFile(t *testing.T, dir, rel string) string {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(got)
}

// TestDirtyTrackedFiles_SeesModifiedTrackedFileOnly pins the detector the
// guard's clean → dirty comparison rests on: it must see a modified tracked
// file, and must NOT see an untracked one. Untracked files are excluded on
// purpose — a render dropping a new gitignored file into .forge/ is ordinary,
// and treating it as a violation would make the guard fire constantly.
func TestDirtyTrackedFiles_SeesModifiedTrackedFileOnly(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir, map[string]string{
		"deploy/nats/nats.conf": "accounts { CP_DEFAULT: {} }\n",
		"README.md":             "fixture\n",
	})

	if dirty := dirtyTrackedFiles(dir); len(dirty) != 0 {
		t.Fatalf("clean repo reported dirty files: %v", dirty)
	}

	writeGuardFile(t, dir, "deploy/nats/nats.conf", "accounts { CP_DEFAULT: {} CP_prod_: {} }\n")
	writeGuardFile(t, dir, "untracked-side-effect.txt", "not under version control\n")

	dirty := dirtyTrackedFiles(dir)
	if !dirty["deploy/nats/nats.conf"] {
		t.Errorf("modified TRACKED file not reported: %v", dirty)
	}
	if dirty["untracked-side-effect.txt"] {
		t.Errorf("untracked file reported as dirty: %v", dirty)
	}
}

// TestDirtyTrackedFiles_NonRepoDisarms pins the fail-open contract: outside a
// git checkout the guard returns nil and the caller skips the comparison
// entirely. forge has to stay usable in a plain directory, and this is a
// diagnostic rather than a gate.
func TestDirtyTrackedFiles_NonRepoDisarms(t *testing.T) {
	if dirty := dirtyTrackedFiles(t.TempDir()); dirty != nil {
		t.Errorf("dirtyTrackedFiles outside a repo = %v, want nil (guard disarmed)", dirty)
	}
}

// TestRestoreTrackedFileFromHEAD_RevertsTheSideEffect is the core repair: the
// exact shape of the incident — an extra NATS account appended to a tracked
// conf — is undone byte-for-byte from HEAD.
func TestRestoreTrackedFileFromHEAD_RevertsTheSideEffect(t *testing.T) {
	const committed = "accounts {\n  CP_DEFAULT: {}\n}\n"
	dir := t.TempDir()
	initGitRepo(t, dir, map[string]string{"deploy/nats/nats.conf": committed})

	writeGuardFile(t, dir, "deploy/nats/nats.conf",
		"accounts {\n  CP_DEFAULT: {}\n  CP_prod_: { user: \"control-plane-prod-\" }\n}\n")

	if err := restoreTrackedFileFromHEAD(dir, "deploy/nats/nats.conf"); err != nil {
		t.Fatalf("restoreTrackedFileFromHEAD: %v", err)
	}
	if got := readGuardFile(t, dir, "deploy/nats/nats.conf"); got != committed {
		t.Errorf("file not restored to committed bytes:\n--- got ---\n%s\n--- want ---\n%s", got, committed)
	}
	if dirty := dirtyTrackedFiles(dir); len(dirty) != 0 {
		t.Errorf("tree still dirty after restore: %v", dirty)
	}
}

// TestRenderKCLPure_LeavesPreexistingEditsAlone is the safety property that
// makes restoring acceptable at all. This checkout is routinely shared with
// other agents, so the guard may only revert a file it PROVED was clean
// immediately before the render — a file that was already dirty going in
// carries someone's uncommitted work and must survive untouched.
//
// It drives the comparison directly rather than through renderKCLPure, which
// would need the KCL toolchain; the logic under test is the before/after set
// difference, and that is what this reproduces.
func TestRenderKCLPure_LeavesPreexistingEditsAlone(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir, map[string]string{
		"deploy/nats/nats.conf": "accounts { CP_DEFAULT: {} }\n",
		"internal/work.go":      "package work\n",
	})

	// Another agent's uncommitted edit, present BEFORE the render.
	const inFlight = "package work\n\n// half-finished change\n"
	writeGuardFile(t, dir, "internal/work.go", inFlight)
	before := dirtyTrackedFiles(dir)
	if !before["internal/work.go"] {
		t.Fatalf("fixture setup: pre-existing edit not seen as dirty: %v", before)
	}

	// The render's side effect lands on a DIFFERENT, previously-clean file.
	writeGuardFile(t, dir, "deploy/nats/nats.conf", "accounts { CP_DEFAULT: {} CP_prod_: {} }\n")

	var restored []string
	for path := range dirtyTrackedFiles(dir) {
		if before[path] {
			continue
		}
		if err := restoreTrackedFileFromHEAD(dir, path); err != nil {
			t.Fatalf("restore %s: %v", path, err)
		}
		restored = append(restored, path)
	}

	if len(restored) != 1 || restored[0] != "deploy/nats/nats.conf" {
		t.Errorf("restored = %v, want [deploy/nats/nats.conf] only", restored)
	}
	if got := readGuardFile(t, dir, "internal/work.go"); got != inFlight {
		t.Errorf("a pre-existing uncommitted edit was reverted — this would destroy another "+
			"agent's work:\n--- got ---\n%s\n--- want ---\n%s", got, inFlight)
	}
}
