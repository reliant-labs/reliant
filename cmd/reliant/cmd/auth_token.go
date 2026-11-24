// Copyright (c) 2025 Reliant Labs
package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
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

Creating a token is a consented login (browser or device code): a token never
mints a token.
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

// mintableTokenScopes are the scopes an API token can carry, narrowest first:
// reliant:api, forge's runs/triggers slices of it and its inbox slices. Every
// other scope in forge's catalog is an org capability or a daemon's, which a
// token for the Reliant API is not for.
func mintableTokenScopes() []fat.Scope {
	out := []fat.Scope{fat.ScopeReliantAPI}
	out = append(out, fat.RunsAndTriggersScopes...)
	return append(out, fat.InboxScopes...)
}

func tokenScopeChoices() string {
	names := make([]string, 0, 10)
	for _, sc := range mintableTokenScopes() {
		names = append(names, string(sc))
	}
	return strings.Join(names, ", ")
}

// tokenScopesFromFlags resolves the --scope values (repeated, or comma or
// space separated) to the scopes to request. No flag keeps the token
// reliant:api. A value that is not a scope forge knows, or is one an API token
// cannot carry, is refused here, naming the valid ones, rather than left for
// the login to clip silently.
func tokenScopesFromFlags(flags []string) ([]string, error) {
	if len(flags) == 0 {
		return []string{cliauth.ScopeAPI}, nil
	}
	mintable := mintableTokenScopes()
	var out []string
	for _, raw := range flags {
		for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ' ' }) {
			sc, known := fat.ParseScope(part)
			switch {
			case !known:
				return nil, fmt.Errorf("--scope %q is not a known scope; valid scopes: %s", part, tokenScopeChoices())
			case !slices.Contains(mintable, sc):
				return nil, fmt.Errorf("--scope %q cannot be carried by an API token; valid scopes: %s", part, tokenScopeChoices())
			case !slices.Contains(out, part):
				out = append(out, part)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--scope needs a value; valid scopes: %s", tokenScopeChoices())
	}
	return out, nil
}

// scopeDifference compares the scopes a login granted with those requested.
// missing are requested scopes it did not grant; extra are granted scopes
// nobody asked for. A control plane may grant less than asked (RFC 6749 3.3),
// and a token that came back with less, or with more, is not the token asked
// for: more would hand automation access the person did not mean to give.
func scopeDifference(requested, granted []string) (missing, extra []string) {
	for _, r := range requested {
		if !slices.Contains(granted, r) {
			missing = append(missing, r)
		}
	}
	for _, g := range granted {
		if !slices.Contains(requested, g) && !slices.Contains(extra, g) {
			extra = append(extra, g)
		}
	}
	return missing, extra
}

// grantMismatch refuses a grant that is not exactly the requested scopes,
// naming what is missing and what is extra; the token is not printed.
func grantMismatch(requested, granted []string, tokenName string) error {
	missing, extra := scopeDifference(requested, granted)
	if len(missing) == 0 && len(extra) == 0 {
		return nil
	}
	var why []string
	if len(missing) > 0 {
		why = append(why, "did not grant "+strings.Join(missing, ", "))
	}
	if len(extra) > 0 {
		why = append(why, "also granted "+strings.Join(extra, ", ")+", which was not asked for")
	}
	return fmt.Errorf("the login %s (requested: %s; granted: %s); the token was not printed, revoke it with 'reliant auth token revoke %s'",
		strings.Join(why, " and "), strings.Join(requested, ", "), strings.Join(granted, ", "), tokenName)
}

