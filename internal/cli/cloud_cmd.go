package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cloud"
	"github.com/reliant-labs/forge/pkg/cloudcred"
	"github.com/reliant-labs/forge/pkg/credentials"
)

// controlPlaneDeclaration reads one environment's hosted control-plane
// declaration out of its rendered KCL.
//
// The environment NAME is the only input, and that is the whole design:
// there is no `forge context use`, no current-endpoint file, and nothing
// a previous command could have left behind that changes the answer.
// `forge cloud status staging` and `forge cloud status prod` resolve
// two different endpoints in one process because each reads its own
// env's declaration.
//
// Returns nil (no error) when the env declares none — the default. The
// caller turns that into the actionable message, because what to say
// depends on what the caller was trying to do.
func controlPlaneDeclaration(ctx context.Context, envName string) (*cloud.Declaration, error) {
	entities, err := RenderKCL(ctx, projectDirForKCL(), envName)
	if err != nil {
		return nil, fmt.Errorf("render KCL for env %q: %w", envName, err)
	}
	return declarationFromEntities(entities), nil
}

// declarationFromEntities maps the rendered entity onto the cloud
// package's consumer-side type. Split out from the render so it can be
// tested without invoking KCL.
func declarationFromEntities(entities *KCLEntities) *cloud.Declaration {
	if entities == nil || entities.ControlPlane == nil {
		return nil
	}
	cp := entities.ControlPlane
	if strings.TrimSpace(cp.Endpoint) == "" {
		return nil
	}
	return &cloud.Declaration{
		Endpoint: cp.Endpoint,
		TokenEnv: cp.TokenEnv,
	}
}

// resolveCloudTarget is the one path every hosted command takes: resolve
// the endpoint from the ENV's declaration, then the credential by
// precedence (flag > env var > credentials file entry for the endpoint).
//
// Order matters here. The endpoint resolves FIRST so that an env with no
// declaration gets "this env declares no hosted control plane" rather
// than "no credential" — the second would send a user to `forge login`
// for a problem logging in cannot fix, which is exactly the wrong hint.
func resolveCloudTarget(ctx context.Context, envName, flagToken string) (cloud.Endpoint, cloud.Credential, error) {
	decl, err := controlPlaneDeclaration(ctx, envName)
	if err != nil {
		return cloud.Endpoint{}, cloud.Credential{}, err
	}
	return resolveCloudTargetFor(envName, decl, flagToken)
}

// resolveCloudTargetFor is resolveCloudTarget with the declaration already in
// hand, for a caller that read it without a render (obs_target.go).
func resolveCloudTargetFor(envName string, decl *cloud.Declaration, flagToken string) (cloud.Endpoint, cloud.Credential, error) {
	ep, err := projectEndpoint(envName, decl)
	if err != nil {
		return cloud.Endpoint{}, cloud.Credential{}, err
	}
	cred, err := cloud.ResolveCredential(flagToken, ep)
	if err != nil {
		return cloud.Endpoint{}, cloud.Credential{}, err
	}
	return ep, cred, nil
}

// loginCallbackTimeout bounds how long `forge login` waits for the
// browser to come back. Long enough for a real SSO round trip including
// an MFA prompt, short enough that a flow abandoned in a closed tab
// fails with the --token hint instead of hanging the terminal.
const loginCallbackTimeout = 5 * time.Minute

// openLoginBrowser launches the browser for `forge login`. A seam so the
// command's test drives the real flow against an httptest control plane
// without opening anything.
var openLoginBrowser = cloud.OpenBrowser

// declaredControlPlane is one distinct control plane this project declares,
// with the envs that declare it (for the login summary).
type declaredControlPlane struct {
	Endpoint cloud.Endpoint
	Envs     []string
}

