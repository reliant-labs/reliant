package cli

// `forge cloud token create|list|revoke`: mint and manage the ORG automation
// token a CI pipeline deploys with (hosted-deploy-primitives §3.6, task F6).
//
// WHY THIS VERB EXISTS. The scaffolded release workflow needs a
// FORGE_CONTROL_PLANE_TOKEN secret. Without a CLI for it, the docs had to send
// a user to a web UI to copy a secret by hand — a step that is skipped, done
// with the wrong scopes, or done with a PERSONAL token that dies when its owner
// leaves. These call AccessTokenService's ORG token RPCs (CreateToken /
// ListTokens / RevokeToken): the token has no acting user, so it outlives any
// person. The control plane requires a HUMAN session for these; a machine
// credential is refused server-side, and that refusal is surfaced as-is.
//
// A CONTROL PLANE MAY REFUSE A CLI LOGIN TOO. Reliant cloud refuses token
// administration to a `forge login` session (control-plane#2636, reason
// person_session_required): a token a CLI login minted outlived revoking the
// login. The CI token is then minted in the web app (Settings → API tokens),
// and these verbs say so (tokenAdminRefusal) instead of printing a bare
// PermissionDenied. They never refuse up front: a control plane that still
// admits a CLI login keeps working.
//
// THE SECRET IS SHOWN EXACTLY ONCE — by the server's design, and so by this
// verb's. `--json` prints it in the document so it can be piped straight into
// `gh secret set` without ever being echoed to a terminal.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/accesstoken"
)

const (
	procCreateToken = "controlplane.v1.AccessTokenService/CreateToken"
	procListTokens  = "controlplane.v1.AccessTokenService/ListTokens"
	procRevokeToken = "controlplane.v1.AccessTokenService/RevokeToken"
)

// wireAccessToken is controlplane.v1.AccessToken, the fields forge reads.
type wireAccessToken struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	DisplayPrefix string     `json:"displayPrefix,omitempty"`
	Scopes        []string   `json:"scopes,omitempty"`
	CreatedAt     *time.Time `json:"createdAt,omitempty"`
	ExpiresAt     *time.Time `json:"expiresAt,omitempty"`
	LastUsedAt    *time.Time `json:"lastUsedAt,omitempty"`
	RevokedAt     *time.Time `json:"revokedAt,omitempty"`
}

func newCloudTokenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "token",
		Short: "Mint, list and revoke ORG automation tokens (the CI deploy credential)",
		Long: `Manage the organization's automation tokens on the hosted control plane.

An org token has no acting user, so it outlives any one person — which is what
a CI pipeline's FORGE_CONTROL_PLANE_TOKEN must be. Minting needs a human session;
the control plane refuses a machine credential.

On Reliant cloud, mint the CI token in the web app instead:
` + cloud.DefaultConsoleURL + cloud.TokenSettingsPath + ` (Settings → API tokens). It refuses
token administration to a CLI login (` + "`forge login`" + `), because a token a CLI
login minted would outlive revoking the login; these commands then say so and
exit 9. They still work against a control plane that admits a CLI login:

  forge cloud token create --env prod --name github-actions \
      --scopes deploy:read,deploy:write --json | jq -r .secret \
    | gh secret set FORGE_CONTROL_PLANE_TOKEN

--env picks the control plane the env's KCL declares. Without it, or for an env
with no deploy/kcl (a repo whose own CI deploys and only runs
` + "`forge release record`" + `), the token is minted on Reliant cloud:

  forge cloud token create --name github-actions --scopes deploy:write`,
	}
	cmd.AddCommand(newCloudTokenCreateCmd(), newCloudTokenListCmd(), newCloudTokenRevokeCmd())
	return cmdutil.StrictGroup(cmd)
}

// cloudTokenFlags are the flags every token verb shares: which control plane
// (by an env that declares it) and with which credential.
type cloudTokenFlags struct {
	env, token string
	org        string
	jsonOut    bool
}

