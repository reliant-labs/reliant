// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	configv1 "github.com/reliant-labs/reliant/gen/config/v1"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/daemoninstance"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/toolexec/bootstrap"
	"github.com/reliant-labs/reliant/internal/toolexec/transport"
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

// daemonInstance is the instance a daemon command's config names
// (DaemonInstanceConfig: --data-dir/DAEMON_DATA_DIR, --account/
// RELIANT_INSTANCE_ACCOUNT, --workspace/RELIANT_INSTANCE_WORKSPACE).
//
// The data directory defaults to the empty string rather than to "./data". The
// old default was the whole bug: a cwd-relative path meant `daemon start` from
// the repo root and `daemon stop` from electron/ addressed two different
// daemons, and stop reported "No daemon running" over a live one. Empty means
// "derive", and derivation is stable no matter where the command is invoked
// from. Containers set DAEMON_DATA_DIR to their mounted volume
// (docker-compose.yml, the workspace operator), where derivation would put the
// daemon's state on the ephemeral filesystem.
func daemonInstance(cfg *configv1.DaemonInstanceConfig) daemonInstanceFlags {
	return daemonInstanceFlags{
		dataDir:   cfg.GetDataDir(),
		account:   cfg.GetAccount(),
		workspace: cfg.GetWorkspace(),
	}
}

// resolveDaemonInstance builds the instance key these flags name.
//
// The workspace precedence is explicit rather than inherited from
// daemoninstance.Resolve, whose own fallback is the current working directory.
// That fallback would reintroduce exactly the defect this change removes: two
// subdirectories of one worktree are two different working directories, so
// start and stop would again address two different instances.
//
//  1. --workspace, else RELIANT_INSTANCE_WORKSPACE   the user, or a container or
//     CI job where cwd means nothing, said so (the command's config)
//  2. the git worktree root of cwd     every directory in a worktree is ONE instance
//  3. cwd                              only when there is no git repository at all
//
// Rule 2 is the one that matters: `reliant/` and `reliant/electron/` share a
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
// lives in, for the commands that only look at a daemon: status, stop, logs.
//
// An explicit --data-dir or DAEMON_DATA_DIR wins outright and is returned
// untouched: containers mount a volume at /data and must keep working, and an
// operator who names a path has said something more specific than any
// derivation could. Otherwise the directory is derived from the instance key,
// which is the same for every subcommand and every working directory.
//
// It creates nothing. A directory that does not exist reads exactly like an
// empty one — no runtime record, no log — so each command already reports "no
// daemon here" truthfully; creating it made `daemon ls` list an instance that
// had only ever been asked about. Only `daemon start` creates (daemonDataDir).
func resolveDaemonDataDir(f daemonInstanceFlags, serverURL string) (string, error) {
	f.account = resolveInstanceAccount(f.account, serverURL)
	if dir := strings.TrimSpace(f.dataDir); dir != "" {
		return dir, nil
	}
	key, err := resolveDaemonInstance(f, serverURL)
	if err != nil {
		return "", fmt.Errorf("resolving daemon instance: %w", err)
	}
	return key.DataDir()
}

// resolveInstanceAccount returns the account component of the instance key:
// the account whose credential the daemon runs with.
//
//  1. --account / RELIANT_INSTANCE_ACCOUNT    the caller said so
//  2. the stored credential's account         what a no-flag `daemon start` will run as
//  3. ""                                      no known account: the _default instance
//
// Rule 2 is what keeps the subcommands agreeing. `daemon start --token` files
// the pasted credential under its owner (see accountForPastedToken) and runs
// in that owner's instance; a later `daemon status` or `daemon stop` with no
// flags must land on the same directory, or it reports "No daemon running"
// over a live one. The stored default is the most recently registered account
// (auth.WriteDaemonCredentials), so the no-flag commands follow the account
// the user last connected with.
func resolveInstanceAccount(explicit, serverURL string) string {
	if account := strings.TrimSpace(explicit); account != "" {
		return account
	}
	creds, err := auth.ReadDaemonCredentials(serverURL, "")
	if err != nil || creds == nil {
		return ""
	}
	if sub := strings.TrimSpace(creds.Sub); sub != auth.DefaultAccount {
		return sub
	}
	return ""
}

