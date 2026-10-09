// Copyright (c) 2025 Reliant Labs
package commands

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/cloudcred"
	"github.com/spf13/cobra"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/cliauth"
	"github.com/reliant-labs/reliant/internal/forgecred"
	"github.com/reliant-labs/reliant/internal/logging"
)

// ── forge's credentials come from the Reliant session ─────────────────
//
// forge resolves a control-plane credential from --token, its declared token
// variable, then `forge login` — and then the credential helper named by
// $FORGE_CREDENTIAL_HELPER (forge/pkg/cloudcred). reliant IS that helper:
// `reliant auth forge-credential` exchanges this machine's Reliant session for
// a short-lived token (internal/forgecred). Two places point forge at it:
//
//   - `reliant forge …` sets the variable for its own process (unpinned: any
//     session on the machine, the CLI's resolved server first);
//   - a running daemon sets it in its own environment, PINNED to itself, so
//     every shell an agent runs — and the Deploy button's `reliant forge`
//     re-exec — acts as that daemon. Confined (connector) children never see
//     it: daemonpolicy's allowlist does not carry it, so a third-party caller
//     cannot mint deploy authority through the user's daemon.
//
// A value the user set themselves is never overwritten.

const (
	flagForgeCredServer  = "daemon-server"
	flagForgeCredAccount = "daemon-account"
)

// forgeCredentialTimeout bounds one helper run end to end. forge bounds the
// helper too; this keeps a hung server from holding the helper past it.
const forgeCredentialTimeout = 45 * time.Second

// forgeHelperArgv is the helper command for this binary, optionally pinned to
// one daemon session.
func forgeHelperArgv(server, account string) ([]string, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locating the reliant binary for forge's credential helper: %w", err)
	}
	argv := []string{exe, "auth", "forge-credential"}
	if strings.TrimSpace(server) != "" {
		argv = append(argv, "--"+flagForgeCredServer, server)
		if strings.TrimSpace(account) != "" {
			argv = append(argv, "--"+flagForgeCredAccount, account)
		}
	}
	return argv, nil
}

// daemonForgeHelper remembers the value this process exported, so a later
// credential refresh may replace its own value but never a user's.
var daemonForgeHelper struct {
	sync.Mutex
	value string
}

// exportForgeCredentialHelper points every first-party child of this daemon at
// the helper, pinned to the daemon's session. Best-effort: forge falls back to
// its own resolution and says how to sign in if this is missing.
func exportForgeCredentialHelper(server, account string) {
	argv, err := forgeHelperArgv(server, account)
	if err != nil {
		logging.Warn("forge will not reuse this daemon's session", "error", err)
		return
	}
	value := cloudcred.FormatCommand(argv)
	daemonForgeHelper.Lock()
	defer daemonForgeHelper.Unlock()
	current := os.Getenv(cloudcred.HelperEnv)
	if current != "" && current != daemonForgeHelper.value {
		logging.Info("leaving the user's forge credential helper in place", "env", cloudcred.HelperEnv)
		return
	}
	if err := os.Setenv(cloudcred.HelperEnv, value); err != nil {
		logging.Warn("forge will not reuse this daemon's session", "error", err)
		return
	}
	daemonForgeHelper.value = value
}

// offerSessionToForge is what a daemon does with a resolved credential for
// forge: point its children at the helper, pinned to this session, and clear
// any token an older release copied into forge's file.
func offerSessionToForge(conn *connection, server, account string) {
	if strings.TrimSpace(server) == "" {
		server = conn.ServerURL
	}
	exportForgeCredentialHelper(server, account)
	pinGitCredentialHelper(server, account)
	retireLegacyForgeDeposits()
}

// retireLegacyForgeDeposits removes what older releases deposited into
// forge's credentials file. Best-effort: forge ignores those entries anyway.
func retireLegacyForgeDeposits() {
	if n, err := cliauth.RemoveLegacyForgeDeposits(); err != nil {
		logging.Warn("could not remove legacy Reliant deposits from forge's credentials file", "error", err)
	} else if n > 0 {
		logging.Info("removed legacy Reliant deposits from forge's credentials file", "count", n)
	}
}