func (f *cloudTokenFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringVar(&f.org, "org", "", "Explicit organization for token administration (not a login or project preference)")
	cmd.Flags().StringVar(&f.env, "env", "", "Env whose declared control plane to use (default, and for an env with no deploy/kcl: Reliant cloud)")
	cmd.Flags().StringVar(&f.token, "token", "", "Credential to use, ahead of the env var and the credentials file")
	cmd.Flags().BoolVar(&f.jsonOut, "json", false, "Emit machine-readable JSON")
}

func (f *cloudTokenFlags) client(ctx context.Context) (cloudCaller, error) {
	ep, cred, err := f.target(ctx, projectDirForKCL())
	if err != nil {
		return nil, err
	}
	if f.org != "" {
		if cred.Kind != "human" {
			return nil, fmt.Errorf("organization administration requires human login")
		}
		ep.ProjectID, ep.ProjectName = "", ""
		return organizationAdminCaller{cloud.NewClient(ep, cred), f.org}, nil
	}
	return cloud.NewClient(ep, cred), nil
}

// target resolves the control plane a token verb talks to. An env forge's
// KCL declares uses the control plane it declares, as every hosted verb does.
// With no --env, or an env that has no deploy/kcl (a repo that only records
// deploys with `forge release record`), it is Reliant cloud — the default
// `forge login` signs into, and the one such a repo's records go to. Safe for
// the reason login's default is: a token is ORG-scoped, so the env only ever
// chose WHERE, never what the token may touch.
func (f *cloudTokenFlags) target(ctx context.Context, projectDir string) (cloud.Endpoint, cloud.Credential, error) {
	if f.env != "" {
		if _, err := os.Stat(filepath.Join(projectDir, "deploy", "kcl", f.env, "main.k")); err == nil {
			return resolveCloudTarget(ctx, f.env, f.token)
		}
	}
	return resolveCloudTargetFor(f.env, &cloud.Declaration{Endpoint: cloud.DefaultEndpoint}, f.token)
}

type organizationAdminCaller struct {
	client *cloud.Client
	org    string
}

func (c organizationAdminCaller) Call(ctx context.Context, procedure string, req, out any) error {
	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return err
	}
	body["orgId"] = c.org
	return c.client.Call(ctx, procedure, body, out)
}

func newCloudTokenCreateCmd() *cobra.Command {
	var (
		f       cloudTokenFlags
		name    string
		scopes  string
		expires time.Duration
	)
	cmd := &cobra.Command{
		Use:   "create --env <env> --name <name> --scopes <s1,s2>",
		Short: "Mint an org automation token; its secret is printed ONCE",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			parsed, err := parseTokenScopes(scopes)
			if err != nil {
				return err
			}
			client, err := f.client(cmd.Context())
			if err != nil {
				return err
			}
			return runCloudTokenCreate(cmd.Context(), client, name, parsed, expires, f.jsonOut, cmd.OutOrStdout())
		},
	}
	f.register(cmd)
	cmd.Flags().StringVar(&name, "name", "", "What the token is for, e.g. github-actions (required)")
	cmd.Flags().StringVar(&scopes, "scopes", "", "Comma-separated scopes, e.g. deploy:read,deploy:write (required)")
	cmd.Flags().DurationVar(&expires, "expires-in", 0, "Expire after this long (default: never)")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("scopes")
	return cmd
}

// parseTokenScopes validates scopes against forge's own vocabulary BEFORE the
// round trip, so a typo ("deploy:writes") is refused here with the list,
// rather than minting a token that cannot do what CI needs.
func parseTokenScopes(raw string) ([]string, error) {
	var out []string
	for _, s := range strings.Split(raw, ",") {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		scope, ok := accesstoken.ParseScope(s)
		if !ok {
			known := make([]string, 0, len(accesstoken.AllScopes))
			for _, k := range accesstoken.AllScopes {
				known = append(known, string(k))
			}
			return nil, fmt.Errorf("unknown scope %q; known scopes: %s", s, strings.Join(known, ", "))
		}
		out = append(out, string(scope))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--scopes is empty: name at least one, e.g. deploy:read,deploy:write")
	}
	// A registry-only scope is a whole credential (accesstoken.RegistryOnly):
	// refused here, with why, rather than by the server after the round trip.
	if set := accesstoken.KnownScopes(out); set.RegistryOnly() {
		for _, s := range out {
			if !accesstoken.RegistryOnly(accesstoken.Scope(s)) {
				return nil, fmt.Errorf("--scopes mixes a registry-only scope with %s: a sourcemaps:write token carries nothing else, so mint a separate token for %s", s, s)
			}
		}
	}
	return out, nil
}

