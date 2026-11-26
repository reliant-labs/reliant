package cli

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// `generate --steps <typo>` used to run the root's clone setup — which can
// rewrite the merge driver in the git config every worktree of the clone
// shares — and only then fail on the preset. The flag is checked when it is
// parsed, before any of that.
func TestGenerateStepsFlag_RejectsAnUnknownPresetAtParseTime(t *testing.T) {
	cmd := newGenerateCmd()
	err := cmd.ParseFlags([]string{"--steps", "no-such-preset"})
	if err == nil || !strings.Contains(err.Error(), `unknown pipeline step preset "no-such-preset"`) {
		t.Fatalf("ParseFlags(--steps no-such-preset) = %v, want the unknown-preset error", err)
	}
	for _, ok := range []string{"mocks", ""} {
		if err := newGenerateCmd().ParseFlags([]string{"--steps", ok}); err != nil {
			t.Errorf("ParseFlags(--steps %q) = %v", ok, err)
		}
	}
}

func TestGenerateStepsFlag_UnknownPresetLeavesTheCloneConfigAlone(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a real git repository; runs in task test")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	for _, env := range []string{"FORGE_NO_GIT_HOOKS", "FORGE_NO_HOOKS", "FORGE_NO_MERGE_DRIVER", delegatedDriverEnv} {
		t.Setenv(env, "")
	}
	prevExe := mergeDriverExecutable
	t.Cleanup(func() { mergeDriverExecutable = prevExe })
	mergeDriverExecutable = func() (string, error) { return "/opt/forge/bin/forge", nil }
	setPinCheck(t, false)
	newClone := func() string {
		repo := t.TempDir()
		gitT(t, repo, "init", "-q", "-b", "main")
		writeTreeFileT(t, filepath.Join(repo, "forge.yaml"), "name: svc\nmodule_path: example.com/svc\ngit:\n  merge_driver: auto\n")
		return repo
	}

	// The fixture is one a writing command wires, so the check below is
	// not vacuous.
	control := newClone()
	ensureGeneratedMergeDriverTo(io.Discard, control)
	if gitConfig(t, control, "merge.forge-generated.driver") == "" {
		t.Fatal("precondition: git.merge_driver: auto wires the clone")
	}

	repo := newClone()
	before, _ := os.ReadFile(filepath.Join(repo, ".git", "config"))
	t.Chdir(repo)
	root := newTestRootCmd(t)
	root.SetArgs([]string{"generate", "--steps", "no-such-preset"})
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "unknown pipeline step preset") {
		t.Fatalf("generate --steps no-such-preset = %v, want the unknown-preset error", err)
	}
	if after, _ := os.ReadFile(filepath.Join(repo, ".git", "config")); string(after) != string(before) {
		t.Errorf("a generate that failed on its flags rewrote the clone's git config:\n--- before\n%s\n--- after\n%s", before, after)
	}
	if _, err := os.Stat(filepath.Join(repo, ".git", "info", "attributes")); err == nil {
		t.Error("a generate that failed on its flags wrote info/attributes")
	}
}
