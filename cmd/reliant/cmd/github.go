// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/cliauth"
	"github.com/reliant-labs/reliant/internal/forgecred"
)

// ── GitHub as you, or as the Reliant bot ───────────────────────────────────
//
// An agent implements as the user (commits, pushes and pull requests go
// through git's credential helper, which is the user's token) and reviews,
// labels and merges as the Reliant GitHub App's bot. `reliant github token
// --as app` is how an agent's shell gets the bot: a one-hour installation
// token for ONE repository, minted by control-plane only when the user can
// push there. `reliant github gh` runs gh with it so the token never has to
// sit in a variable or a file. Both ask the server as this machine's daemon
// session, exactly as the git credential helper does; nothing is cached.

const (
	githubAsApp  = "app"
	githubAsUser = "user"
)

// githubBotToken is what the server answers for --as app.
type githubBotToken struct {
	Token          string `json:"token"`
	ExpiresAt      string `json:"expires_at,omitempty"`
	BotLogin       string `json:"bot_login,omitempty"`
	ActingForLogin string `json:"acting_for_login,omitempty"`
	Repository     string `json:"repository,omitempty"`
}

// githubTokenRequest names what token to fetch.
type githubTokenRequest struct {
	As          string
	Owner, Repo string
	Permissions map[string]string
}

// githubTokenFetcher returns the token req asks for, as the session pin finds.
type githubTokenFetcher func(ctx context.Context, cmd *cobra.Command, pin forgecred.Pin, req githubTokenRequest) (githubBotToken, error)

func fetchGitHubToken(ctx context.Context, cmd *cobra.Command, pin forgecred.Pin, req githubTokenRequest) (githubBotToken, error) {
	if req.As == githubAsUser {
		tok, err := fetchGitToken(ctx, cmd, pin, "github")
		return githubBotToken{Token: tok}, err
	}
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
		return githubBotToken{}, fmt.Errorf("reading Reliant sessions: %w", err)
	}
	if len(sessions) == 0 {
		return githubBotToken{}, errors.New("no Reliant daemon session on this machine: run this in a Reliant agent's shell or on a signed-in daemon's machine")
	}
	var last error
	for _, s := range sessions {
		httpClient := (&connection{ServerURL: s.Server}).httpClientWithBearer(s.Token)
		httpClient.Timeout = 20 * time.Second
		resp, err := reliantv1connect.NewTokenServiceClient(httpClient, strings.TrimRight(s.Server, "/")).
			GetGitHubBotToken(ctx, connect.NewRequest(&reliantv1.GetGitHubBotTokenRequest{
				Owner: req.Owner, Repo: req.Repo, Permissions: req.Permissions,
			}))
		if err == nil {
			m := resp.Msg
			if m.GetToken() == "" {
				return githubBotToken{}, errors.New("the server returned no bot token")
			}
			return githubBotToken{Token: m.GetToken(), ExpiresAt: m.GetExpiresAt(), BotLogin: m.GetBotLogin(),
				ActingForLogin: m.GetActingForLogin(), Repository: m.GetRepository()}, nil
		}
		switch connect.CodeOf(err) {
		case connect.CodeUnauthenticated:
			last = fmt.Errorf("%s session was not accepted: %s", s.Kind, connect.CodeOf(err))
		case connect.CodePermissionDenied:
			// Either the session is not a daemon's, or the user cannot push to
			// the repository: the message says which, and it is not the
			// session's fault only in the first case.
			last = errors.New(connectMessage(err))
		default:
			// A refusal (not installed, not connected, bad name) is the same
			// from every session: report it rather than try the next one.
			return githubBotToken{}, errors.New(connectMessage(err))
		}
	}
	return githubBotToken{}, last
}

// connectMessage is a Connect error's message without the code prefix.
func connectMessage(err error) string {
	var ce *connect.Error
	if errors.As(err, &ce) && ce.Message() != "" {
		return ce.Message()
	}
	return err.Error()
}

