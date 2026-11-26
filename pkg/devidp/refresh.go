package devidp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/reliant-labs/forge/pkg/session"
)

// Staying signed in: the refresh-token grant, token revocation, and the
// adapter that lets a forge/pkg/session Manager hold a broker's tokens.
//
// The broker signs a person in once (flow.go) and keeps the tokens
// SERVER-SIDE in a session (forge/pkg/session); the browser holds only the
// session's opaque id. When the access token is about to expire the session
// redeems the refresh token here — a public client, so the client id and the
// token are the whole request, exactly as the code exchange was.

// ErrRefreshRejected reports that the issuer refused a refresh token
// (invalid_grant): it was revoked, expired, or already spent. It wraps
// session.ErrRefreshRejected, so a session Manager ends the session.
var ErrRefreshRejected = fmt.Errorf("devidp: %w", session.ErrRefreshRejected)

// Refresh redeems refreshToken for fresh tokens (RFC 6749 §6). An issuer that
// rotates refresh tokens returns a new one; the old is then spent.
//
// The client must be registered for the refresh-token grant (forge's identity
// converge registers native-broker apps for it), and the sign-in must have
// asked for the offline_access scope, or no refresh token was issued.
func (c *Client) Refresh(ctx context.Context, cfg FlowConfig, refreshToken string) (Tokens, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return Tokens{}, errors.New("a refresh token is required")
	}
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {refreshToken},
		"client_id":     {cfg.ClientID},
	}
	body, err := c.postForm(ctx, "/oauth/v2/token", form)
	if err != nil {
		var api *apiError
		if errors.As(err, &api) && api.status >= 400 && api.status < 500 && strings.Contains(api.body, "invalid_grant") {
			// The body names the cause (revoked, expired, reused); it is
			// the issuer's own and kept for the log.
			return Tokens{}, fmt.Errorf("%w: %s", ErrRefreshRejected, api.body)
		}
		return Tokens{}, fmt.Errorf("refresh tokens: %w", err)
	}
	var out Tokens
	if err := json.Unmarshal(body, &out); err != nil {
		return Tokens{}, fmt.Errorf("decode tokens: %w", err)
	}
	if out.AccessToken == "" {
		return Tokens{}, errors.New("the issuer returned no access token")
	}
	return out, nil
}

// RevokeToken asks the issuer to revoke token (RFC 7009): a refresh token at
// sign-out, so a copy that outlived the session cannot mint access tokens.
// hint is "refresh_token" or "access_token" ("" sends none). The issuer
// answers 200 for a token it does not know, so an error means it could not
// be asked.
func (c *Client) RevokeToken(ctx context.Context, cfg FlowConfig, token, hint string) error {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	form := url.Values{"token": {token}, "client_id": {cfg.ClientID}}
	if hint != "" {
		form.Set("token_type_hint", hint)
	}
	if _, err := c.postForm(ctx, "/oauth/v2/revoke", form); err != nil {
		return fmt.Errorf("revoke token: %w", err)
	}
	return nil
}

// postForm POSTs an application/x-www-form-urlencoded body to one of the
// issuer's OAuth endpoints, as an unauthenticated public client.
func (c *Client) postForm(ctx context.Context, path string, form url.Values) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.Base, "/")+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c.Host != "" {
		req.Host = c.Host
	}
	return c.do(req)
}

// SessionTokens converts what a flow yielded into what a session stores. The
// access token's expiry is the issuer's expires_in counted from now, else
// the token's own exp claim (read, not verified: it only schedules the next
// refresh), else unknown, which a session treats as due in minutes.
func SessionTokens(t Tokens, now time.Time) session.Tokens {
	out := session.Tokens{AccessToken: t.AccessToken, RefreshToken: t.RefreshToken, IDToken: t.IDToken,
		IssuerSessionID: t.IssuerSession.ID, IssuerSessionToken: t.IssuerSession.Token}
	switch {
	case t.ExpiresIn > 0:
		out.Expiry = now.Add(time.Duration(t.ExpiresIn) * time.Second)
	default:
		if exp, ok := jwtExpiry(t.AccessToken); ok {
			out.Expiry = exp
		}
	}
	return out
}

// jwtExpiry reads a JWT's exp claim without verifying the token.
func jwtExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(raw, &claims) != nil || claims.Exp <= 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.Exp, 0), true
}

// SessionRefresher is the session.Refresher for tokens this client's flow
// issued: Refresh with cfg, converted by SessionTokens.
func (c *Client) SessionRefresher(cfg FlowConfig) session.Refresher {
	return session.RefresherFunc(func(ctx context.Context, refreshToken string) (session.Tokens, error) {
		t, err := c.Refresh(ctx, cfg, refreshToken)
		if err != nil {
			return session.Tokens{}, err
		}
		return SessionTokens(t, time.Now()), nil
	})
}
