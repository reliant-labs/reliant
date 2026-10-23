package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A driver command names one forge binary by absolute path, and that binary
// can vanish: the durable copies are pruned to the few most recently used
// identities on the machine, and a worktree on an older pin outlives its
// copy. git then ran `sh -c "'<gone>' merge-generated ..."`, got "not found"
// (127), and left every generated file conflicted. A driver whose forge is
// gone must still merge sanely and say what to run.

func missingBinaryRepo(t *testing.T) (repo, exe string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	// exe lives under the real temp dir; point TMPDIR elsewhere so the
	// driver wiring does not refuse it as an ephemeral binary.
	exeDir := t.TempDir()
	t.Setenv("TMPDIR", t.TempDir())
	for _, env := range []string{"FORGE_NO_GIT_HOOKS", "FORGE_NO_HOOKS", "FORGE_NO_MERGE_DRIVER", delegatedDriverEnv} {
		t.Setenv(env, "")
	}
	exe = filepath.Join(exeDir, "forge")
	writeTreeFileT(t, exe, "#!/bin/sh\n# args: [forge] merge-generated %O %A %B %L %P\nwhile [ \"$1\" != merge-generated ]; do shift; done\nprintf 'from-binary\\n' > \"$3\"\n")
	if err := os.Chmod(exe, 0o755); err != nil {
		t.Fatal(err)
	}
	prev := mergeDriverExecutable
	t.Cleanup(func() { mergeDriverExecutable = prev })
	mergeDriverExecutable = func() (string, error) { return exe, nil }

	repo = t.TempDir()
	gitT(t, repo, "init", "-q", "-b", "main")
	command, ok := mergeDriverCommand(false)
	if !ok {
		t.Fatal("mergeDriverCommand refused a durable binary")
	}
	gitT(t, repo, "config", "merge.forge-generated.driver", command)
	writeTreeFileT(t, filepath.Join(repo, ".git", "info", "attributes"),
		"*_gen.go merge=forge-generated\n.forge/hashes.json merge=forge-generated\n")
	return repo, exe
}

// conflictingBranches commits base on main, then makes branch "other" and
// main both rewrite the same line of each file.
func conflictingBranches(t *testing.T, repo string, files ...string) {
	t.Helper()
	for _, f := range files {
		writeTreeFileT(t, filepath.Join(repo, f), "line\n")
	}
	gitT(t, repo, "add", "-A")
	gitT(t, repo, "commit", "-qm", "base")
	gitT(t, repo, "checkout", "-qb", "other")
	for _, f := range files {
		writeTreeFileT(t, filepath.Join(repo, f), "theirs\n")
	}
	gitT(t, repo, "commit", "-qam", "other")
	gitT(t, repo, "checkout", "-q", "main")
	for _, f := range files {
		writeTreeFileT(t, filepath.Join(repo, f), "ours\n")
	}
	gitT(t, repo, "commit", "-qam", "main")
}

func gitMerge(t *testing.T, repo string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", "-c", "user.name=t", "-c", "user.email=t@t", "-c", "commit.gpgsign=false",
		"merge", "--no-edit", "other")
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestMergeDriver_GoneBinaryKeepsOurGeneratedFileAndSaysWhatToRun(t *testing.T) {
	repo, exe := missingBinaryRepo(t)
	conflictingBranches(t, repo, "api_gen.go")
	if err := os.Remove(exe); err != nil {
		t.Fatal(err)
	}

	out, err := gitMerge(t, repo)
	if err != nil {
		t.Fatalf("a generated file must not stay conflicted because the driver's forge is gone: %v\n%s", err, out)
	}
	if strings.Contains(out, "not found") {
		t.Errorf("the merge printed a shell error instead of a message:\n%s", out)
	}
	if !strings.Contains(out, "forge generate") {
		t.Errorf("the merge did not say to run forge generate:\n%s", out)
	}
	got, _ := os.ReadFile(filepath.Join(repo, "api_gen.go"))
	if string(got) != "ours\n" {
		t.Errorf("api_gen.go = %q, want our side", got)
	}
}

// Committed state is not a pure function of merged inputs: a fallback must
// not discard the other side's entries, so overlapping edits stay a normal
// conflict with markers.
func TestMergeDriver_GoneBinaryStillConflictsOnStateFiles(t *testing.T) {
	repo, exe := missingBinaryRepo(t)
	conflictingBranches(t, repo, ".forge/hashes.json")
	if err := os.Remove(exe); err != nil {
		t.Fatal(err)
	}

	if out, err := gitMerge(t, repo); err == nil {
		t.Fatalf("overlapping edits to a state file merged silently:\n%s", out)
	}
	got, _ := os.ReadFile(filepath.Join(repo, ".forge", "hashes.json"))
	if !strings.Contains(string(got), "<<<<<<<") || !strings.Contains(string(got), "theirs") {
		t.Errorf("hashes.json lacks conflict markers carrying both sides:\n%s", got)
	}
}

func TestMergeDriver_PresentBinaryStillRunsFirst(t *testing.T) {
	repo, _ := missingBinaryRepo(t)
	conflictingBranches(t, repo, "api_gen.go")

	out, err := gitMerge(t, repo)
	if err != nil {
		t.Fatalf("merge: %v\n%s", err, out)
	}
	_ = out
	got, _ := os.ReadFile(filepath.Join(repo, "api_gen.go"))
	if string(got) != "from-binary\n" {
		t.Errorf("api_gen.go = %q, want the driver binary's result", got)
	}
}

func TestDriverCommandUsable_ReadsTheBinaryOutOfBothShapes(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "for ge")
	writeTreeFileT(t, bin, "#!/bin/sh\n")
	legacy := "'" + bin + "' merge-generated %O %A %B %L %P"
	if !driverCommandUsable(legacy) {
		t.Errorf("a command in the original shape is unusable: %q", legacy)
	}
	guarded := driverCommandLine(bin, "forge")
	if !driverCommandUsable(guarded) {
		t.Errorf("a guarded command naming a live binary is unusable: %q", guarded)
	}
	if err := os.Remove(bin); err != nil {
		t.Fatal(err)
	}
	if driverCommandUsable(guarded) || driverCommandUsable(legacy) {
		t.Error("a command whose binary is gone reads as usable, so it would never be repaired")
	}
}