// useReliantSessionForForge makes `reliant forge …` find the session: it sets
// the helper for this process when nothing set one (a daemon's pinned value,
// or the user's own, wins).
func useReliantSessionForForge() {
	if strings.TrimSpace(os.Getenv(cloudcred.HelperEnv)) != "" {
		return
	}
	argv, err := forgeHelperArgv("", "")
	if err != nil {
		logging.Debug("forge will not reuse the Reliant session", "error", err)
		return
	}
	_ = os.Setenv(cloudcred.HelperEnv, cloudcred.FormatCommand(argv))
}

// forgeTokenCachePath is where exchanged tokens are cached between forge
// commands: beside the daemon credentials, in ~/.reliant.
func forgeTokenCachePath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".reliant", "forge-token-cache.json")
}

// newForgeCredentialService wires the helper over this machine's sessions.
func newForgeCredentialService(cmd *cobra.Command, pin forgecred.Pin) forgecred.Service {
	return forgecred.New(forgecred.Deps{
		Sessions: func() ([]forgecred.Session, error) {
			preferred := ""
			if conn, err := resolveServer(cmd); err == nil {
				preferred = conn.ServerURL
			}
			path, err := cliauth.CredentialsPath()
			if err != nil {
				path = ""
			}
			return forgecred.Discover(pin, preferred, path, time.Now())
		},
		Exchanger: connectExchanger{},
		CachePath: forgeTokenCachePath(),
	})
}

// connectExchanger calls TokenService.ExchangeToken over Connect.
type connectExchanger struct{}

func (connectExchanger) Exchange(ctx context.Context, server, bearer, audience string, scopes []string) (forgecred.Exchanged, error) {
	httpClient := (&connection{ServerURL: server}).httpClientWithBearer(bearer)
	httpClient.Timeout = 30 * time.Second
	resp, err := reliantv1connect.NewTokenServiceClient(httpClient, strings.TrimRight(server, "/")).
		ExchangeToken(ctx, connect.NewRequest(&reliantv1.ExchangeTokenRequest{Audience: audience, Scopes: scopes}))
	if err != nil {
		return forgecred.Exchanged{}, err
	}
	out := forgecred.Exchanged{Token: resp.Msg.GetToken(), Scopes: resp.Msg.GetScopes()}
	if at, err := time.Parse(time.RFC3339, resp.Msg.GetExpiresAt()); err == nil {
		out.ExpiresAt = at
	}
	return out, nil
}

func newAuthForgeCredentialCmd() *cobra.Command {
	var pin forgecred.Pin
	cmd := &cobra.Command{
		Use:    "forge-credential",
		Short:  "Credential helper for forge: a short-lived control-plane token from your Reliant session",
		Hidden: true,
		Long: `Answers forge's credential-helper protocol ($FORGE_CREDENTIAL_HELPER) on
stdin/stdout. forge runs it when no --token, token variable or 'forge login'
applies; you do not run it yourself. 'reliant forge' and the daemon set it.

It exchanges this machine's Reliant session (a daemon's credential, or a
'reliant auth login') at the Reliant server for a token that carries only
deploy, secret and domain authority, only what the session itself holds, for
at most an hour, and only for the control plane that server belongs to.
Tokens are cached in ~/.reliant/forge-token-cache.json (0600) and reused
while at least half their life remains. Nothing it prints to stderr contains
a token.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// stdout is the protocol channel: nothing but the one JSON
			// response may reach it. Diagnostics go to stderr, warnings only.
			slog.SetDefault(slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: slog.LevelWarn})))
			ctx, cancel := context.WithTimeout(cmd.Context(), forgeCredentialTimeout)
			defer cancel()
			svc := newForgeCredentialService(cmd, pin)
			return cloudcred.Serve(ctx, cmd.InOrStdin(), cmd.OutOrStdout(), svc.Mint)
		},
	}
	cmd.Flags().StringVar(&pin.Server, flagForgeCredServer, "", "Use the session of the daemon at this server (set by the daemon for its children)")
	cmd.Flags().StringVar(&pin.Account, flagForgeCredAccount, "", "Use this daemon account at --daemon-server")
	return cmd
}
