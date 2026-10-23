package cloud

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
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