// githubFlags are the flags token and gh share.
type githubFlags struct {
	pin   forgecred.Pin
	as    string
	repo  string
	perms []string
}

func (f *githubFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.as, "as", githubAsApp, "Who to act as: app (the Reliant bot, reliant-labs[bot]) or user (you)")
	cmd.Flags().StringVar(&f.repo, "repo", "", "The one repository the bot token is scoped to, as owner/name (required with --as app)")
	cmd.Flags().StringArrayVar(&f.perms, "permission", nil, "Narrow the bot token: name=level, name one of issues, pull_requests, contents and level read or write; repeatable (default: all three at write)")
	cmd.Flags().StringVar(&f.pin.Server, flagForgeCredServer, "", "Use the session of the daemon at this server")
	cmd.Flags().StringVar(&f.pin.Account, flagForgeCredAccount, "", "Use this daemon account at --daemon-server")
}

func (f *githubFlags) request() (githubTokenRequest, error) {
	req := githubTokenRequest{As: strings.ToLower(strings.TrimSpace(f.as))}
	switch req.As {
	case githubAsUser:
		if f.repo != "" || len(f.perms) > 0 {
			return req, errors.New("--repo and --permission narrow a bot token; your own token (--as user) is not scoped")
		}
		return req, nil
	case githubAsApp:
	default:
		return req, fmt.Errorf("--as must be app or user, got %q", f.as)
	}
	owner, name, ok := strings.Cut(strings.TrimSpace(f.repo), "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return req, errors.New("--repo owner/name is required with --as app: a bot token is scoped to one repository")
	}
	req.Owner, req.Repo = owner, name
	for _, p := range f.perms {
		k, v, ok := strings.Cut(p, "=")
		if !ok || strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
			return req, fmt.Errorf("--permission %q: want name=level, e.g. pull_requests=write", p)
		}
		if req.Permissions == nil {
			req.Permissions = map[string]string{}
		}
		req.Permissions[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return req, nil
}

func newGitHubCmd() *cobra.Command { return newGitHubCmdWith(fetchGitHubToken, runGH) }

func newGitHubCmdWith(fetch githubTokenFetcher, run ghRunner) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "github",
		Short: "Act on GitHub as the Reliant bot (reviews, labels, merges) or as yourself",
		Long: `Act on GitHub as the Reliant GitHub App's bot, reliant-labs[bot], or as yourself.

Implementation is yours: commit, push and open pull requests as you (git's
credential helper already does). Reviewing, labelling and merging are the
bot's: use 'reliant github gh --repo owner/name -- <gh args>' for them.

The bot writes only where you can push: control-plane checks your own GitHub
access before it mints the token, scopes the token to one repository, and
records the mint in its audit log. Tokens last at most an hour and are never
stored.`,
	}
	cmd.AddCommand(newGitHubTokenCmd(fetch), newGitHubGHCmd(fetch, run))
	return cmd
}

