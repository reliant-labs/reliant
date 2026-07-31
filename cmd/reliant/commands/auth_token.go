// Copyright (c) 2025 Reliant Labs
package commands

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/cliconfig"
)

func newAuthTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage API tokens (rlnt_pat_ personal access tokens)",
		Long: `API tokens authenticate CLI and automation requests against the Reliant
API without a browser login. Tokens are shown once at creation and stored
into the resolved CLI context (see 'reliant context').

Management runs over the reliant.v1.TokenService Connect RPCs. Creating a
token always requires an interactive login JWT ('reliant auth login') — a
PAT cannot mint a PAT. Listing and revoking accept either the context API
token or a login JWT.`,
	}

	cmd.AddCommand(newAuthTokenCreateCmd())
	cmd.AddCommand(newAuthTokenListCmd())
	cmd.AddCommand(newAuthTokenRevokeCmd())

	return cmd
}

// requireJWTAndServer resolves the target server (context-aware, credentials
// not required) plus the auth-file JWT that token *creation* requires — a PAT
// cannot mint a PAT. List/revoke do not use this: they authenticate with the
// resolved context credential (PAT or JWT).
func requireJWTAndServer(cmd *cobra.Command) (conn *connection, jwt string, err error) {
	conn, err = resolveServer(cmd)
	if err != nil {
		return nil, "", err
	}

	jwt, err = auth.ReadAccessTokenFromAuthFile()
	if err != nil {
		return nil, "", fmt.Errorf("reading auth file: %w", err)
	}
	if jwt == "" {
		return nil, "", fmt.Errorf("token creation requires an interactive login for %s — run 'reliant auth login' first", conn.describeServer())
	}
	return conn, jwt, nil
}

// tokenServiceClient builds a Connect TokenService client on the resolved
// server, authenticated with the given bearer (an rlnt_pat_ API token or a
// login JWT).
func tokenServiceClient(conn *connection, bearer string) reliantv1connect.TokenServiceClient {
	return reliantv1connect.NewTokenServiceClient(conn.httpClientWithBearer(bearer), conn.ServerURL)
}