type cloudTokenCreateDocument struct {
	jsonEnvelope
	Token wireAccessToken `json:"token"`
	// Secret is the plaintext — returned exactly once by the server. Pipe
	// it into the secret store; it cannot be read back.
	Secret string `json:"secret"`
}

func runCloudTokenCreate(ctx context.Context, client cloudCaller, name string, scopes []string, expires time.Duration, jsonOut bool, out io.Writer) error {
	req := map[string]any{"name": name, "scopes": scopes}
	if expires > 0 {
		req["expiresAt"] = time.Now().Add(expires).UTC().Format(time.RFC3339)
	}
	var resp struct {
		Token  wireAccessToken `json:"token"`
		Secret string          `json:"secret"`
	}
	if err := client.Call(ctx, procCreateToken, req, &resp); err != nil {
		return tokenAdminRefusal("mint", fmt.Errorf("create token %q: %w", name, err))
	}
	if jsonOut {
		doc := cloudTokenCreateDocument{Token: resp.Token, Secret: resp.Secret}
		doc.stamp(nil)
		return writeJSONDocument(out, doc)
	}
	fmt.Fprintf(out, "Created org token %q (%s), scopes %s\n", resp.Token.Name, resp.Token.ID, strings.Join(resp.Token.Scopes, ","))
	fmt.Fprintf(out, "\n  %s\n\n", resp.Secret)
	fmt.Fprintln(out, "This secret is shown ONCE and cannot be read back. Store it now, e.g.:")
	fmt.Fprintln(out, "  gh secret set FORGE_CONTROL_PLANE_TOKEN")
	return nil
}

func newCloudTokenListCmd() *cobra.Command {
	var (
		f              cloudTokenFlags
		includeRevoked bool
	)
	cmd := &cobra.Command{
		Use:   "list --env <env>",
		Short: "List the org's automation tokens (never their secrets)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := f.client(cmd.Context())
			if err != nil {
				return err
			}
			return runCloudTokenList(cmd.Context(), client, includeRevoked, f.jsonOut, cmd.OutOrStdout())
		},
	}
	f.register(cmd)
	cmd.Flags().BoolVar(&includeRevoked, "include-revoked", false, "Also list revoked tokens")
	return cmd
}

type cloudTokenListDocument struct {
	jsonEnvelope
	Tokens []wireAccessToken `json:"tokens"`
}

func runCloudTokenList(ctx context.Context, client cloudCaller, includeRevoked, jsonOut bool, out io.Writer) error {
	req := map[string]any{}
	if includeRevoked {
		req["includeRevoked"] = true
	}
	var resp struct {
		Tokens []wireAccessToken `json:"tokens"`
	}
	if err := client.Call(ctx, procListTokens, req, &resp); err != nil {
		return tokenAdminRefusal("list", fmt.Errorf("list tokens: %w", err))
	}
	if jsonOut {
		doc := cloudTokenListDocument{Tokens: resp.Tokens}
		if doc.Tokens == nil {
			doc.Tokens = []wireAccessToken{}
		}
		doc.stamp(nil)
		return writeJSONDocument(out, doc)
	}
	if len(resp.Tokens) == 0 {
		fmt.Fprintln(out, "No org tokens.")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tID\tPREFIX\tSCOPES\tLAST USED\tSTATE")
	for _, t := range resp.Tokens {
		state := "active"
		if t.RevokedAt != nil {
			state = "revoked"
		} else if t.ExpiresAt != nil && t.ExpiresAt.Before(time.Now()) {
			state = "expired"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", t.Name, t.ID, emptyDash(t.DisplayPrefix),
			strings.Join(t.Scopes, ","), timeOrDash(t.LastUsedAt), state)
	}
	return tw.Flush()
}

func newCloudTokenRevokeCmd() *cobra.Command {
	var f cloudTokenFlags
	cmd := &cobra.Command{
		Use:   "revoke <token-id> --env <env>",
		Short: "Revoke an org automation token, immediately",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := f.client(cmd.Context())
			if err != nil {
				return err
			}
			return runCloudTokenRevoke(cmd.Context(), client, args[0], f.jsonOut, cmd.OutOrStdout())
		},
	}
	f.register(cmd)
	return cmd
}

