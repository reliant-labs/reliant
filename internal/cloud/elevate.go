package cloud

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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

// elevationCache remembers elevated tokens for this process, and the
// exchanges the control plane refused, so a refusal is asked for once.
type elevationCache struct {
	mu      sync.Mutex
	entries map[string]elevatedToken
	refused map[string]error
}

type elevatedToken struct {
	token     string
	expiresAt time.Time
}

// scopeTable maps a procedure to the scope a call to it needs.
type scopeTable struct {
	mu     sync.Mutex
	byProc map[string]string
}

func (t *scopeTable) set(procedure, scope string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.byProc == nil {
		t.byProc = map[string]string{}
	}
	t.byProc[strings.TrimPrefix(procedure, "/")] = scope
}

func (t *scopeTable) get(procedure string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	scope, ok := t.byProc[strings.TrimPrefix(procedure, "/")]
	return scope, ok
}

// DeclareScope says that a call to each of procedures needs scope, so this
// client elevates for it BEFORE the first call instead of learning it from a
// refusal.
//
// WHY DECLARE. Learning a scope from a 403 costs every run one refused call
// per scope: an extra round trip for the command, and a permission denial in
// the control plane's error rate and audit log for a request that was always
// going to be elevated (846 refused ObservabilityService calls in a week, all
// of them `forge obs` runs). A command family knows the scope its procedures
// need, so it says so where it names them.
//
// A declaration is a hint, never an authority: the control plane still
// decides. A credential that is not elevated (--token, the token env var, a
// human session) ignores it, and so does one that already reports carrying
// the scope. A wrong declaration costs a call: when the elevated token is
// refused for another scope, the call falls back to the base credential and
// the reactive path. The elevated token lives in this process only, as a
// reactive one does; nothing is written to the credentials file.
func (c *Client) DeclareScope(scope string, procedures ...string) {
	for _, procedure := range procedures {
		c.scopes.set(procedure, scope)
	}
}

// upfrontScope reports the scope procedure is called with an elevated token
// for: declared, or learned from an earlier refusal on this client, for a
// credential that can be elevated and does not already carry it.
func (c *Client) upfrontScope(procedure string) (string, bool) {
	scope, ok := c.scopes.get(procedure)
	if !ok || !c.elevatable() || c.carries(scope) {
		return "", false
	}
	return scope, true
}

// carries reports whether the base credential holds scope, as far as forge
// knows: the scopes its issuer reported, else the ones `forge login` and the
// credential helper ask for.
func (c *Client) carries(scope string) bool {
	held := c.credential().Scopes
	if len(held) == 0 {
		held = LoginScopes()
	}
	return slices.Contains(held, scope)
}

// callDeclared calls procedure with a token elevated for scope from the start.
// When the elevation is refused, the base credential is presented instead (a
// declaration is not proof the base lacks the scope), and its refusal carries
// why the elevation did not help; the cached refusal means the exchange is not
// asked twice.
func (c *Client) callDeclared(ctx context.Context, procedure string, req, out any, scope string) error {
	token, elevErr := c.elevatedToken(ctx, scope)
	if elevErr != nil {
		return c.elevateAndRetry(ctx, procedure, req, out, c.callBase(ctx, procedure, req, out))
	}
	err := c.callElevated(ctx, procedure, req, out, scope, token)
	if missing, ok := c.elevationScope(err); ok && missing != scope {
		// The procedure needs another scope than declared, and the elevated
		// token carries only the declared one. Start over from the base
		// credential, which may hold the other, and elevate for it if not.
		return c.elevateAndRetry(ctx, procedure, req, out, c.callBase(ctx, procedure, req, out))
	}
	return err
}

// elevateAndRetry is the reactive path. err is a call's outcome; when it is a
// refusal naming a scope this credential can be elevated for, forge exchanges
// for that scope and repeats the call once. A successful elevation is
// remembered for procedure, so the client's later calls to it elevate up
// front: a polling command is refused once, not on every poll.
func (c *Client) elevateAndRetry(ctx context.Context, procedure string, req, out any, err error) error {
	scope, ok := c.elevationScope(err)
	if !ok {
		return err
	}
	token, elevErr := c.elevatedToken(ctx, scope)
	if elevErr != nil {
		return c.loginLacksScope(annotateElevation(err, scope, elevErr), scope)
	}
	c.scopes.set(procedure, scope)
	return c.callElevated(ctx, procedure, req, out, scope, token)
}

// elevatable reports whether this client's credential may be traded for an
// elevated one: a stored login or a helper's token, never one the user handed
// forge verbatim and never a human session. elevationScope says why.
func (c *Client) elevatable() bool {
	cred := c.credential()
	if cred.Kind == "human" {
		return false
	}
	switch cred.Source {
	case SourceLogin, SourceHelper:
		return true
	}
	return false
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
	if !c.elevatable() {
		return "", false
	}
	var apiErr *Error
	if !errors.As(err, &apiErr) || apiErr.HTTPStatus != 403 {
		return "", false
	}
	return apiErr.MissingScope()
}

// elevatedToken returns a live elevated token for scope, exchanging for one
// when none is cached. An exchange the control plane refused is not asked
// again by this client: its answer will not change within one command, and
// each asking is another refusal in the control plane's audit log.
func (c *Client) elevatedToken(ctx context.Context, scope string) (string, error) {
	c.elevated.mu.Lock()
	defer c.elevated.mu.Unlock()
	if cached, ok := c.elevated.entries[scope]; ok && time.Until(cached.expiresAt) > elevationReuseMargin {
		return cached.token, nil
	}
	if refused, ok := c.elevated.refused[scope]; ok {
		return "", refused
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
		if exchangeRefused(err) {
			if c.elevated.refused == nil {
				c.elevated.refused = map[string]error{}
			}
			c.elevated.refused[scope] = err
		}
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

// exchangeRefused reports an exchange the control plane answered with a
// refusal that asking again cannot change: a 4xx other than 401 (a 401 refuses
// the base credential, which a refresh may replace). An outage or a dropped
// connection is not remembered.
func exchangeRefused(err error) bool {
	var apiErr *Error
	return errors.As(err, &apiErr) && apiErr.HTTPStatus >= 400 && apiErr.HTTPStatus < 500 &&
		!apiErr.Unauthenticated() && apiErr.HTTPStatus != http.StatusTooManyRequests
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