// declaredControlPlanes renders every env under deploy/kcl/ and returns the
// DISTINCT control planes they declare, keyed by normalized endpoint URL, in
// a stable order. An env that does not render is reported in skipped rather
// than failing the whole walk: one broken env must not stop a login to the
// others. Outside a forge project (no forge.yaml up the tree) nothing is
// declared — a stray deploy/kcl/ in an arbitrary directory is not a project's
// declaration. A seam so tests state the project's declarations.
var declaredControlPlanes = func(ctx context.Context) (planes []declaredControlPlane, skipped []string, err error) {
	cfgPath, err := findProjectConfigFile()
	if errors.Is(err, ErrProjectConfigNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	projectDir := filepath.Dir(cfgPath)
	envs, err := ListEnvs(projectDir)
	if err != nil {
		return nil, nil, err
	}
	var decls []envDeclaration
	for _, env := range envs {
		entities, rerr := RenderKCL(ctx, projectDir, env)
		if rerr != nil {
			skipped = append(skipped, fmt.Sprintf("%s (render failed: %v)", env, rerr))
			continue
		}
		decls = append(decls, envDeclaration{Env: env, Decl: declarationFromEntities(entities)})
	}
	return distinctControlPlanes(decls), skipped, nil
}

// envDeclaration is one env's control-plane declaration (nil: none).
type envDeclaration struct {
	Env  string
	Decl *cloud.Declaration
}

// distinctControlPlanes groups env declarations by normalized endpoint. Pure.
// The token_env of the FIRST env (sorted by name) to declare an endpoint is
// the one reported; credentials are keyed by the endpoint alone.
func distinctControlPlanes(decls []envDeclaration) []declaredControlPlane {
	sort.Slice(decls, func(i, j int) bool { return decls[i].Env < decls[j].Env })
	byKey := map[string]int{}
	var out []declaredControlPlane
	for _, d := range decls {
		if d.Decl == nil {
			continue
		}
		ep, err := cloud.ResolveEndpoint(d.Env, d.Decl)
		if err != nil {
			continue
		}
		key, err := credentials.Normalize(ep.URL)
		if err != nil {
			key = ep.URL
		}
		if i, ok := byKey[key]; ok {
			out[i].Envs = append(out[i].Envs, d.Env)
			continue
		}
		byKey[key] = len(out)
		out = append(out, declaredControlPlane{Endpoint: ep, Envs: []string{d.Env}})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Endpoint.URL < out[j].Endpoint.URL })
	return out
}

// loginTargets resolves which control planes `forge login` / `forge logout`
// act on, first match wins:
//
//  1. an explicit --endpoint;
//  2. EVERY distinct control plane this project's envs declare;
//  3. Reliant cloud (cloud.DefaultEndpoint), when nothing declares one —
//     outside a forge project, or in a project that has not declared a
//     control plane yet. This is what makes a plain `go install`'d forge log
//     into prod with no configuration.
//
// Step 3 is refused when an env failed to render: that env might declare a
// different control plane, and logging into the default instead would be a
// guess presented as an answer.
//
// Login is about WHO you are, never WHERE you deploy — so it takes no
// environment, and nothing it stores selects one: a command finds its
// credential by the endpoint ITS env declares. That is also why defaulting is
// safe here and nowhere else: a stored credential sends nothing anywhere.
func loginTargets(ctx context.Context, endpoint string, errOut io.Writer) ([]declaredControlPlane, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint != "" {
		if _, err := credentials.Normalize(endpoint); err != nil {
			return nil, err
		}
		ep, err := cloud.ResolveEndpoint("", &cloud.Declaration{Endpoint: endpoint})
		if err != nil {
			return nil, err
		}
		return []declaredControlPlane{{Endpoint: ep}}, nil
	}
	planes, skipped, err := declaredControlPlanes(ctx)
	if err != nil {
		return nil, err
	}
	for _, s := range skipped {
		fmt.Fprintf(errOut, "note: skipped env %s\n", s)
	}
	if len(planes) > 0 {
		return planes, nil
	}
	if len(skipped) > 0 {
		return nil, fmt.Errorf("no env that rendered declares a control plane, and %d did not render — one of those may declare a different one\n"+
			"fix: repair the env's KCL (forge env render <env>), or name the control plane directly: forge login --endpoint https://…",
			len(skipped))
	}
	ep, err := cloud.ResolveEndpoint("", &cloud.Declaration{Endpoint: cloud.DefaultEndpoint})
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(errOut, "note: no env here declares a control plane; using Reliant cloud (%s), the default. "+
		"Name another with --endpoint.\n", ep.URL)
	return []declaredControlPlane{{Endpoint: ep}}, nil
}

