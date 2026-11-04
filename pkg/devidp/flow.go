package devidp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// The WHOLE OIDC flow, executed on the SERVER.
//
// ── What this file is for ─────────────────────────────────────────────
//
// The browser never talks to the identity provider. Not to /authorize, not
// to the discovery document, not to the token endpoint, not through a
// hidden iframe. It talks to YOUR backend, and your backend talks to the
// issuer.
//
// That is a stronger position than the usual browser-side PKCE flow, and
// it is a deliberate architectural choice rather than a convenience:
//
//   - NO IdP ORIGIN IN THE BROWSER. Nothing has to be publicly reachable
//     except your own app. An issuer on a private network, behind a VPN,
//     or bound to loopback works unchanged — which is exactly the shape of
//     a self-hosted admin IdP, and the shape that makes the browser flow
//     collapse.
//   - NO TOKENS IN JAVASCRIPT. Access and refresh tokens are created and
//     held server-side. There is nothing in `window` for an XSS to steal.
//   - NO THIRD-PARTY-COOKIE DEPENDENCE. The silent-restore iframe the
//     browser flow relies on is already unreliable and is being switched
//     off by browsers. This flow has no equivalent.
//   - ONE ORIGIN. No CORS between the app and the issuer, and no
//     redirect-URI allowlist to keep in sync with every deploy.
//
// ── The five steps, all server-side ───────────────────────────────────
//
//  1. BeginFlow      — mint PKCE, create the auth request at the issuer.
//  2. CreateSession  — check the user's credentials (login.go).
//  3. FinalizeAuthRequest — exchange the session for a code (login.go).
//  4. RedeemCode     — exchange the code for tokens, using the verifier
//     that never left this process.
//  5. your app       — put the tokens in a session cookie, or wherever
//     your product keeps them.
//
// The browser's entire part is: POST a login name and password to your
// app, and receive a cookie. That is the whole contract.

// PKCE is one flow's proof-of-possession pair.
//
// The verifier NEVER leaves the server. That is the point of doing this
// here: in the browser flow the verifier has to survive a redirect, which
// means sessionStorage, which means it is readable. Here it is a value in
// a struct that lives as long as one login attempt.
type PKCE struct {
	Verifier  string
	Challenge string
}

// NewPKCE generates a verifier and its S256 challenge (RFC 7636).
func NewPKCE() (PKCE, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return PKCE{}, fmt.Errorf("generate PKCE verifier: %w", err)
	}
	verifier := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	return PKCE{
		Verifier:  verifier,
		Challenge: base64.RawURLEncoding.EncodeToString(sum[:]),
	}, nil
}

// FlowConfig is what an app needs to drive the flow: the OAuth client it
// registered, and where the issuer should send the code.
//
// RedirectURI is still required even though no browser is redirected — the
// issuer validates it against the registered allowlist, and the token
// exchange must present the identical value. It is a protocol parameter
// here, not a navigation target.
type FlowConfig struct {
	ClientID    string
	RedirectURI string
	// Scopes requested. Empty defaults to the standard OIDC set.
	Scopes []string
}

func (f FlowConfig) scopes() string {
	if len(f.Scopes) == 0 {
		return "openid profile email"
	}
	return strings.Join(f.Scopes, " ")
}

// Flow is one in-progress login: the issuer's auth request id, and the
// PKCE pair that will redeem it.
//
// Hold this for the duration of ONE login attempt. It is not a session and
// must not be given to a browser: whoever holds the verifier can redeem
// the code.
type Flow struct {
	AuthRequestID string
	PKCE          PKCE
}

