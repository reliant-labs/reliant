//go:build e2e

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/goexec"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// TestE2EScaffoldFirstCommitPassesItsHooks is a new user's first commit: a
// fresh `forge project new` with a service and a Next.js frontend, then
// `git commit` through the git hooks the scaffold activates, which run every
// hook in the scaffolded .pre-commit-config.yaml and then commitlint.
//
// Each hook here once failed a pristine scaffold, before the user had changed
// a line:
//
//   - check-yaml rejected the frontend's pnpm-lock.yaml, which pnpm 12 writes
//     as two YAML documents.
//   - prettier rewrote dozens of scaffold files that `forge lint` had
//     accepted: templates formatted at one print width inside frontends that
//     configure another, and a lockfile it should never have touched. A hook
//     that modifies files fails the commit.
//   - commitlint came from a hook repository that npm installs as a git
//     dependency, which npm's EALLOWGIT policy refuses.
//
// The commit must succeed AND leave the tree clean, the hooks must have
// actually run (a pre-commit missing from PATH makes the chained hooks
// no-ops, which would pass vacuously), and commitlint must still reject a
// message that is not a Conventional Commit.
func TestE2EScaffoldFirstCommitPassesItsHooks(t *testing.T) {
	testkit.Slow(t, "scaffolds a project with a frontend and runs every scaffolded pre-commit hook, "+
		"which downloads the hook environments (gitleaks, prettier, commitlint)")
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	requireTool(t, "git", "go", "node", "npm", "pnpm", "buf", "goimports", "pre-commit")

	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	// No git identity while scaffolding, so `project new` initializes and
	// stages the repository but leaves the first commit to the user — the
	// commit this test makes. With an identity it commits itself, with
	// --no-verify. GIT_CONFIG_GLOBAL replaces ~/.gitconfig (HOME, and so the
	// Go and pnpm caches, stays put); useConfigOnly stops git guessing an
	// address from the hostname.
	gitConfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(gitConfig, []byte("[user]\n\tuseConfigOnly = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"GIT_CONFIG_GLOBAL=" + gitConfig, "GIT_CONFIG_NOSYSTEM=1"}
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		switch key {
		case "GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL",
			"GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM", "FORGE_NO_GIT_HOOKS":
			continue
		}
		env = append(env, kv)
	}
	run := func(dir string, extraEnv []string, name string, args ...string) (string, error) {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		cmd.Env = goexec.WithTrimpath(append(append([]string{}, env...), extraEnv...))
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	if out, err := run(dir, []string{"GOFLAGS="}, forgeBin, "project", "new", "hookapp",
		"--mod", "example.com/hookapp", "--service", "notes", "--frontend", "web"); err != nil {
		t.Fatalf("forge project new: %v\n%s", err, out)
	}
	projectDir := filepath.Join(dir, "hookapp")

	if out, _ := run(projectDir, nil, "git", "rev-parse", "--verify", "HEAD"); !strings.Contains(out, "fatal") {
		t.Fatalf("forge project new committed without a git identity, so the hooks under test never see the scaffold:\n%s", out)
	}
	if out, err := run(projectDir, nil, "git", "config", "core.hooksPath"); err != nil || strings.TrimSpace(out) != ".githooks" {
		t.Fatalf("the scaffold did not activate its git hooks (core.hooksPath = %q, %v)", strings.TrimSpace(out), err)
	}

	identity := []string{
		"GIT_AUTHOR_NAME=forge-e2e", "GIT_AUTHOR_EMAIL=e2e@forge.local",
		"GIT_COMMITTER_NAME=forge-e2e", "GIT_COMMITTER_EMAIL=e2e@forge.local",
	}
	out, err := run(projectDir, identity, "git", "commit", "-m", "chore: initial commit from forge")
	if err != nil {
		status, _ := run(projectDir, nil, "git", "status", "--short")
		diff, _ := run(projectDir, nil, "git", "diff")
		t.Fatalf("the first commit of a pristine scaffold failed its own hooks: %v\n%s\n"+
			"files the hooks changed:\n%s\n%s", err, out, status, diff)
	}
	// Every hook of the scaffolded config, by the name pre-commit prints, must
	// have run and passed — a "(no files to check)Skipped" line here means a
	// hook's file selection no longer reaches the scaffold.
	for _, hook := range []string{
		"check yaml", "check json", "trim trailing whitespace", "fix end of files", "Detect hardcoded secrets",
		"go fmt", "go imports", "prettier", "go vet (module)", "buf format", "commitlint",
	} {
		if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(hook) + `\.+Passed$`).MatchString(out) {
			t.Errorf("hook %q did not run and pass on the first commit:\n%s", hook, out)
		}
	}
	if status, _ := run(projectDir, nil, "git", "status", "--porcelain"); strings.TrimSpace(status) != "" {
		t.Errorf("the first commit left changes behind:\n%s", status)
	}

	out, err = run(projectDir, identity, "git", "commit", "--allow-empty", "-m", "fixture")
	if err == nil || !strings.Contains(out, "type-empty") {
		t.Errorf("commitlint accepted a message that is not a Conventional Commit (err %v):\n%s", err, out)
	}
}
