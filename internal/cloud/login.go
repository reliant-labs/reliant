package cloud

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/credentials"
	"github.com/reliant-labs/forge/pkg/oauth2"
)

// ClientID is forge's OAuth client id at a control plane.
const ClientID = "forge-cli"

// LoginScopes names the CLI's requested use cases on the OAuth approval page.
// Human login authenticates the user, not an org grant snapshot; the server
// authorizes each target operation against current membership and permissions.
func LoginScopes() []string {
	return []string{"deploy:read", "deploy:write", "secret:read", "secret:write", "domain:read", "domain:write"}
}

// BrowserLogin runs the interactive half of `forge login`: the OAuth 2.0
// authorization-code flow with PKCE (RFC 7636) against the endpoint's
// /oauth/authorize and /oauth/token, with a loopback redirect (RFC 8252).
//
// The mechanics are forge/pkg/oauth2.LoopbackLogin, which `reliant auth login`
// also runs. forge depends on no reliant code, deliberately. A user deploying to
// hosted infra without the reliant harness must never have to install reliant
// first, so `forge login` stands alone.
type BrowserLogin struct {
	// Endpoint is the control plane being logged in to.
	Endpoint Endpoint
	// OpenURL is the browser launcher. Injectable so a test drives the
	// whole flow without opening a real browser.
	OpenURL func(string) error
	// Timeout bounds the wait for the browser. A login that hangs forever is
	// indistinguishable from one about to succeed.
	Timeout time.Duration
	// Out receives the "open this URL" notice.
	Out io.Writer
}

// Run performs the flow and returns the credential to store.
func (b BrowserLogin) Run(ctx context.Context) (credentials.Credential, error) {
	open := b.OpenURL
	if open == nil {
		open = OpenBrowser
	}
	if b.Out != nil {
		fmt.Fprintf(b.Out, "Logging in to %s\n", b.Endpoint.URL)
	}
	timeout := b.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	tok, err := oauth2.LoopbackLogin{
		AuthorizeEndpoint: b.Endpoint.URL + "/oauth/authorize",
		TokenEndpoint:     b.Endpoint.URL + "/oauth/token",
		ClientID:          ClientID,
		Scopes:            LoginScopes(),
		Extra:             url.Values{"device": {DeviceLabel()}},
		OpenURL:           open,
		Out:               b.Out,
		Timeout:           timeout,
	}.Run(ctx)
	if err != nil {
		if strings.Contains(err.Error(), "timed out") {
			return credentials.Credential{}, fmt.Errorf("%w\n"+
				"fix: complete the flow in the browser, or use `forge login --token <token>` "+
				"(also what CI uses, since a pipeline has no browser)", err)
		}
		return credentials.Credential{}, fmt.Errorf("login to %s failed: %w", b.Endpoint.URL, err)
	}
	switch tokenCredentialKind(tok) {
	case KindHuman, KindOrgLogin:
	default:
		return credentials.Credential{}, fmt.Errorf("control plane returned an unknown credential kind %q for login; upgrade forge", tokenCredentialKind(tok))
	}
	return CredentialFromToken(tok, time.Now()), nil
}

// Credential kinds a browser login can produce.
//
// KindHuman is an identity across the user's memberships: the server resolves
// every operation's organization from its hosted project target.
//
// KindOrgLogin is what a control plane that predates human identities issues
// for the same flow (its token response carries no credential_kind): an
// `rlat_` token bound to the user's organization. It is a delegated
// capability, never reclassified as an identity, so it takes the delegated
// path (GetTenant, no ResolveHostedProject) that such a control plane serves.
// Once the control plane issues human identities, the next `forge login`
// stores one instead.
const (
	KindHuman    = "human"
	KindOrgLogin = "org-login"
)

func tokenCredentialKind(tok *oauth2.Token) string {
	kind, _ := tok.Raw["credential_kind"].(string)
	if kind == "" {
		return KindOrgLogin
	}
	return kind
}

// CredentialFromToken turns a token response into the stored entry.
func CredentialFromToken(tok *oauth2.Token, now time.Time) credentials.Credential {
	c := credentials.Credential{
		Kind:        tokenCredentialKind(tok),
		Token:       tok.AccessToken,
		TokenPrefix: displayPrefix(tok.AccessToken),
		Scopes:      strings.Fields(tok.Scope),
		CreatedAt:   now.UTC(),
	}
	c.AccountID, _ = tok.Raw["account_id"].(string)
	c.AccountEmail, _ = tok.Raw["account_email"].(string)
	if exp := tok.Expiry(now); !exp.IsZero() {
		exp = exp.UTC()
		c.ExpiresAt = &exp
	}
	return c
}

// displayPrefix is the safe-to-print head of a token: its type prefix plus a
// few characters, enough to match it against the web UI's token list.
func displayPrefix(token string) string {
	const n = 13 // "rlat_" + 8
	if len(token) <= n {
		return ""
	}
	return token[:n]
}

// DeviceLabel names this machine in the minted token's name
// (forge-cli@<device>). A re-login from the same device replaces that device's
// token server-side rather than accumulating new ones.
func DeviceLabel() string {
	host, err := os.Hostname()
	if err != nil || strings.TrimSpace(host) == "" {
		return "cli"
	}
	return strings.TrimSuffix(host, ".local")
}

// VerifyToken checks a credential against the endpoint before storing it, so
// `forge login --token` fails at login rather than at the first real command.
func VerifyToken(ctx context.Context, ep Endpoint, cred Credential) error {
	client := NewClient(ep, cred)
	var out json.RawMessage
	return client.Call(ctx, "controlplane.v1.DeployService/ListReleases",
		map[string]any{"limit": 1}, &out)
}

// OpenBrowser launches the platform's URL handler.
func OpenBrowser(target string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", target).Start()
	case "linux":
		return exec.Command("xdg-open", target).Start()
	case "windows":
		return exec.Command("cmd", "/c", "start", target).Start()
	default:
		return fmt.Errorf("cannot open a browser on %s — visit the URL above by hand", runtime.GOOS)
	}
}
