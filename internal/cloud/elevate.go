package cloud

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/reliant-labs/forge/pkg/cloudcred"
)

// procExchangeToken is the control plane's on-demand elevation: a credential
// that acts as a person trades itself for a short-lived token carrying a scope
// that person's role holds. The name is the control plane's identifier.
const procExchangeToken = "controlplane.v1.AccessTokenService/ExchangeToken"

// elevationReuseMargin is how close to expiry an elevated token may be and
// still be presented. Past it a fresh one is requested.
const elevationReuseMargin = time.Minute

// elevationCache remembers elevated tokens for this process.
type elevationCache struct {
	mu      sync.Mutex
	entries map[string]elevatedToken
}

type elevatedToken struct {
	token     string
	expiresAt time.Time
}

// elevationScope reports the scope to elevate for when err is a 403 naming a
// missing scope AND this client's credential is one the user did not hand
// forge verbatim.
//
// WHY NOT FOR --token / the token env var. Those are explicit: the user said
// "use exactly this". Silently trading a CI token for something else would make
// "which credential did that command use" unanswerable, and an org automation
// token acts as nobody, so there is no role to elevate from anyway.
//
// WHY ELEVATE AT ALL. The stored `forge login` credential (and a Reliant
// session's token) is deliberately narrow — daemon tokens live on remote
// machines. The person behind it may hold more (an org owner holds
// cluster:manage). The control plane re-reads that role at exchange time, so
// forge asks for exactly the scope a command just proved it needs, for an hour,
// instead of every stored token carrying every permission forever.
func (c *Client) elevationScope(err error) (string, bool) {
	if c.credential().Kind == "human" {
		return "", false
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.HTTPStatus != 403 {
		return "", false
	}
	switch c.credential().Source {
	case SourceLogin, SourceHelper:
	default:
		return "", false
	}
	return apiErr.MissingScope()
}

// elevatedToken returns a live elevated token for scope, exchanging for one
// when none is cached.
func (c *Client) elevatedToken(ctx context.Context, scope string) (string, error) {
	c.elevated.mu.Lock()
	defer c.elevated.mu.Unlock()
	if cached, ok := c.elevated.entries[scope]; ok && time.Until(cached.expiresAt) > elevationReuseMargin {
		return cached.token, nil
	}

	if tok, ok := c.helperScopedToken(scope); ok {
		if c.elevated.entries == nil {
			c.elevated.entries = map[string]elevatedToken{}
		}
		c.elevated.entries[scope] = tok
		return tok.token, nil
	}

	var resp struct {
		Secret string `json:"secret"`
		Token  struct {
			ExpiresAt string `json:"expiresAt"`
		} `json:"token"`
	}
	// The exchange is authenticated by the base credential. It is a plain
	// call, not Call, so a refusal cannot recurse into another elevation.
	if err := c.call(ctx, procExchangeToken, map[string]any{"scopes": []string{scope}}, &resp, c.credential().Token); err != nil {
		return "", err
	}
	if strings.TrimSpace(resp.Secret) == "" {
		return "", errors.New("the control plane answered the exchange with no token")
	}
	expires := time.Now().Add(elevationReuseMargin * 2)
	if at, err := time.Parse(time.RFC3339, resp.Token.ExpiresAt); err == nil {
		expires = at
	}
	if c.elevated.entries == nil {
		c.elevated.entries = map[string]elevatedToken{}
	}
	c.elevated.entries[scope] = elevatedToken{token: resp.Secret, expiresAt: expires}
	return resp.Secret, nil
}

// helperScopedToken asks the credential helper — the host session behind a
// SourceHelper credential — for a token that also carries scope.
//
// WHY THE HELPER FIRST. The helper's token is minted for LoginScopes only, and
// ExchangeToken only ever ATTENUATES the credential it is called with, so
// exchanging that token for obs:read is refused ("an exchange cannot widen a
// credential") even when the session behind it holds obs:read. The helper is
// asked from that session, so it can mint the wider token; the control plane
// still clips it to what the session and the person's role hold. It reports
// ok only when the helper produced a different token that carries scope (or
// does not say what it carries), so a helper that cannot grant it falls
// through to the exchange and its refusal explains itself.
func (c *Client) helperScopedToken(scope string) (elevatedToken, bool) {
	cur := c.credential()
	if cur.Source != SourceHelper {
		return elevatedToken{}, false
	}
	helperValue := os.Getenv(cloudcred.HelperEnv)
	if strings.TrimSpace(helperValue) == "" {
		return elevatedToken{}, false
	}
	argv, err := cloudcred.ParseCommand(helperValue)
	if err != nil {
		return elevatedToken{}, false
	}
	if slices.Contains(LoginScopes(), scope) {
		// The helper was already asked for it; asking again repeats the answer.
		return elevatedToken{}, false
	}
	got, err := helperCredentialFor(argv, c.Endpoint, append(LoginScopes(), scope))
	if err != nil || strings.TrimSpace(got.Token) == "" || got.Token == cur.Token {
		return elevatedToken{}, false
	}
	if len(got.Scopes) > 0 && !slices.Contains(got.Scopes, scope) {
		return elevatedToken{}, false
	}
	expires := time.Now().Add(helperReuseMargin * 2)
	if got.ExpiresAt != nil {
		expires = *got.ExpiresAt
	}
	return elevatedToken{token: got.Token, expiresAt: expires}, true
}

// reelevatedToken forgets rejected for scope and exchanges again. The cache is
// only dropped when it still holds the rejected token, so a concurrent caller
// that already replaced it keeps its newer one.
func (c *Client) reelevatedToken(ctx context.Context, scope, rejected string) (string, error) {
	c.elevated.mu.Lock()
	if cached, ok := c.elevated.entries[scope]; ok && cached.token == rejected {
		delete(c.elevated.entries, scope)
	}
	c.elevated.mu.Unlock()
	return c.elevatedToken(ctx, scope)
}

// annotateElevation keeps the ORIGINAL refusal — it is what the user's command
// hit — and adds why the on-demand elevation did not rescue it. A control plane
// that predates the exchange says so; a role without the permission says that.
func annotateElevation(original error, scope string, elevErr error) error {
	var apiErr *Error
	if !errors.As(original, &apiErr) {
		return original
	}
	reason := elevErr.Error()
	var exchangeErr *Error
	if errors.As(elevErr, &exchangeErr) {
		switch {
		case exchangeErr.HTTPStatus == 404 || exchangeErr.HasCode(CodeUnimplemented):
			reason = "this control plane predates on-demand elevation; it must be upgraded"
		case exchangeErr.Message != "":
			reason = exchangeErr.Message
		}
	}
	apiErr.message = strings.TrimRight(apiErr.Error(), "\n") +
		fmt.Sprintf("\n  elevation: forge asked the control plane for %s on demand and was refused: %s", scope, reason)
	return apiErr
}

// loginLacksScope names the way out when a stored `forge login` credential
// lacks scope, could not be elevated, and a credential helper is configured.
//
// The file credential outranks the helper by design (ResolveCredential: a
// login the user chose is never displaced by an ambient session), so forge
// does not switch identity on its own. But a login made before a scope
// existed is then a permanent 403 even though the helper — the Reliant
// session on this machine — could authorize the call. That was every agent's
// `forge obs` against prod on 2026-10-25: the error said "re-run forge login",
// which an agent cannot do, and never mentioned the helper. Say both options.
func (c *Client) loginLacksScope(err error, scope string) error {
	cred := c.credential()
	if cred.Source != SourceLogin || strings.TrimSpace(os.Getenv(cloudcred.HelperEnv)) == "" {
		return err
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) {
		return err
	}
	apiErr.message = strings.TrimRight(apiErr.Error(), "\n") + fmt.Sprintf(
		"\n  remedy: your `forge login` credential (%s, for %s) lacks %s; run `forge logout --endpoint %s` to use the Reliant credential helper ($%s), or `forge login` again",
		cred.From, c.Endpoint.URL, scope, c.Endpoint.URL, cloudcred.HelperEnv)
	return apiErr
}