// BeginFlow creates an authorization request at the issuer and returns the
// handle needed to complete it.
//
// This calls /authorize the way a browser would, but WITHOUT following the
// redirect: the response's Location header carries the auth request id,
// which is the only part that matters. Nothing navigates.
func (c *Client) BeginFlow(ctx context.Context, cfg FlowConfig) (Flow, error) {
	if strings.TrimSpace(cfg.ClientID) == "" {
		return Flow{}, fmt.Errorf("a client id is required to begin a sign-in")
	}
	if strings.TrimSpace(cfg.RedirectURI) == "" {
		return Flow{}, fmt.Errorf("a redirect URI is required: the issuer validates it even when nothing is redirected")
	}

	pkce, err := NewPKCE()
	if err != nil {
		return Flow{}, err
	}

	query := url.Values{
		"client_id":             {cfg.ClientID},
		"redirect_uri":          {cfg.RedirectURI},
		"response_type":         {"code"},
		"scope":                 {cfg.scopes()},
		"state":                 {"server"}, // see the note below
		"code_challenge":        {pkce.Challenge},
		"code_challenge_method": {"S256"},
	}
	// `state` is CSRF protection for a BROWSER redirect — it exists so a
	// callback arriving at a browser can be matched to a request that
	// browser started. No browser is involved and no callback is received,
	// so there is nothing to correlate: the code is handed to this process
	// directly, in the response to a call this process made. A constant is
	// honest about that; generating a random value would imply a check
	// that nothing performs.

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(c.Base, "/")+"/oauth/v2/authorize?"+query.Encode(), nil)
	if err != nil {
		return Flow{}, err
	}
	if c.Host != "" {
		req.Host = c.Host
	}

	// Do NOT follow the redirect: its Location is the whole answer.
	client := *c.httpClient()
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	resp, err := client.Do(req)
	if err != nil {
		return Flow{}, fmt.Errorf("begin sign-in at the issuer: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	location := resp.Header.Get("Location")
	id, v1 := authRequestIDFrom(location)
	if id == "" {
		return Flow{}, fmt.Errorf(
			"the issuer did not return an auth request id (HTTP %d, Location %q). "+
				"This usually means the login UI is not pointed at this app — see SetLoginUI",
			resp.StatusCode, location)
	}
	if v1 {
		// Refused HERE, before any credential is checked. Carried on, the
		// flow would verify the password (counting a failed attempt against
		// the account if it was wrong) and then fail at finalize with "Auth
		// Request does not exist (COMMAND-jae5P)" — an error that names the
		// request rather than the instance setting that caused it. Observed
		// on a dev instance whose bring-up switched LoginV2 back off.
		return Flow{}, fmt.Errorf(
			"the issuer minted a v1 auth request (Location %q): the LoginV2 feature is off for this "+
				"instance, so it is redirecting to its own login pages and FinalizeAuthRequest cannot "+
				"complete the request. Point the login UI at this app with SetLoginUI "+
				"(PUT /v2/features/instance {\"loginV2\":{\"required\":true,\"baseUri\":<sign-in route>}})",
			location)
	}
	return Flow{AuthRequestID: id, PKCE: pkce}, nil
}

// authRequestIDFrom pulls the request id out of the issuer's redirect, and
// reports whether it is a v1 request.
//
// The two spellings mark different issuer generations: `authRequest` is
// the v2 login flow (what this package drives), `authRequestID` the v1 one,
// which the issuer mints when the LoginV2 feature is off and it redirects
// to its own bundled /ui/login pages instead of the app. Both are read so
// BeginFlow can refuse a v1 request by NAME rather than return an empty id.
func authRequestIDFrom(location string) (id string, v1 bool) {
	if location == "" {
		return "", false
	}
	u, err := url.Parse(location)
	if err != nil {
		return "", false
	}
	q := u.Query()
	if v := strings.TrimSpace(q.Get("authRequest")); v != "" {
		return v, false
	}
	if v := strings.TrimSpace(q.Get("authRequestID")); v != "" {
		return v, true
	}
	return "", false
}

// Tokens is what a completed flow yields.
//
// These are the credentials themselves. They belong in a server-side
// session, or in an HttpOnly cookie — never in a JSON response a script
// can read, which would give away the property this whole file exists to
// establish.
type Tokens struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
}

