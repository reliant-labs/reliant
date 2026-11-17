package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// runMergeDriverCmd runs `forge merge-driver <sub>` in dir.
func runMergeDriverCmd(t *testing.T, dir, sub string) (string, error) {
	t.Helper()
	t.Chdir(dir)
	cmd := newMergeDriverCmd()
	var out strings.Builder
	cmd.SetArgs([]string{sub})
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	err := cmd.Execute()
	return out.String(), err
}

// A merge queue's pool checkout sets FORGE_NO_MERGE_DRIVER so its gates never
// rewrite git config, which left it merging with whatever forge the shared
// config named. `merge-driver install` is the explicit step: it wires this
// forge in despite the opt-out, and `check` fails once that forge is gone.
func TestMergeDriverInstall_WiresAnOptedOutCheckoutAndCheckFailsWhenTheForgeIsGone(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories; runs in task test")
	}
	repo := pinTestRepo(t)
	setPinCheck(t, false)
	svc := filepath.Join(repo, "svc")
	exe := filepath.Join(t.TempDir(), "forge")
	// exe lives under the real temp dir; point TMPDIR elsewhere so the
	// wiring does not refuse it as an ephemeral binary.
	t.Setenv("TMPDIR", t.TempDir())
	writeTreeFileT(t, exe, "#!/bin/sh\n")
	if err := os.Chmod(exe, 0o755); err != nil {
		t.Fatal(err)
	}
	prevExe, prevID := mergeDriverExecutable, mergeDriverIdentity
	t.Cleanup(func() { mergeDriverExecutable, mergeDriverIdentity = prevExe, prevID })
	mergeDriverExecutable = func() (string, error) { return exe, nil }
	mergeDriverIdentity = func() string { return "7410e351b23b" }
	t.Setenv(mergeDriverOptOutEnv, "1")

	if out, err := runMergeDriverCmd(t, svc, "check"); err == nil {
		t.Fatalf("check passed in a checkout with no driver:\n%s", out)
	}
	out, err := runMergeDriverCmd(t, svc, "install")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if got := gitConfig(t, repo, "merge.forge-generated.driver"); !strings.Contains(got, exe) {
		t.Errorf("install under %s=1 did not wire this forge: %q", mergeDriverOptOutEnv, got)
	}
	if out, err := runMergeDriverCmd(t, svc, "check"); err != nil {
		t.Fatalf("check after install: %v\n%s", err, out)
	}

	if runtime.GOOS != "windows" {
		if err := os.Chmod(exe, 0o644); err != nil {
			t.Fatal(err)
		}
		if out, err := runMergeDriverCmd(t, svc, "check"); err == nil {
			t.Fatalf("check must reject a driver without execute permission:\n%s", out)
		}
		if out, err := runMergeDriverCmd(t, svc, "install"); err == nil {
			t.Fatalf("install must not retain a non-executable driver as ready:\n%s", out)
		}
		if err := os.Chmod(exe, 0o755); err != nil {
			t.Fatal(err)
		}
		if out, err := runMergeDriverCmd(t, svc, "install"); err != nil {
			t.Fatalf("install after restoring executable: %v\n%s", err, out)
		}
	}
	if err := os.Remove(exe); err != nil {
		t.Fatal(err)
	}
	out, err = runMergeDriverCmd(t, svc, "check")
	if err == nil || !strings.Contains(out, "gone") {
		t.Fatalf("check must fail, naming the gone forge: %v\n%s", err, out)
	}
}
