package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/generator"
)

// skipHookActivationAnnotation marks a command during which the root
// pre-run must NOT self-activate git hooks. Two kinds carry it:
//
//   - `forge project new`, which owns git setup for the project it creates:
//     a fresh repository gets core.hooksPath from initGitRepository, and a
//     repository that already existed is the user's — its config is not
//     forge's to change. (Without this, a `--force` re-run found the
//     scaffold's forge.yaml + .githooks and activated them.)
//   - the hidden protoc-gen-forge plugin, which buf runs as a subprocess.
//     It is not a user command: in a normal `forge generate` the parent
//     already made the offer, and during `project new`'s bootstrap it would
//     have written core.hooksPath into the user's existing repository.
//
// The offer is still made by the user's next ordinary forge command.
const skipHookActivationAnnotation = "forge.skip-hook-activation"

// hookActivationSkipped reports whether cmd opted out of hook activation.
func hookActivationSkipped(cmd *cobra.Command) bool {
	_, ok := cmd.Annotations[skipHookActivationAnnotation]
	return ok
}

// gitHooksOptOutEnv stops forge activating its tracked git hooks in this
// clone (core.hooksPath=.githooks). Any non-empty value opts out.
//
// It is about GIT hooks only. The commands a project declares in forge.yaml
// `generate.hooks` are part of `forge generate` itself and always run (see
// generate_hooks.go). The merge driver for generated files is not a hook
// either, and has its own opt-out, FORGE_NO_MERGE_DRIVER (merge_generated.go).
const gitHooksOptOutEnv = "FORGE_NO_GIT_HOOKS"

// legacyGitHooksOptOutEnv is gitHooksOptOutEnv's earlier spelling, still
// honoured so a clone opted out under it stays opted out. It was renamed
// because "hooks" read as forge.yaml's generate hooks too: people set it
// expecting `forge generate` to skip `make sqlc`, and it never did.
const legacyGitHooksOptOutEnv = "FORGE_NO_HOOKS"

// gitHooksOptOut returns the variable that opts this process out of forge
// configuring git hooks, or "" when neither is set.
func gitHooksOptOut() string {
	for _, k := range []string{gitHooksOptOutEnv, legacyGitHooksOptOutEnv} {
		if os.Getenv(k) != "" {
			return k
		}
	}
	return ""
}

// ensureGitHooksActivated makes forge's committed git hooks fire in a
// fresh clone or new worktree without any explicit install step or new
// forge subcommand.
//
// The hook SCRIPT (.githooks/pre-commit, written by the generator)
// travels with the repo because it's tracked. The ACTIVATION —
// core.hooksPath — is LOCAL git config that `git clone` deliberately does
// not carry (git won't let a repo auto-run hooks on clone; that's the
// same reason tools like Husky exist). So we set it idempotently as a
// cheap side-effect of a forge command that writes the project
// (cmdutil.ConfiguresClone): the first `forge generate` / `scaffold` after a
// clone activates the hooks. Read-only commands never touch .git/config. Worktrees of an already-activated repo inherit it via the shared
// .git/config, so they need nothing.
//
// Deliberately conservative — it must never surprise the user or block a
// command:
//   - no-op unless we're in a forge project that ships .githooks/pre-commit
//     (skips pre-feature projects until their next `forge generate`),
//   - no-op unless that pre-commit is FORGE'S (generator.IsForgeManagedGitHook).
//     `project new --in-place` keeps a repository's own .githooks/pre-commit,
//     and activating it would switch on the project's hooks (a format-on-commit,
//     a test run) for everyone sharing the clone's .git/config — every linked
//     worktree. Whether a project's own hooks run is its own setup's call,
//   - no-op unless it's a git repo,
//   - never clobbers a core.hooksPath the user pointed elsewhere; only
//     acts when it is unset,
//   - fully suppressible with FORGE_NO_GIT_HOOKS=1 (gitHooksOptOut),
//   - best-effort: any git error is swallowed, never surfaced.
//
// root is the project root (the directory holding forge.yaml).
func ensureGitHooksActivated(root string) {
	if gitHooksOptOut() != "" {
		return
	}
	// Only when the tracked hook is present AND forge wrote it. This gates
	// the whole feature on the generator having written .githooks/ —
	// pre-feature projects pick it up on their next `forge generate` — and
	// leaves a project's own hooks to the project.
	script, err := os.ReadFile(filepath.Join(root, ".githooks", "pre-commit"))
	if err != nil || !generator.IsForgeManagedGitHook(script) {
		return
	}
	// Must be a git repo. In a linked worktree .git is a file (a gitdir
	// pointer), not a directory, so Stat (not a dir check) is correct.
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return
	}
	// Respect an existing setting at any level — if the user (or a prior
	// run) already pointed core.hooksPath somewhere, leave it alone. Only
	// an unset value is ours to fill.
	cur, _ := exec.CommandContext(context.Background(), "git", "-C", root, "config", "--get", "core.hooksPath").Output()
	if strings.TrimSpace(string(cur)) != "" {
		return
	}
	// --local writes the repository config (the shared .git/config for
	// worktrees), so one write covers every linked worktree.
	if err := exec.CommandContext(context.Background(), "git", "-C", root, "config", "--local", "core.hooksPath", ".githooks").Run(); err != nil {
		return
	}
	fmt.Fprintln(os.Stderr,
		"forge: activated git hooks (.githooks) — 'git push' now runs 'forge lint' + 'forge project audit'. Disable with "+gitHooksOptOutEnv+"=1.")
}
