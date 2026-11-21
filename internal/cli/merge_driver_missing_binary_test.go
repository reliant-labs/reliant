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
// (127), and left every generated file conflicted with no word of why. A
// driver whose forge is gone reinstalls it, or fails the merge and says what
// to run; it never reports a merge forge did not do.

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

// A driver whose forge is gone and cannot be reinstalled must FAIL the
// merge, loudly. The fallback this replaces kept our side of a conflicting
// generated file and exited 0: git recorded a stale mock as cleanly merged,
// and nothing downstream knew to regenerate it.
func TestMergeDriver_GoneBinaryFailsTheMergeAndSaysWhatToRun(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories; runs in task test")
	}
	repo, exe := missingBinaryRepo(t)
	conflictingBranches(t, repo, "api_gen.go")
	if err := os.Remove(exe); err != nil {
		t.Fatal(err)
	}

	out, err := gitMerge(t, repo)
	if err == nil {
		t.Fatalf("a merge whose driver forge is gone succeeded:\n%s", out)
	}
	if strings.Contains(out, "not found") {
		t.Errorf("the merge printed a shell error instead of a message:\n%s", out)
	}
	for _, want := range []string{"FAILED", exe, "merge-driver install"} {
		if !strings.Contains(out, want) {
			t.Errorf("the merge output does not say %q:\n%s", want, out)
		}
	}
	got, _ := os.ReadFile(filepath.Join(repo, "api_gen.go"))
	if !strings.Contains(string(got), "<<<<<<<") || !strings.Contains(string(got), "theirs") || !strings.Contains(string(got), "ours") {
		t.Errorf("api_gen.go must carry both sides between conflict markers, not one side:\n%s", got)
	}
	if u := strings.TrimSpace(gitOut(t, repo, "diff", "--name-only", "--diff-filter=U")); u != "api_gen.go" {
		t.Errorf("git must record api_gen.go as unmerged; unmerged = %q", u)
	}
}

// Even where git's text merge would be clean, a merge forge did not run is
// not reported as merged.
func TestMergeDriver_GoneBinaryFailsEvenAMergeTextWouldSettle(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories; runs in task test")
	}
	repo, exe := missingBinaryRepo(t)
	writeTreeFileT(t, filepath.Join(repo, "api_gen.go"), "a\nb\nc\nd\ne\n")
	gitT(t, repo, "add", "-A")
	gitT(t, repo, "commit", "-qm", "base")
	gitT(t, repo, "checkout", "-qb", "other")
	writeTreeFileT(t, filepath.Join(repo, "api_gen.go"), "a\nb\nc\nd\nE\n")
	gitT(t, repo, "commit", "-qam", "other")
	gitT(t, repo, "checkout", "-q", "main")
	writeTreeFileT(t, filepath.Join(repo, "api_gen.go"), "A\nb\nc\nd\ne\n")
	gitT(t, repo, "commit", "-qam", "main")
	if err := os.Remove(exe); err != nil {
		t.Fatal(err)
	}
	if out, err := gitMerge(t, repo); err == nil {
		t.Fatalf("a merge forge never ran was reported clean:\n%s", out)
	}
}