// daemonGateway is where a daemon dials and how.
type daemonGateway struct {
	url     string
	tlsMode bootstrap.TLSMode
}

// daemonWhoAmITimeout bounds the identity probe. It is one unary round trip;
// a gateway that cannot answer in this long will not be connected to either,
// and the fallback (the shared default identity) still starts the daemon.
const daemonWhoAmITimeout = 15 * time.Second

// accountForPastedToken asks the gateway whose token was pasted, so `daemon
// start --token` can run in that account's instance.
//
// This is what lets one laptop serve several accounts. The instance — data
// directory, lock, logs and the saved daemon id — is keyed by (server,
// account, workspace), and a pasted token used to carry no account, so every
// account landed in `_default` and shared ONE saved daemon id. The gateway
// refuses an id its owner does not hold, so whichever account connected second
// was told "daemon id is owned by another user" on every start.
//
// verified reports that the gateway accepted the token. Outcomes:
//
//   - identified: the owner is the account; the token is known good.
//   - refused (Unauthenticated): a bad token, said now and before anything is
//     written — interactively. A non-interactive start proceeds instead,
//     because the runtime retries Unauthenticated: a gateway mid-rollout also
//     refuses good tokens, and a container should still be trying when it
//     recovers.
//   - anything else (a gateway too old to have WhoAmI, or unreachable): start
//     anyway in the shared default instance. The runtime's foreign-id
//     recovery keeps that from wedging.
func accountForPastedToken(ctx context.Context, cmd *cobra.Command, conn *connection, gateway daemonGateway, token string, nonInteractive bool) (account string, verified bool, err error) {
	owner, err := whoAmI(ctx, gateway, token)
	switch {
	case err == nil:
		return owner, true, nil
	case connect.CodeOf(err) == connect.CodeUnauthenticated && !nonInteractive:
		return "", false, tokenRejectedError(conn, connect.CodeOf(err))
	default:
		fmt.Fprintf(cmd.ErrOrStderr(),
			"  ! Could not confirm which account this token belongs to (%v).\n"+
				"    Continuing with this machine's shared default identity.\n", err)
		return "", false, nil
	}
}

// whoAmI asks the gateway which account token acts as.
func whoAmI(ctx context.Context, gateway daemonGateway, token string) (string, error) {
	httpClient, baseURL, err := transport.NewDaemonHTTPClient(bootstrap.DaemonBootstrapConfig{
		AuthToken: token,
		GRPCURL:   gateway.url,
		TLSMode:   gateway.tlsMode,
	})
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, daemonWhoAmITimeout)
	defer cancel()
	client := reliantv1connect.NewToolsDaemonServiceClient(httpClient, baseURL, connect.WithGRPC())
	resp, err := client.WhoAmI(ctx, connect.NewRequest(&reliantv1.WhoAmIRequest{}))
	if err != nil {
		return "", err
	}
	owner := strings.TrimSpace(resp.Msg.GetUserId())
	if owner == "" {
		return "", fmt.Errorf("the gateway named no account for this token")
	}
	return owner, nil
}

// tokenRejectedError is what a user who pasted a token hears when the gateway
// refuses the token itself — and only then. A refused daemon id is a different
// failure with a different remedy (see daemonStartFailure).
func tokenRejectedError(conn *connection, code connect.Code) error {
	return fmt.Errorf("token rejected by gateway %s (%s) — verify the PAT is correct, not revoked, and was minted by %s",
		conn.describeGateway(), code.String(), conn.describeServer())
}

// daemonDataDir derives `daemon start`'s data directory from f exactly as
// given, creating it; the caller resolves the account first.
func daemonDataDir(f daemonInstanceFlags, serverURL string) (string, error) {
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
