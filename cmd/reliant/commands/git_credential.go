// Copyright (c) 2025 Reliant Labs
package commands

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/cliauth"
	"github.com/reliant-labs/reliant/internal/forgecred"
	"github.com/reliant-labs/reliant/internal/toolexec/daemonruntime"
)

// ── git's credentials come from the server, at the moment git asks ────────
//
// A daemon never holds a GitHub token at rest. git runs this helper (see
// daemonruntime/git_credential.go for how it is wired) and the helper asks
// TokenService.GetGitToken, as this machine's daemon session, for the user's
// current token. Nothing is cached or written: a cache file would be the
// token at rest this exists to remove.

// gitCredentialHosts maps the hosts the helper answers to the provider the
// server resolves. Only hosts the server can answer belong here.
var gitCredentialHosts = map[string]string{
	"github.com": "github",
}

const gitCredentialTimeout = 30 * time.Second

// pinGitCredentialHelper tells the daemon's git wiring which helper command
// to use: this binary, pinned to the daemon session. Best-effort.
func pinGitCredentialHelper(server, account string) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	argv := []string{exe, "auth", "git-credential"}
	if strings.TrimSpace(server) != "" {
		argv = append(argv, "--"+flagForgeCredServer, server)
		if strings.TrimSpace(account) != "" {
			argv = append(argv, "--"+flagForgeCredAccount, account)
		}
	}
	daemonruntime.SetGitCredentialHelper(argv)
}

// gitTokenFetcher returns the user's current token for provider, as the
// session found by pin.
type gitTokenFetcher func(ctx context.Context, cmd *cobra.Command, pin forgecred.Pin, provider string) (string, error)

var errGitProviderNotConnected = errors.New("Reliant has no usable GitHub connection for you: reconnect GitHub in Reliant settings")

func fetchGitToken(ctx context.Context, cmd *cobra.Command, pin forgecred.Pin, provider string) (string, error) {
	preferred := ""
	if conn, err := resolveServer(cmd); err == nil {
		preferred = conn.ServerURL
	}
	path, err := cliauth.CredentialsPath()
	if err != nil {
		path = ""
	}
	sessions, err := forgecred.Discover(pin, preferred, path, time.Now())
	if err != nil {
		return "", fmt.Errorf("reading Reliant sessions: %w", err)
	}
	if len(sessions) == 0 {
		return "", errors.New("no Reliant session on this machine")
	}
	var last error
	for _, s := range sessions {
		httpClient := (&connection{ServerURL: s.Server}).httpClientWithBearer(s.Token)
		httpClient.Timeout = 20 * time.Second
		resp, err := reliantv1connect.NewTokenServiceClient(httpClient, strings.TrimRight(s.Server, "/")).
			GetGitToken(ctx, connect.NewRequest(&reliantv1.GetGitTokenRequest{Provider: provider}))
		if err == nil {
			if resp.Msg.GetAccessToken() == "" {
				return "", errGitProviderNotConnected
			}
			return resp.Msg.GetAccessToken(), nil
		}
		switch connect.CodeOf(err) {
		case connect.CodeFailedPrecondition:
			return "", errGitProviderNotConnected
		case connect.CodeUnauthenticated, connect.CodePermissionDenied:
			last = fmt.Errorf("%s session was not accepted: %s", s.Kind, connect.CodeOf(err))
		default:
			last = fmt.Errorf("asking Reliant for the git token: %s", connect.CodeOf(err))
		}
	}
	return "", last
}

// readCredentialRequest parses git's key=value lines up to the blank line.
func readCredentialRequest(r io.Reader) map[string]string {
	attrs := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			break
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			attrs[k] = v
		}
	}
	return attrs
}

func newAuthGitCredentialCmd() *cobra.Command { return newAuthGitCredentialCmdWith(fetchGitToken) }

func newAuthGitCredentialCmdWith(fetch gitTokenFetcher) *cobra.Command {
	var pin forgecred.Pin
	cmd := &cobra.Command{
		Use:          "git-credential <get|store|erase>",
		SilenceUsage: true,
		Short:        "git credential helper: the user's current GitHub token, fetched from Reliant",
		Hidden:       true,
		Long: `Answers git's credential-helper protocol. git runs it; you do not.

For 'get' on https://github.com it asks the Reliant server, as this machine's
daemon session, for the user's current token and prints it. Any other host or
protocol gets no answer, so git falls through to its other helpers. 'store'
and 'erase' do nothing: the token is never written anywhere.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// stdout is the protocol channel: only the response may reach it.
			slog.SetDefault(slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{Level: slog.LevelWarn})))
			if args[0] != "get" {
				return nil
			}
			attrs := readCredentialRequest(cmd.InOrStdin())
			provider, ok := gitCredentialHosts[strings.ToLower(attrs["host"])]
			if attrs["protocol"] != "https" || !ok {
				return nil
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), gitCredentialTimeout)
			defer cancel()
			token, err := fetch(ctx, cmd, pin, provider)
			if err != nil {
				return fmt.Errorf("reliant git-credential: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "username=x-access-token\npassword=%s\n", token)
			return nil
		},
	}
	cmd.Flags().StringVar(&pin.Server, flagForgeCredServer, "", "Use the session of the daemon at this server")
	cmd.Flags().StringVar(&pin.Account, flagForgeCredAccount, "", "Use this daemon account at --daemon-server")
	return cmd
}