type cloudTokenRevokeDocument struct {
	jsonEnvelope
	Revoked string `json:"revoked"`
}

func runCloudTokenRevoke(ctx context.Context, client cloudCaller, id string, jsonOut bool, out io.Writer) error {
	if err := client.Call(ctx, procRevokeToken, map[string]any{"id": id}, &struct{}{}); err != nil {
		return tokenAdminRefusal("revoke", fmt.Errorf("revoke token %s: %w", id, err))
	}
	if jsonOut {
		doc := cloudTokenRevokeDocument{Revoked: id}
		doc.stamp(nil)
		return writeJSONDocument(out, doc)
	}
	fmt.Fprintf(out, "Revoked token %s. Anything still using it now gets 401.\n", id)
	return nil
}

// tokenAdminRefusal turns a control plane's refusal of token administration
// to a CLI login (reason person_session_required) into where to do it
// instead: the web app's Settings → API tokens. Every other error, including
// a refusal without that reason, is returned unchanged. It reacts to the
// server's answer only, so a control plane that admits a CLI login is never
// refused by forge.
func tokenAdminRefusal(action string, err error) error {
	var ce *cloud.Error
	if !errors.As(err, &ce) || !ce.HasReason(cloud.ReasonPersonSessionRequired) {
		return err
	}
	return &personSessionRefusal{action: action, err: ce}
}

// personSessionRefusal is a token verb refused because the control plane
// wants a person in the web app, not a CLI login. Exit 9 (exitNeedsHuman):
// re-running changes nothing, and a person has to act — in the web app.
// Unwrap keeps the control plane's code and reason in a --json document's
// error facts.
type personSessionRefusal struct {
	action string // mint | list | revoke
	err    *cloud.Error
}

func (r *personSessionRefusal) Unwrap() error { return r.err }
func (r *personSessionRefusal) ExitCode() int { return exitNeedsHuman }

func (r *personSessionRefusal) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "the control plane at %s does not let a CLI login (`forge login`) %s access tokens: "+
		"a token a CLI login minted would outlive revoking the login.\n", r.err.Endpoint, r.action)
	where := "in the web app's Settings → API tokens"
	if u := cloud.TokenSettingsURL(r.err.Endpoint); u != "" {
		where = "in the web app: " + u + " (Settings → API tokens)"
	} else {
		where += " (the web app of the control plane at " + r.err.Endpoint + ")"
	}
	fmt.Fprintf(&b, "fix: sign in as a person and %s tokens %s.\n", r.action, where)
	if r.action == "mint" {
		fmt.Fprintf(&b, "     Store the secret as your CI's %s; signing in again with `forge login` does not help.", cloud.DefaultTokenEnv)
	} else {
		b.WriteString("     Signing in again with `forge login` does not help.")
	}
	if r.err.Message != "" {
		b.WriteString("\n  server said: " + r.err.Message)
	}
	b.WriteString("\n  reason: " + r.err.Reason)
	return b.String()
}

func timeOrDash(t *time.Time) string {
	if t == nil {
		return "-"
	}
	return t.UTC().Format(time.RFC3339)
}
