// Copyright (c) 2025 Reliant Labs
package commands

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/reliant-labs/reliant/internal/daemoninstance"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/spf13/cobra"
)

// daemonInstanceFlags are the three components of an instance key as the daemon
// commands take them, plus the data-dir override that bypasses derivation
// entirely. Every daemon subcommand that touches a data directory embeds one of
// these and resolves it through resolveDaemonDataDir, so start, status, stop and
// logs cannot disagree about which directory they mean.
type daemonInstanceFlags struct {
	// dataDir, when non-empty, IS the answer — see resolveDaemonDataDir.
	dataDir   string
	account   string
	workspace string
}

// envDataDir is the container-facing data-directory override. docker-compose.yml
// and docker-compose.cloud.yml both set it to /data, where the volume is
// mounted; derivation would put the daemon's state on the container's ephemeral
// filesystem instead.
const envDataDir = "DAEMON_DATA_DIR"

// registerDaemonInstanceFlags wires the instance flags onto a command.
//
// --data-dir defaults to the empty string rather than to "./data". The old
// default was the whole bug: a cwd-relative path meant `daemon start` from the
// repo root and `daemon stop` from electron/ addressed two different daemons,
// and stop reported "No daemon running" over a live one. Empty means "derive",
// and derivation is stable no matter where the command is invoked from.
func registerDaemonInstanceFlags(cmd *cobra.Command, f *daemonInstanceFlags, dataDirUsage string) {
	cmd.Flags().StringVar(&f.dataDir, "data-dir", os.Getenv(envDataDir), dataDirUsage)
	cmd.Flags().StringVar(&f.account, "account", os.Getenv(envAccount),
		"Account (Supabase subject) this daemon runs as; selects among several accounts on one server")
	cmd.Flags().StringVar(&f.workspace, "workspace", "",
		"Workspace this daemon serves (defaults to the git worktree root of the current directory)")
}

// envAccount lets a container or CI job name the account without a flag, the
// same way RELIANT_INSTANCE_WORKSPACE names the workspace.
const envAccount = "RELIANT_INSTANCE_ACCOUNT"

// resolveDaemonInstance builds the instance key these flags name.
//
// The workspace precedence is explicit rather than inherited from
// daemoninstance.Resolve, whose own fallback is the current working directory.
// That fallback would reintroduce exactly the defect this change removes: two
// subdirectories of one worktree are two different working directories, so
// start and stop would again address two different instances.
//
//  1. --workspace                      the user said so
//  2. RELIANT_INSTANCE_WORKSPACE       containers and CI, where cwd means nothing
//  3. the git worktree root of cwd     every directory in a worktree is ONE instance
//  4. cwd                              only when there is no git repository at all
//
// Rule 3 is the one that matters: `reliant/` and `reliant/electron/` share a
// toplevel and therefore share an instance, while a second worktree has its own
// toplevel and is properly separate.
func resolveDaemonInstance(f daemonInstanceFlags, serverURL string) (daemoninstance.Key, error) {
	return daemoninstance.Resolve(serverURL, f.account, resolveWorkspace(f.workspace))
}

// resolveWorkspace applies the precedence above and always returns a non-empty
// value, so daemoninstance.Resolve never reaches its own cwd fallback.
func resolveWorkspace(explicit string) string {
	if w := strings.TrimSpace(explicit); w != "" {
		return w
	}
	if w := strings.TrimSpace(os.Getenv(daemoninstance.EnvWorkspace)); w != "" {
		return w
	}
	if root := gitWorktreeRoot(); root != "" {
		return root
	}
	// Not in a repository at all. cwd is the only thing left to name the
	// workspace by, and it is still better than failing — a daemon started
	// outside a repo is legitimate, it simply gets a cwd-shaped identity.
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return cwd
}

// gitWorktreeRoot returns the toplevel of the git worktree containing the
// current directory, or "" when there is none.
//
// Shelling out rather than reaching for a git library keeps this honest about
// worktrees: `git rev-parse --show-toplevel` reports the LINKED worktree's root
// inside a `git worktree add` checkout, which is precisely the boundary we want,
// and any reimplementation would have to rediscover that. Not being in a
// repository is an ordinary outcome, not an error, so a non-zero exit falls
// through to the caller's next option.
func gitWorktreeRoot() string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// resolveDaemonDataDir returns the directory this invocation's daemon state
// lives in.
//
// An explicit --data-dir or DAEMON_DATA_DIR wins outright and is returned
// untouched: containers mount a volume at /data and must keep working, and an
// operator who names a path has said something more specific than any
// derivation could. Otherwise the directory is derived from the instance key,
// which is the same for every subcommand and every working directory.
//
// The directory is created when derived. `daemon status` and `daemon stop`
// create it too, which is deliberate — an empty directory is a truthful "no
// daemon here", whereas the alternative is each subcommand growing its own
// not-exists special case and drifting apart again.
func resolveDaemonDataDir(f daemonInstanceFlags, serverURL string) (string, error) {
	if dir := strings.TrimSpace(f.dataDir); dir != "" {
		return dir, nil
	}

	key, err := resolveDaemonInstance(f, serverURL)
	if err != nil {
		return "", fmt.Errorf("resolving daemon instance: %w", err)
	}
	dir, err := key.EnsureDataDir()
	if err != nil {
		return "", err
	}
	logging.Debug("resolved daemon instance", "instance", key.Slug(), "data_dir", dir)
	return dir, nil
}