func newGitHubTokenCmd(fetch githubTokenFetcher) *cobra.Command {
	var f githubFlags
	var asJSON bool
	cmd := &cobra.Command{
		Use:          "token",
		Short:        "Print a GitHub token: the Reliant bot's for one repository (--as app), or yours (--as user)",
		SilenceUsage: true,
		Example: `  # The bot's token for one repository, for a tool that reads GH_TOKEN
  GH_TOKEN="$(reliant github token --as app --repo reliant-labs/forge)" gh pr review 12 --repo reliant-labs/forge --comment -b "LGTM"

  # Only what a review needs, with who it speaks as and when it expires
  reliant github token --repo reliant-labs/forge --permission pull_requests=write --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			req, err := f.request()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), gitCredentialTimeout)
			defer cancel()
			tok, err := fetch(ctx, cmd, f.pin, req)
			if err != nil {
				return fmt.Errorf("reliant github token: %w", err)
			}
			if asJSON {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(tok)
			}
			fmt.Fprintln(cmd.OutOrStdout(), tok.Token)
			return nil
		},
	}
	f.bind(cmd)
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print the token with who it speaks as, its repository and its expiry")
	return cmd
}

// ghRunner runs gh with args, env and the command's stdio, returning its exit code.
type ghRunner func(cmd *cobra.Command, args, env []string) (int, error)

func runGH(cmd *cobra.Command, args, env []string) (int, error) {
	gh, err := exec.LookPath("gh")
	if err != nil {
		return 0, errors.New("gh is not installed or not on PATH")
	}
	c := exec.CommandContext(cmd.Context(), gh, args...)
	c.Env, c.Stdin, c.Stdout, c.Stderr = env, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()
	err = c.Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	if err != nil {
		return 0, err
	}
	return 0, nil
}

// ghEnv is the environment gh runs in: this process's, minus every way gh
// could pick another identity, plus the token (and, for the bot, the
// repository it is scoped to as gh's default).
func ghEnv(base []string, tok githubBotToken, repository string) []string {
	drop := map[string]bool{"GH_TOKEN": true, "GITHUB_TOKEN": true, "GH_ENTERPRISE_TOKEN": true,
		"GITHUB_ENTERPRISE_TOKEN": true, "GH_HOST": true, "GH_REPO": true}
	env := make([]string, 0, len(base)+2)
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		if !drop[k] {
			env = append(env, kv)
		}
	}
	env = append(env, "GH_TOKEN="+tok.Token)
	if repository != "" {
		env = append(env, "GH_REPO="+repository)
	}
	return env
}

func newGitHubGHCmd(fetch githubTokenFetcher, run ghRunner) *cobra.Command {
	var f githubFlags
	cmd := &cobra.Command{
		Use:   "gh [flags] -- <gh arguments>",
		Short: "Run gh as the Reliant bot for one repository (--as app, the default) or as yourself",
		Long: `Run the GitHub CLI with a freshly minted token, so its writes are made as the
Reliant bot (reliant-labs[bot]) in the one repository --repo names. GH_REPO is
set to that repository, so gh's commands default to it; a command aimed at any
other repository is refused by GitHub, because the token reaches only this one.

Use it for reviews, labels and merges. Commits, pushes and opening a pull
request stay yours: run git and gh as usual for those.`,
		Example: `  reliant github gh --repo reliant-labs/reliant -- pr review 1903 --comment -b "Reviewed at 3c7162a"
  reliant github gh --repo reliant-labs/reliant -- pr edit 1903 --add-label merge:queue
  reliant github gh --repo reliant-labs/reliant -- pr merge 1903 --squash --auto --match-head-commit 3c7162a8cb0e0725b3c287b633a93e32a562a4e6`,
		SilenceUsage: true,
		Args:         cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.ArgsLenAtDash() != 0 {
				return errors.New("put gh's arguments after --, e.g. reliant github gh --repo owner/name -- pr view 12")
			}
			req, err := f.request()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), gitCredentialTimeout)
			defer cancel()
			tok, err := fetch(ctx, cmd, f.pin, req)
			if err != nil {
				return fmt.Errorf("reliant github gh: %w", err)
			}
			repository := ""
			if req.As == githubAsApp {
				repository = req.Owner + "/" + req.Repo
			}
			code, err := run(cmd, args, ghEnv(os.Environ(), tok, repository))
			if err != nil {
				return fmt.Errorf("reliant github gh: %w", err)
			}
			if code != 0 {
				return &exitCodeError{code: code}
			}
			return nil
		},
	}
	f.bind(cmd)
	return cmd
}

// exitCodeError carries gh's exit code out of the command unchanged.
type exitCodeError struct{ code int }

func (e *exitCodeError) Error() string { return fmt.Sprintf("gh exited %d", e.code) }

// ExitCode is the process exit code the CLI's main returns.
func (e *exitCodeError) ExitCode() int { return e.code }