// newLoginCmd is `forge login`: obtain a credential for a hosted control
// plane and store it in the shared credentials file, keyed by endpoint.
//
// It STANDS ALONE. A user who wants to deploy to hosted infrastructure
// without the reliant harness runs this and nothing else — forge depends on
// no reliant code, so `reliant auth login` is never a prerequisite. The two
// CLIs share the FILE (forge/pkg/credentials), which forge owns.
func newLoginCmd() *cobra.Command {
	var (
		endpoint string
		token    string
		noVerify bool
	)

	cmd := &cobra.Command{
		Use:   "login [--endpoint URL]",
		Short: "Authenticate to the control plane(s) this project declares",
		Long: `Obtain a credential for a hosted control plane and store it in the shared
credentials file (` + credentialsPathForHelp() + `, 0600), keyed by the endpoint.

Login is about WHO you are, not WHERE you deploy, so it takes no environment.
With no flag it logs into EVERY distinct control plane this project's envs
declare in KCL:

    control_plane = forge.ControlPlane {}    # Reliant cloud (` + cloud.DefaultEndpoint + `)
    control_plane = forge.ControlPlane {     # any other control plane
        endpoint = "http://127.0.0.1:8090"
    }

(two envs declaring the same endpoint share one login). When nothing declares
one — outside a project, or before any env does — it logs into Reliant cloud.
--endpoint names one directly. There is no "current" server: every forge
command finds its credential by the endpoint its own env declares, so which
env a command acts on is always the env it names — never a login's side
effect.

INTERACTIVE (a human): the OAuth authorization-code flow with PKCE. forge
opens your browser at <endpoint>/oauth/authorize, you sign in and approve,
and the browser returns a one-time code to a temporary listener on a
loopback port (chosen by the OS, so two logins can run at once). forge
redeems it at <endpoint>/oauth/token for an access token (rlat_…, 90 days).

NON-INTERACTIVE (CI): pass --token (with --endpoint when the project declares
more than one control plane), or set the environment variable the environment
declares (FORGE_CONTROL_PLANE_TOKEN by default) and skip login entirely. A
pipeline has no browser, which is what org tokens are for.

SIGNED IN TO RELIANT? You do not need this command. ` + "`reliant forge …`" + ` and every
shell a Reliant agent runs set $` + cloudcred.HelperEnv + `, and forge asks that
helper for a short-lived token minted from your Reliant session for exactly the
control plane an env declares. forge login is for standalone forge (no Reliant)
and for overriding the session with a different identity.

CREDENTIAL PRECEDENCE, when any forge command talks to the control plane:

    1. --token           explicit, beats everything
    2. $<token_env>      the env var the environment declares — CI
    3. the credentials file entry for that env's endpoint — what this command stores
    4. $` + cloudcred.HelperEnv + ` — a host application's session (Reliant's);
       also used when the stored login (3) has expired`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			planes, err := loginTargets(ctx, endpoint, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			if strings.TrimSpace(token) != "" && len(planes) > 1 {
				// A token is minted by ONE control plane; storing it under
				// several would present it to servers that never issued it.
				urls := make([]string, 0, len(planes))
				for _, p := range planes {
					urls = append(urls, p.Endpoint.URL)
				}
				return fmt.Errorf("--token is for one control plane, and this project declares %d: %s\n"+
					"fix: forge login --endpoint <url> --token <token>", len(planes), strings.Join(urls, ", "))
			}
			path, err := cloud.CredentialsPath()
			if err != nil {
				return err
			}
			if helper := strings.TrimSpace(os.Getenv(cloudcred.HelperEnv)); helper != "" {
				// Under a host the user is already signed in; say so before
				// they finish a browser flow they did not need. Not an error:
				// a login stored here deliberately overrides the session.
				fmt.Fprintf(cmd.ErrOrStderr(), "note: $%s is set, so forge already uses your host application's session (Reliant's) "+
					"when nothing is stored. A login stored here takes precedence over that session.\n", cloudcred.HelperEnv)
			}
			for i, p := range planes {
				if i > 0 {
					fmt.Fprintln(out)
				}
				if err := loginOne(ctx, p, token, noVerify, path, out); err != nil {
					return err
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&endpoint, "endpoint", "", "Log into this control plane URL only, instead of every one the project declares (default when none is declared: Reliant cloud)")
	cmd.Flags().StringVar(&token, "token", "", "Store this token instead of running the browser flow")
	cmd.Flags().BoolVar(&noVerify, "no-verify", false, "With --token: store it without checking it against the endpoint")
	return cmd
}

// loginOne obtains and stores the credential for one control plane.
func loginOne(ctx context.Context, p declaredControlPlane, token string, noVerify bool, path string, out io.Writer) error {
	ep := p.Endpoint
	var (
		stored credentials.Credential
		err    error
	)
	if t := strings.TrimSpace(token); t != "" {
		// Verify a pasted token before storing, so a bad one fails
		// HERE rather than at the first real command. A browser login
		// needs no check: the server minted it seconds ago.
		if !noVerify {
			verifyCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			cred := cloud.Credential{Token: t, Source: cloud.SourceFlag, From: "--token"}
			if err := cloud.VerifyToken(verifyCtx, ep, cred); err != nil {
				return fmt.Errorf("the token was not accepted by %s: %w\n"+
					"(pass --no-verify to store it anyway)", ep.URL, err)
			}
		}
		stored = credentials.Credential{Token: t, CreatedAt: time.Now().UTC()}
	} else {
		stored, err = cloud.BrowserLogin{
			Endpoint: ep,
			Out:      out,
			Timeout:  loginCallbackTimeout,
			OpenURL:  openLoginBrowser,
		}.Run(ctx)
		if err != nil {
			return err
		}
	}
	clientID := cloud.ClientID
	if stored.Kind == "human" {
		clientID = cloud.HumanClientID
	}
	if err := credentials.Store(path, ep.URL, clientID, stored); err != nil {
		return fmt.Errorf("store credential: %w", err)
	}
	key, _ := credentials.Normalize(ep.URL)
	fmt.Fprintf(out, "Logged in to %s\n", key)
	if stored.AccountEmail != "" {
		fmt.Fprintf(out, "  Account:     %s\n", stored.AccountEmail)
	}
	if len(p.Envs) > 0 {
		fmt.Fprintf(out, "  Declared by: %s\n", strings.Join(p.Envs, ", "))
	}
	if len(stored.Scopes) > 0 {
		fmt.Fprintf(out, "  Scopes:      %s\n", strings.Join(stored.Scopes, " "))
	}
	if stored.ExpiresAt != nil {
		fmt.Fprintf(out, "  Expires:     %s\n", stored.ExpiresAt.Format(time.RFC3339))
	}
	fmt.Fprintf(out, "  Credential:  %s, stored in %s (inspect with `forge cloud status <env>`, never by opening the file)\n",
		cloud.TokenIdentity(stored.Token), path)
	fmt.Fprintf(out, "In CI, set %s instead — it takes precedence over this file.\n", ep.TokenEnv)
	return nil
}

// newLogoutCmd is `forge logout`: drop the stored credential of the control
// plane(s) — the ones this project declares, or one --endpoint — from the
// shared credentials file. Every other endpoint's entry (including
// reliant's) is untouched. Human logout revokes the device identity across all
// organizations at the endpoint, then forgets it. Delegated capabilities are
// deliberately separate and are never revoked or removed by human logout.
func newLogoutCmd() *cobra.Command {
	var endpoint string
	cmd := &cobra.Command{
		Use:   "logout [--endpoint URL]",
		Short: "Forget the stored credential for this project's control plane(s)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			planes, err := loginTargets(cmd.Context(), endpoint, cmd.ErrOrStderr())
			if err != nil {
				return err
			}
			path, err := cloud.CredentialsPath()
			if err != nil {
				return err
			}
			for _, p := range planes {
				stored, lookupErr := credentials.Lookup(path, p.Endpoint.URL, cloud.HumanClientID)
				if lookupErr != nil && !errors.Is(lookupErr, credentials.ErrNotFound) {
					return lookupErr
				}
				if lookupErr == nil && stored.Kind != "human" {
					return errors.New("human credential entry contains a delegated capability; refusing to remove it without revocation")
				}
				if lookupErr == nil {
					if err := cloud.RevokeIdentity(cmd.Context(), p.Endpoint, stored.Token); err != nil {
						return err
					}
				}
				if errors.Is(lookupErr, credentials.ErrNotFound) {
					// An org-bound browser login (a control plane that
					// predates human identities) is what `forge login`
					// stored, so logout forgets it. A pasted --token or any
					// other delegated entry is left alone.
					legacy, err := credentials.Lookup(path, p.Endpoint.URL, cloud.ClientID)
					if err == nil && legacy.Kind == cloud.KindOrgLogin {
						if _, err := credentials.RemoveRevoked(path, p.Endpoint.URL, cloud.ClientID, legacy.Token); err != nil {
							return err
						}
						key, _ := credentials.Normalize(p.Endpoint.URL)
						fmt.Fprintf(cmd.OutOrStdout(), "Logged out of %s (removed from %s)\n", key, path)
						continue
					}
					fmt.Fprintf(cmd.OutOrStdout(), "Not logged in to %s (nothing in %s)\n", p.Endpoint.URL, path)
					continue
				}
				existed, err := credentials.RemoveRevoked(path, p.Endpoint.URL, cloud.HumanClientID, stored.Token)
				if err != nil {
					return err
				}
				key, _ := credentials.Normalize(p.Endpoint.URL)
				if !existed {
					fmt.Fprintf(cmd.OutOrStdout(), "Not logged in to %s (nothing in %s)\n", key, path)
					continue
				}
				fmt.Fprintf(cmd.OutOrStdout(), "Logged out of %s (removed from %s)\n", key, path)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "Forget this control plane URL only, instead of every one the project declares")
	return cmd
}

// credentialsPathForHelp renders the file location for --help without
// failing help when HOME is unset.
func credentialsPathForHelp() string {
	if p, err := cloud.CredentialsPath(); err == nil {
		return p
	}
	return "~/.config/forge/credentials.json"
}

// newCloudCmd is `forge cloud`: commands that talk to the hosted control
// plane an environment declares.
//
// It is deliberately NARROW. Only operations backed by an implemented
// server-side RPC live here. Adding a subcommand for a stub would produce a
// command that looks supported and returns Unimplemented, which is worse than
// the command not existing. The release history that `forge cloud releases`
// once demonstrated lives on the env noun now: `forge env releases`.
func newCloudCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cloud",
		Short: "Talk to the hosted control plane an environment declares",
		Long: `Commands that reach the hosted control plane declared by an environment's
forge.ControlPlane block.

The endpoint is per-environment and comes from KCL, so which server a
command hits follows from its env argument — not from any stored
"current context".

Authenticate with ` + "`forge login`" + `, or set the declared token env var.`,
	}
	cmd.AddCommand(newCloudStatusCmd())
	cmd.AddCommand(newCloudTokenCmd())
	cmd.AddCommand(newCloudIdentityCmd())
	// StrictGroup, not a bare group: cobra's default would accept
	// `forge cloud <typo>` and exit 0, which for a command that talks to
	// a remote endpoint reads as "it worked".
	return cmdutil.StrictGroup(cmd)
}

// newCloudStatusCmd reports what WOULD be used, without calling anything.
//
// This exists because the most common hosted-auth question is "which
// endpoint and which credential is this about to use", and answering it
// by running a real command conflates configuration problems with
// server-side ones.
func newCloudStatusCmd() *cobra.Command {
	var token string

	cmd := &cobra.Command{
		Use:   "status <env>",
		Short: "Show the endpoint and credential source for an environment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			envName := args[0]
			out := cmd.OutOrStdout()

			decl, err := controlPlaneDeclaration(cmd.Context(), envName)
			if err != nil {
				return err
			}
			ep, err := projectEndpoint(envName, decl)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "env %q\n", ep.Env)
			fmt.Fprintf(out, "  endpoint:  %s   (declared in deploy/kcl/%s/main.k)\n", ep.URL, ep.Env)
			fmt.Fprintf(out, "  token env: %s\n", ep.TokenEnv)

			cred, err := cloud.ResolveCredential(token, ep)
			if err != nil {
				fmt.Fprintf(out, "  credential: NONE\n")
				return err
			}
			// The IDENTITY and the source, never the token. Printing a
			// bearer credential to a terminal puts it in scrollback, CI
			// logs and agent transcripts. This command exists so nobody
			// has to open the credentials file to learn which credential
			// is in use — the file holds the secret.
			fmt.Fprintf(out, "  credential: %s, from %s (%s)\n", cred.Identity(), cred.From, cred.Source)
			if len(cred.Scopes) > 0 {
				fmt.Fprintf(out, "  scopes:     %s\n", strings.Join(cred.Scopes, " "))
			}
			if cred.ExpiresAt != nil {
				fmt.Fprintf(out, "  expires:    %s\n", cred.ExpiresAt.Format(time.RFC3339))
			}
			fmt.Fprintf(out, "  (the secret is never printed; match the prefix in `forge cloud token list --env %s` or the console)\n", ep.Env)
			return nil
		},
	}
	cmd.Flags().StringVar(&token, "token", "", "Credential to use, ahead of the env var and the credentials file")
	return cmd
}

// cloudCaller is the one method this command needs from a client.
// Declared at the CONSUMER so the test can drive it without a real
// endpoint, and so Client stays a concrete type nothing has to satisfy.
type cloudCaller interface {
	Call(ctx context.Context, procedure string, req, out any) error
}