// RedeemCode exchanges an authorization code for tokens.
//
// The PKCE verifier proves this is the same party that started the flow.
// It has been in this process's memory the entire time, which is what
// makes the proof meaningful — in a browser flow it has to survive a
// redirect in storage a script can read.
//
// No client secret: this is a public client using PKCE. Adding one would
// not improve anything here, and would put a credential in config that
// PKCE already replaced.
func (c *Client) RedeemCode(ctx context.Context, cfg FlowConfig, code string, pkce PKCE) (Tokens, error) {
	if strings.TrimSpace(code) == "" {
		return Tokens{}, fmt.Errorf("an authorization code is required")
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {cfg.RedirectURI},
		"client_id":     {cfg.ClientID},
		"code_verifier": {pkce.Verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(c.Base, "/")+"/oauth/v2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return Tokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c.Host != "" {
		req.Host = c.Host
	}

	body, err := c.do(req)
	if err != nil {
		return Tokens{}, fmt.Errorf("redeem authorization code: %w", err)
	}
	var out Tokens
	if err := json.Unmarshal(body, &out); err != nil {
		return Tokens{}, fmt.Errorf("decode tokens: %w", err)
	}
	if out.AccessToken == "" {
		return Tokens{}, fmt.Errorf("the issuer returned no access token")
	}
	return out, nil
}

// SignIn runs the ENTIRE flow — begin, authenticate, finalize, redeem —
// and returns the tokens. One call, no browser involvement at all.
//
// This is the function an app's POST /auth/login handler wraps. Everything
// it needs from the browser is a login name and a password; everything the
// browser gets back is whatever session representation the app chooses.
//
// The login name may be an EMAIL, which is what a sign-in form asks for. It
// is resolved to the account's real login name first (see loginNameFor):
// the issuer creates sessions by login name, and the two differ for any
// account created with a bare username.
func (c *Client) SignIn(ctx context.Context, cfg FlowConfig, creds Credentials) (Tokens, error) {
	flow, err := c.BeginFlow(ctx, cfg)
	if err != nil {
		return Tokens{}, err
	}
	creds.LoginName = c.loginNameFor(ctx, creds.LoginName)
	session, err := c.CreateSession(ctx, creds)
	if err != nil {
		return Tokens{}, err
	}
	callback, err := c.FinalizeAuthRequest(ctx, flow.AuthRequestID, session)
	if err != nil {
		return Tokens{}, err
	}
	code, err := codeFromCallback(callback)
	if err != nil {
		return Tokens{}, err
	}
	return c.RedeemCode(ctx, cfg, code, flow.PKCE)
}

// SignUp registers an account and signs it in, in one call.
//
// The sign-in is not a convenience: an account created without a session
// leaves the person who just chose a password on a login form retyping it,
// which is the worst step of most sign-up flows and one this shape removes
// for free.
func (c *Client) SignUp(ctx context.Context, cfg FlowConfig, reg Registration) (Tokens, error) {
	if _, err := c.Register(ctx, reg); err != nil {
		return Tokens{}, err
	}
	return c.SignIn(ctx, cfg, Credentials{
		LoginName: reg.Email,
		Password:  reg.Password,
	})
}

// codeFromCallback extracts the authorization code from the callback URL
// the issuer hands back.
//
// The URL is never visited. The issuer expresses "here is the code" as a
// redirect target because that is what the protocol says; this reads the
// parameter out of it and throws the rest away.
func codeFromCallback(callback string) (string, error) {
	u, err := url.Parse(callback)
	if err != nil {
		return "", fmt.Errorf("parse callback URL: %w", err)
	}
	q := u.Query()
	if e := q.Get("error"); e != "" {
		return "", fmt.Errorf("the issuer refused the sign-in: %s (%s)", e, q.Get("error_description"))
	}
	code := q.Get("code")
	if code == "" {
		return "", fmt.Errorf("the issuer's callback carried no authorization code")
	}
	return code, nil
}