func newAuthTokenCreateCmd() *cobra.Command {
	var name string
	var scopeFlags []string
	var forceDevice, forceBrowser bool
	var expiresIn string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a new API token",
		Long: `Mints an API token (reliant:api, 90 days) named reliant-cli@<name> through a
login you approve, and prints it once. It is NOT stored: this CLI keeps using
its own login. Hand the printed token to automation as RELIANT_TOKEN.

Re-running with the same --name replaces that token.

--expires-in asks for a shorter-lived token (a CI job's, say): 1h, 12h, 7d.
The server never grants more than 90 days. A control plane too old to honour it
mints its 90-day default, and then the command does not print the token: it
names it so you can revoke it.

You approve it the way you approve 'reliant auth login': in a browser that
calls back to a loopback port of THIS machine, or with a device code (RFC 8628)
you approve on any device. The browser is the default, except where its
callback cannot reach your browser: a Reliant cloud machine, an ssh session, a
Linux host with no display, or a Reliant agent's shell. There the device code
is used. --device or --browser chooses explicitly.

--scope narrows the token to part of the API instead of all of it. Repeat it, or
give a comma-separated list. Without it the token is reliant:api, the whole
API. A narrow token reaches only the rpcs its scopes cover and gets
PermissionDenied (scope_missing) on the rest. Valid scopes:
` + tokenScopeChoices() + `.`,
		Example: `  reliant auth token create --name ci --scope runs:read --scope triggers:fire
  reliant auth token create --name inbox-bot --scope inbox:read,inbox:manage
  reliant auth token create --name ci --device
  reliant auth token create --name nightly-job --scope runs:start --expires-in 12h`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if strings.TrimSpace(name) == "" {
				return fmt.Errorf("--name is required")
			}
			scopes, err := tokenScopesFromFlags(scopeFlags)
			if err != nil {
				return err
			}
			lifetime, err := parseTokenLifetime(expiresIn)
			if err != nil {
				return err
			}
			conn, err := resolveServer(cmd)
			if err != nil {
				return err
			}
			// The same choice as `reliant auth login`: the browser flow's
			// callback needs a browser on this machine, the device flow does
			// not. Prompts go to stderr so stdout carries only the token.
			managed, err := isManagedMachine(cmd)
			if err != nil {
				return err
			}
			flow := chooseLoginFlow(forceDevice, forceBrowser, managed,
				os.Getenv, //nolint:forbidigo // SSH_*, DISPLAY/WAYLAND_DISPLAY and the agent-shell marker describe the host's session, not configuration of this process
				runtime.GOOS)
			prompts := cmd.ErrOrStderr()
			if flow.Device {
				// The control plane prompts for every device request that is
				// not a machine pairing as a CLI sign-in; say what approving
				// this one does.
				fmt.Fprintf(prompts, "Approving the code below mints the API token %q (%s); it does not sign this CLI in.\n",
					cliauth.ClientID+"@"+name, strings.Join(scopes, " "))
			}
			cred, err := runLogin(cmd.Context(), prompts, loginRequest{
				Server:    conn.ServerURL,
				Scopes:    scopes,
				Name:      name,
				ExpiresIn: lifetime,
			}, flow)
			if err != nil {
				return fmt.Errorf("minting token: %w", err)
			}
			if err := lifetimeMismatch(lifetime, cred.ExpiresAt, time.Now(), cliauth.ClientID+"@"+name); err != nil {
				return err
			}
			// RFC 6749 3.3: a reply with no scope field means "as requested".
			if len(cred.Scopes) > 0 {
				if err := grantMismatch(scopes, cred.Scopes, cliauth.ClientID+"@"+name); err != nil {
					return err
				}
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "Token %q created\n", cliauth.ClientID+"@"+name)
			if len(cred.Scopes) > 0 {
				fmt.Fprintf(out, "  Scopes: %s\n", strings.Join(cred.Scopes, " "))
			}
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
	cmd.Flags().StringVar(&expiresIn, "expires-in", "", "How long the token lives, e.g. 1h, 12h, 7d (at least 1m, at most 90 days). An older control plane ignores it; the token is then not printed")
	cmd.Flags().StringSliceVar(&scopeFlags, "scope", nil, "Narrow the token to a scope; repeatable or comma-separated, e.g. --scope runs:read --scope triggers:fire (default reliant:api)")
	cmd.Flags().BoolVar(&forceDevice, "device", false, "Approve with a device code on any device (the default where a browser cannot reach this machine)")
	cmd.Flags().BoolVar(&forceBrowser, "browser", false, "Approve in a browser that calls back to this machine (the default on a desktop)")
	cmd.MarkFlagsMutuallyExclusive("device", "browser")
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
			resp, err := client.ListTokens(cmd.Context(), connect.NewRequest(&reliantv1.ListTokensRequest{Kind: reliantv1.TokenKind_TOKEN_KIND_API.Enum()}))
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

			listResp, err := client.ListTokens(cmd.Context(), connect.NewRequest(&reliantv1.ListTokensRequest{Kind: reliantv1.TokenKind_TOKEN_KIND_API.Enum()}))
			if err != nil {
				return conn.annotate(err)
			}

			matches := tokensToRevoke(listResp.Msg.GetTokens(), target)
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

// cliTokenNamePrefix is what the control plane puts before the --name that
// 'reliant auth token create' sends: --name ci is stored as reliant-cli@ci.
const cliTokenNamePrefix = "reliant-cli@"

// tokensToRevoke is the tokens 'reliant auth token revoke <target>' means: an
// id or a stored name, else the name 'token create --name' took (ci for
// reliant-cli@ci), so a token is revoked by the name it was created with.
func tokensToRevoke(tokens []*reliantv1.TokenInfo, target string) []*reliantv1.TokenInfo {
	var matches []*reliantv1.TokenInfo
	for _, t := range tokens {
		if t.GetId() == target || t.GetName() == target {
			matches = append(matches, t)
		}
	}
	if len(matches) > 0 || strings.HasPrefix(target, cliTokenNamePrefix) {
		return matches
	}
	for _, t := range tokens {
		if t.GetName() == cliTokenNamePrefix+target {
			matches = append(matches, t)
		}
	}
	return matches
}

// minTokenLifetime is the shortest lifetime the control plane mints
// (cliauth.ExpiresInParam: at least 60 seconds).
const minTokenLifetime = time.Minute

// maxTokenLifetime is the longest lifetime the control plane mints (its
// default, which it clamps every request to).
const maxTokenLifetime = 90 * 24 * time.Hour

// parseTokenLifetime reads --expires-in: a Go duration (90m, 12h) or whole
// days (7d). Empty asks for nothing: the server's default lifetime.
func parseTokenLifetime(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	var d time.Duration
	if days, ok := strings.CutSuffix(raw, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("--expires-in %q: give a duration such as 1h, 12h or 7d", raw)
		}
		// Checked before multiplying: a huge day count would overflow
		// time.Duration and wrap to a short, wrong lifetime.
		if n > int(maxTokenLifetime/(24*time.Hour)) {
			return 0, fmt.Errorf("--expires-in %q: a token lives at most 90d", raw)
		}
		d = time.Duration(n) * 24 * time.Hour
	} else {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return 0, fmt.Errorf("--expires-in %q: give a duration such as 1h, 12h or 7d", raw)
		}
		d = parsed
	}
	if d < minTokenLifetime {
		return 0, fmt.Errorf("--expires-in %q: a token lives at least 1m", raw)
	}
	if d > maxTokenLifetime {
		return 0, fmt.Errorf("--expires-in %q: a token lives at most 90d", raw)
	}
	return d, nil
}

// lifetimeMismatch refuses a token that outlives what --expires-in asked
// for: a control plane that predates cliauth.ExpiresInParam ignores it and
// mints its default, and a person who asked for an hour must not be handed a
// 90-day token unawares. The token is not printed, so it cannot leak; it is
// named so it can be revoked. A minute of slack covers the clocks and the
// approval wait.
func lifetimeMismatch(requested time.Duration, expiresAt *time.Time, now time.Time, tokenName string) error {
	if requested <= 0 {
		return nil
	}
	if expiresAt != nil && !expiresAt.After(now.Add(requested+time.Minute)) {
		return nil
	}
	granted := "no expiry"
	if expiresAt != nil {
		granted = "until " + expiresAt.Format(time.RFC3339)
	}
	return fmt.Errorf("the server granted the token %s, longer than --expires-in %s asked for (it predates --expires-in); "+
		"the token was not printed, revoke it with 'reliant auth token revoke %s'", granted, requested, tokenName)
}