// parseTTL parses token lifetimes: Go durations ("12h", "45m") plus a day
// suffix ("90d"). Returns 0 for the empty string (no expiry).
func parseTTL(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	if strings.HasSuffix(raw, "d") {
		days, err := strconv.Atoi(strings.TrimSuffix(raw, "d"))
		if err != nil || days <= 0 {
			return 0, fmt.Errorf("invalid --ttl %q — expected a positive day count like '90d' or a duration like '12h'", raw)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid --ttl %q — expected a positive day count like '90d' or a duration like '12h'", raw)
	}
	return d, nil
}

func newAuthTokenCreateCmd() *cobra.Command {
	var (
		name   string
		ttl    string
		noSave bool
	)

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new API token",
		Long: `Creates a new API token. The raw token is printed exactly once and saved
into the resolved CLI context (creating a "default" context when none is
configured) so subsequent commands authenticate with it automatically.

Requires an interactive login JWT ('reliant auth login') — a PAT cannot mint
a PAT.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			ttlDur, err := parseTTL(ttl)
			if err != nil {
				return err
			}

			// Token creation is JWT-only: server from the context, bearer from
			// the login auth file.
			conn, jwt, err := requireJWTAndServer(cmd)
			if err != nil {
				return err
			}
			contextName := conn.ContextName

			req := &reliantv1.CreateTokenRequest{Name: name}
			if ttlDur > 0 {
				req.TtlSeconds = int64(ttlDur / time.Second)
			}

			client := tokenServiceClient(conn, jwt)
			resp, err := client.CreateToken(cmd.Context(), connect.NewRequest(req))
			if err != nil {
				return conn.annotate(err)
			}
			info := resp.Msg.GetInfo()

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Token %q created\n", info.GetName())
			fmt.Fprintf(out, "  ID:      %s\n", info.GetId())
			if info.GetExpiresAt() != "" {
				fmt.Fprintf(out, "  Expires: %s\n", info.GetExpiresAt())
			}
			fmt.Fprintln(out)
			fmt.Fprintf(out, "  %s\n", resp.Msg.GetToken())
			fmt.Fprintln(out)
			fmt.Fprintln(out, "This token is shown only once — it cannot be retrieved again.")

			if noSave {
				return nil
			}

			// Save into the resolved context (or bootstrap "default").
			cfg, err := cliconfig.Load()
			if err != nil {
				return fmt.Errorf("saving token to CLI config: %w", err)
			}
			if contextName == "" {
				contextName = "default"
			}
			ctx := cfg.Contexts[contextName]
			if ctx == nil {
				ctx = &cliconfig.Context{}
				cfg.Contexts[contextName] = ctx
			}
			ctx.Token = resp.Msg.GetToken()
			if ctx.Server == "" {
				ctx.Server = conn.ServerURL
			}
			if cfg.CurrentContext == "" {
				cfg.CurrentContext = contextName
			}
			if err := cliconfig.Save(cfg); err != nil {
				return fmt.Errorf("saving token to CLI config: %w", err)
			}
			configPath, _ := cliconfig.DefaultPath()
			fmt.Fprintf(out, "Saved into context %q (%s)\n", contextName, configPath)
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Human-readable token name (required)")
	cmd.Flags().StringVar(&ttl, "ttl", "", "Token lifetime, e.g. 90d or 12h (default: no expiry)")
	cmd.Flags().BoolVar(&noSave, "no-save", false, "Print the token without saving it into the CLI context")

	return cmd
}

// tokenJSON is the --json presentation of an API token, preserving the
// snake_case field names the former HTTP surface emitted.
type tokenJSON struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	TokenPrefix string `json:"token_prefix"`
	CreatedAt   string `json:"created_at"`
	LastUsedAt  string `json:"last_used_at,omitempty"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	RevokedAt   string `json:"revoked_at,omitempty"`
}

func tokensToJSON(toks []*reliantv1.TokenInfo) []tokenJSON {
	out := make([]tokenJSON, 0, len(toks))
	for _, t := range toks {
		out = append(out, tokenJSON{
			ID:          t.GetId(),
			Name:        t.GetName(),
			TokenPrefix: t.GetTokenPrefix(),
			CreatedAt:   t.GetCreatedAt(),
			LastUsedAt:  t.GetLastUsedAt(),
			ExpiresAt:   t.GetExpiresAt(),
			RevokedAt:   t.GetRevokedAt(),
		})
	}
	return out
}

func newAuthTokenListCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List API tokens (metadata only, never secrets)",
		RunE: func(cmd *cobra.Command, args []string) error {
			conn, err := resolveConnection(cmd)
			if err != nil {
				return err
			}

			client := tokenServiceClient(conn, conn.Token)
			resp, err := client.ListTokens(cmd.Context(), connect.NewRequest(&reliantv1.ListTokensRequest{}))
			if err != nil {
				return conn.annotate(err)
			}
			toks := resp.Msg.GetTokens()

			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(tokensToJSON(toks))
			}

			if len(toks) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No API tokens")
				return nil
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tID\tPREFIX\tCREATED\tLAST USED\tEXPIRES\tSTATUS")
			for _, t := range toks {
				status := "active"
				if t.GetRevokedAt() != "" {
					status = "revoked"
				} else if t.GetExpiresAt() != "" {
					if exp, err := time.Parse(time.RFC3339, t.GetExpiresAt()); err == nil && time.Now().After(exp) {
						status = "expired"
					}
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
					t.GetName(), t.GetId(), t.GetTokenPrefix(), t.GetCreatedAt(),
					orDash(t.GetLastUsedAt()), orDash(t.GetExpiresAt()), status)
			}
			return w.Flush()
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output in JSON format")

	return cmd
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func newAuthTokenRevokeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "revoke <name-or-id>",
		Short: "Revoke an API token by name or ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			target := args[0]

			conn, err := resolveConnection(cmd)
			if err != nil {
				return err
			}
			client := tokenServiceClient(conn, conn.Token)

			listResp, err := client.ListTokens(cmd.Context(), connect.NewRequest(&reliantv1.ListTokensRequest{}))
			if err != nil {
				return conn.annotate(err)
			}

			var matches []*reliantv1.TokenInfo
			for _, t := range listResp.Msg.GetTokens() {
				if t.GetRevokedAt() != "" {
					continue
				}
				if t.GetId() == target || t.GetName() == target {
					matches = append(matches, t)
				}
			}
			switch len(matches) {
			case 0:
				return fmt.Errorf("no active token named %q (check 'reliant auth token list')", target)
			case 1:
			default:
				return fmt.Errorf("%d active tokens match %q — revoke by ID instead", len(matches), target)
			}

			tok := matches[0]
			if _, err := client.RevokeToken(cmd.Context(), connect.NewRequest(&reliantv1.RevokeTokenRequest{Id: tok.GetId()})); err != nil {
				return conn.annotate(err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Revoked token %q (%s)\n", tok.GetName(), tok.GetId())

			// Drop the now-dead token from any context that stored it (match by
			// display prefix — the config never stores hashes).
			if cfg, err := cliconfig.Load(); err == nil {
				changed := false
				for _, c := range cfg.Contexts {
					if c.Token != "" && tok.GetTokenPrefix() != "" && strings.HasPrefix(c.Token, tok.GetTokenPrefix()) {
						c.Token = ""
						changed = true
					}
				}
				if changed {
					if err := cliconfig.Save(cfg); err == nil {
						fmt.Fprintln(cmd.OutOrStdout(), "Removed the revoked token from the CLI config")
					}
				}
			}
			return nil
		},
	}
}