// When the binary is gone but its version is known, the driver reinstalls
// that same forge into its durable-copy directory and merges with it.
func TestMergeDriver_GoneBinaryReinstallsItsOwnForge(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories; runs in task test")
	}
	const version = "v0.1.47-0.20261026081749-03f02d7dce78"
	cache := t.TempDir()
	prevVer, prevCache := mergeDriverVersion, mergeDriverCacheDir
	t.Cleanup(func() { mergeDriverVersion, mergeDriverCacheDir = prevVer, prevCache })
	mergeDriverVersion = func() string { return version }
	mergeDriverCacheDir = func() (string, error) { return cache, nil }
	// A `go` that "installs" a forge which merges by writing a marker line.
	fakeBin := t.TempDir()
	writeTreeFileT(t, filepath.Join(fakeBin, "go"), `#!/bin/sh
[ "$1" = install ] || exit 2
case "$2" in */cmd/forge@`+version+`) ;; *) exit 3;; esac
mkdir -p "$GOBIN"
printf '#!/bin/sh\nwhile [ "$1" != merge-generated ]; do shift; done\nprintf "from-reinstalled\\n" > "$3"\n' > "$GOBIN/forge"
chmod +x "$GOBIN/forge"
`)
	if err := os.Chmod(filepath.Join(fakeBin, "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	repo, exe := missingBinaryRepo(t)
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	conflictingBranches(t, repo, "api_gen.go")
	if err := os.Remove(exe); err != nil {
		t.Fatal(err)
	}

	out, err := gitMerge(t, repo)
	if err != nil {
		t.Fatalf("the driver did not merge with its reinstalled forge: %v\n%s", err, out)
	}
	got, _ := os.ReadFile(filepath.Join(repo, "api_gen.go"))
	if string(got) != "from-reinstalled\n" {
		t.Errorf("api_gen.go = %q, want the reinstalled forge's merge", got)
	}
	if _, err := os.Stat(filepath.Join(cache, "03f02d7dce78", "forge")); err != nil {
		t.Errorf("the reinstalled forge is not in the durable-copy directory: %v", err)
	}
}

// Committed state is not a pure function of merged inputs: a fallback must
// not discard the other side's entries, so overlapping edits stay a normal
// conflict with markers.
func TestMergeDriver_GoneBinaryStillConflictsOnStateFiles(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories; runs in task test")
	}
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

// The forge a driver names can be a build of a checkout or of a dirty tree —
// forgedelegate's pinned/checkout-* entries, evicted as newer builds arrive —
// which names no version to reinstall. The driver then resolves forge at
// merge time: the entry point on PATH runs the merge (and, outside a test,
// hands it to the forge the project pins). Forge reports 8020c931, 3bd41c08,
// a64b1b81: every merge after the eviction failed instead.
func TestMergeDriver_GoneUninstallableBinaryMergesWithTheForgeOnPath(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories; runs in task test")
	}
	prevVer, prevLaunchers := mergeDriverVersion, driverLaunchers
	t.Cleanup(func() { mergeDriverVersion, driverLaunchers = prevVer, prevLaunchers })
	mergeDriverVersion = func() string { return "v1.8.1-0.20261120064152-ccaa932a1c8d+dirty" }
	driverLaunchers = func(string) [][]string {
		return [][]string{{"forge-test-absent-launcher"}, {"forge-test-launcher", "forge"}}
	}
	launcherDir := t.TempDir()
	writeTreeFileT(t, filepath.Join(launcherDir, "forge-test-launcher"),
		"#!/bin/sh\n[ \"$1\" = forge ] || exit 3\nwhile [ \"$1\" != merge-generated ]; do shift; done\nprintf 'from-launcher\\n' > \"$3\"\n")
	if err := os.Chmod(filepath.Join(launcherDir, "forge-test-launcher"), 0o755); err != nil {
		t.Fatal(err)
	}
	repo, exe := missingBinaryRepo(t)
	if command := gitOut(t, repo, "config", "--get", "merge.forge-generated.driver"); strings.Contains(command, "go install") {
		t.Fatalf("a dirty build's driver reinstalls it, which cannot work: %s", command)
	}
	t.Setenv("PATH", launcherDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	conflictingBranches(t, repo, "api_gen.go")
	if err := os.Remove(exe); err != nil {
		t.Fatal(err)
	}

	out, err := gitMerge(t, repo)
	if err != nil {
		t.Fatalf("the driver did not merge with the forge on PATH: %v\n%s", err, out)
	}
	if got, _ := os.ReadFile(filepath.Join(repo, "api_gen.go")); string(got) != "from-launcher\n" {
		t.Errorf("api_gen.go = %q, want the PATH forge's merge", got)
	}
	if !strings.Contains(out, "`forge-test-launcher forge` from PATH") {
		t.Errorf("the merge does not say which forge merged:\n%s", out)
	}
}

func TestDefaultDriverLaunchers(t *testing.T) {
	for name, want := range map[string]string{
		"forge":          "forge|reliant forge",
		"reliant forge":  "reliant forge|forge",
		"cli.test forge": "cli.test forge|forge|reliant forge",
		"it's forge":     "forge|reliant forge", // a name the shell would misread is skipped
	} {
		var got []string
		for _, l := range defaultDriverLaunchers(name) {
			got = append(got, strings.Join(l, " "))
		}
		if strings.Join(got, "|") != want {
			t.Errorf("defaultDriverLaunchers(%q) = %q, want %q", name, strings.Join(got, "|"), want)
		}
	}
}

func TestMergeDriver_PresentBinaryStillRunsFirst(t *testing.T) {
	if testing.Short() {
		t.Skip("builds real git repositories; runs in task test")
	}
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
	if err := os.Chmod(bin, 0o755); err != nil {
		t.Fatal(err)
	}
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
