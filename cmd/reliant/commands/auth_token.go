// Copyright (c) 2025 Reliant Labs
package commands

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/cliauth"
)

func newAuthTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Manage API tokens (rlat_ access tokens acting as you)",
		Long: `API tokens authenticate automation against the Reliant API without a browser
login. 'reliant auth login' already stores one for this CLI; 'create' mints an
additional, separately named token to hand to a script or CI (RELIANT_TOKEN).

Creating a token is a consented browser login: a token never mints a token.
Listing and revoking use the resolved credential (RELIANT_TOKEN or your login).`,
	}

	cmd.AddCommand(newAuthTokenCreateCmd())
	cmd.AddCommand(newAuthTokenListCmd())
	cmd.AddCommand(newAuthTokenRevokeCmd())

	return cmd
}

// tokenServiceClient builds a Connect TokenService client on the resolved
// server, authenticated with the given rlat_ bearer.
func tokenServiceClient(conn *connection, bearer string) reliantv1connect.TokenServiceClient {
	return reliantv1connect.NewTokenServiceClient(conn.httpClientWithBearer(bearer), conn.ServerURL)
}

func newAuthTokenCreateCmd() *cobra.Command {
	var name string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new API token",
		Long: `Mints an API token (reliant:api, 90 days) named reliant-cli@<name> through a
browser login you approve, and prints it once. It is NOT stored: this CLI
keeps using its own login. Hand the printed token to automation as
RELIANT_TOKEN.

Re-running with the same --name replaces that token.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("--name is required")
			}
			conn, err := resolveServer(cmd)
			if err != nil {
				return err
			}
			cred, err := cliauth.Login{
				Server:  conn.ServerURL,
				Scopes:  []string{cliauth.ScopeAPI},
				Name:    name,
				Out:     cmd.ErrOrStderr(),
				OpenURL: loginOpener,
			}.Run(cmd.Context())
			if err != nil {
				return fmt.Errorf("minting token: %w", err)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Token %q created\n", cliauth.ClientID+"@"+name)
			if cred.ExpiresAt != nil {
				fmt.Fprintf(out, "  Expires: %s\n", cred.ExpiresAt.Format(time.RFC3339))
			}
			fmt.Fprintln(out)
			fmt.Fprintf(out, "  %s\n", cred.Token)
			fmt.Fprintln(out)
			fmt.Fprintln(out, "This token is shown only once — it cannot be retrieved again.")
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "Label for the token, e.g. ci-deploy (required)")
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
			resp, err := client.ListTokens(cmd.Context(), connect.NewRequest(&reliantv1.ListTokensRequest{Kind: reliantv1.TokenKind_TOKEN_KIND_API}))
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
				if t.GetExpiresAt() != "" {
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

			listResp, err := client.ListTokens(cmd.Context(), connect.NewRequest(&reliantv1.ListTokensRequest{Kind: reliantv1.TokenKind_TOKEN_KIND_API}))
			if err != nil {
				return conn.annotate(err)
			}

			var matches []*reliantv1.TokenInfo
			for _, t := range listResp.Msg.GetTokens() {
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

			return nil
		},
	}
}
