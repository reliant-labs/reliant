// Copyright (c) 2025 Reliant Labs

package connections

import (
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/oauth2"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/vault"
)

// FlowTTL is how long an authorization may take.
const FlowTTL = 10 * time.Minute

// CallbackPath returns the callback path for a provider.
func CallbackPath(providerID string) string {
	return "/integrations/oauth/" + providerID + "/callback"
}

// AppCallbackPath is the web app route a browser flow is relayed to. The app
// finishes the flow there, signed in, with CompleteOAuth.
const AppCallbackPath = "/connections/oauth/callback"

// brokerStore is what the broker needs from persistence.
type brokerStore interface {
	CreateConnection(ctx context.Context, c *core.Connection, secrets []core.ConnectionSecret, ev core.ConnectionEvent) error
	ReauthorizeConnection(ctx context.Context, userID, id string, upd core.ConnectionUpdate, secrets []core.ConnectionSecret, ev core.ConnectionEvent) (*core.Connection, error)
	GetConnection(ctx context.Context, userID, id string) (*core.Connection, error)
	FindByExternalAccount(ctx context.Context, userID, integrationID, externalAccountID string) (*core.Connection, error)
	CreateOAuthFlow(ctx context.Context, f *core.OAuthFlow) error
	PeekOAuthFlow(ctx context.Context, stateHash []byte, now time.Time) (*core.OAuthFlow, error)
	ConsumeOAuthFlow(ctx context.Context, stateHash []byte, now time.Time) (*core.OAuthFlow, error)
}

// Broker runs the server side of the authorization-code + PKCE flow. State is
// a row, not a signed token: a signed state cannot be made single-use.
//
// A flow is bound to the user who started it, and only that user, signed in,
// can finish it. The provider redirects to this server (the redirect URI the
// provider has registered), but the browser that arrives there carries no
// credential for it: the web app is a different site, so no cookie of ours
// survives the round trip, and on the desktop the consent ran in the system
// browser, not the app. So the callback finishes nothing. It relays the code
// to the client that started the flow — the web app's AppCallbackPath on an
// origin this deployment serves, or a desktop app's loopback receiver — and
// that client calls CompleteOAuth with the user's session.
//
// That is also the login-CSRF defence. A code delivered to someone else's
// session fails the user check, and the relay only ever lands somewhere the
// flow's own user is signed in or on that user's own machine, so a flow
// started by one account cannot be finished, by consent in another person's
// browser, into it.
type Broker struct {
	store      brokerStore
	vault      Sealer
	providers  *Registry
	doer       HTTPDoer
	publicURL  string
	appOrigins map[string]bool
	now        func() time.Time
	exchanger  *oauth2.Exchanger
}

// NewBroker builds the broker. publicURL is this server's externally reachable
// base URL, which the provider redirects back to.
func NewBroker(store brokerStore, v Sealer, providers *Registry, doer HTTPDoer, publicURL string) *Broker {
	if doer == nil {
		doer = NewHTTPClient()
	}
	b := &Broker{
		store: store, vault: v, providers: providers, doer: doer,
		publicURL: strings.TrimRight(publicURL, "/"), appOrigins: map[string]bool{}, now: time.Now,
		exchanger: &oauth2.Exchanger{Client: doer, UserAgent: "reliant-connections"},
	}
	if origin, ok := canonicalOrigin(b.publicURL); ok {
		b.appOrigins[origin] = true
	}
	return b
}

// WithAppOrigins adds the origins the web app is served from (the
// deployment's CORS allow-list), which a browser flow may be relayed back to.
// Anything that is not an http(s) origin — "*", app://bundle — is ignored: a
// wildcard is not an allow-list, and a desktop flow relays to its loopback
// receiver instead.
func (b *Broker) WithAppOrigins(origins []string) *Broker {
	for _, o := range origins {
		if origin, ok := canonicalOrigin(o); ok {
			b.appOrigins[origin] = true
		}
	}
	return b
}

// StartParams describes one authorization.
type StartParams struct {
	UserID        string
	IntegrationID string
	Name          string
	ReconnectID   string
	RedirectAfter string
	Params        map[string]string
	// ClientOrigin is the Origin of the web app that started the flow (the
	// request's Origin header, which page script cannot set). The provider's
	// redirect is relayed to its AppCallbackPath, so it must be an origin
	// this deployment serves.
	ClientOrigin string
	// LoopbackRedirect is a desktop app's loopback receiver
	// (http://127.0.0.1:<port>/...). When set, the redirect is relayed there
	// instead of to ClientOrigin.
	LoopbackRedirect string
}

// userBinder is what a flow is bound to: the user who started it.
func userBinder(userID string) string { return "user:" + userID }

func binderHash(binder string) []byte {
	sum := sha256.Sum256([]byte("oauth-session\x00" + binder))
	return sum[:]
}

func stateHash(state string) []byte {
	sum := sha256.Sum256([]byte(state))
	return sum[:]
}

// SafeRedirectAfter validates a post-authorization landing path. Only a
// same-origin relative path passes: anything with a scheme, a host, a
// protocol-relative prefix, a backslash (browsers treat it as a slash) or a
// control character is refused, so the callback cannot become an open redirect.
func SafeRedirectAfter(raw string) (string, bool) {
	if raw == "" {
		return "", true
	}
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.Contains(raw, `\`) {
		return "", false
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil {
		return "", false
	}
	return raw, true
}

// canonicalOrigin reduces an http(s) origin (or a URL, of which only the
// origin is kept) to scheme://host[:port], lower-cased and without a default
// port, so an allow-list match is an exact string compare.
func canonicalOrigin(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.User != nil || u.Hostname() == "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	host, port := strings.ToLower(u.Hostname()), u.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return scheme + "://" + host, true
}

// isLoopbackHost reports whether host names this machine. A relay to it can
// only reach the user's own machine, never a third party.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// returnTo resolves where a flow's provider redirect is relayed. It is never
// an arbitrary client-supplied URL: either a loopback receiver, which can only
// reach the user's own machine, or the app's callback route on an origin this
// deployment serves (or a loopback origin, which is a local web app).
func (b *Broker) returnTo(p StartParams) (string, error) {
	if p.LoopbackRedirect != "" {
		u, err := url.Parse(p.LoopbackRedirect)
		if err != nil || u.Scheme != "http" || !isLoopbackHost(u.Hostname()) || u.Port() == "" ||
			u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return "", newError(CodeInvalidArgument, "loopback_redirect must be an http URL on a loopback address with a port")
		}
		return u.String(), nil
	}
	origin, ok := canonicalOrigin(p.ClientOrigin)
	if !ok {
		return "", newError(CodeInvalidArgument, "a browser sign-in must be started from the web app: the request carried no usable Origin")
	}
	if u, _ := url.Parse(origin); !b.appOrigins[origin] && !isLoopbackHost(u.Hostname()) {
		return "", newError(CodeFailedPrecondition, "%s is not an origin this deployment serves the app from (CORS_ALLOWED_ORIGINS)", origin)
	}
	return origin + AppCallbackPath, nil
}

func (b *Broker) redirectURI(providerID string) (string, error) {
	if b.publicURL == "" {
		return "", newError(CodeFailedPrecondition, "PUBLIC_URL is not configured, so the provider has nowhere to redirect back to")
	}
	return b.publicURL + CallbackPath(providerID), nil
}

// provider returns an integration that offers oauth2 and whose deployment
// configured its client.
func (b *Broker) provider(id string) (*Provider, error) {
	p, ok := b.providers.Get(id)
	if !ok {
		return nil, newError(CodeNotFound, "unknown integration %q", id)
	}
	if _, ok := p.Method(MethodOAuth2); !ok {
		return nil, newError(CodeFailedPrecondition, "%s does not connect through a sign-in flow", p.DisplayName)
	}
	if st := p.MethodStatus(MethodOAuth2); !st.Available {
		return nil, newError(CodeFailedPrecondition, "integration %q is not available: %s", id, st.Reason)
	}
	return p, nil
}

// oauthSpec is the provider's oauth2 declaration (provider() checked it exists).
func oauthSpec(p *Provider) *reliantv1.OAuth2Auth {
	m, _ := p.Method(MethodOAuth2)
	return m.GetOauth2()
}

// authorizeExtra is the manifest's extra authorize parameters. The loader
// already refused any the flow sets itself.
func authorizeExtra(o *reliantv1.OAuth2Auth) url.Values {
	extra := url.Values{}
	for k, v := range o.GetAuthorizeParams() {
		extra.Set(k, v)
	}
	return extra
}

// Start records a flow and returns the provider authorization URL.
func (b *Broker) Start(ctx context.Context, p StartParams) (string, error) {
	prov, err := b.provider(p.IntegrationID)
	if err != nil {
		return "", err
	}
	if p.UserID == "" {
		return "", newError(CodeInvalidArgument, "an authenticated user is required")
	}
	redirectAfter, ok := SafeRedirectAfter(p.RedirectAfter)
	if !ok {
		return "", newError(CodeInvalidArgument, "redirect_after must be a relative path")
	}
	returnTo, err := b.returnTo(p)
	if err != nil {
		return "", err
	}
	redirectURI, err := b.redirectURI(prov.ID)
	if err != nil {
		return "", err
	}
	// A new connection takes its params from the caller; a reconnect keeps the
	// connection's own, so it cannot be moved to another tenant.
	params := p.Params
	if p.ReconnectID != "" {
		conn, err := b.store.GetConnection(ctx, p.UserID, p.ReconnectID)
		if err != nil {
			return "", mapStoreErr(err)
		}
		if conn.IntegrationID != prov.ID {
			return "", newError(CodeInvalidArgument, "that connection belongs to a different integration")
		}
		if len(p.Params) > 0 {
			return "", newError(CodeInvalidArgument, "a reconnect keeps the connection's params")
		}
		params = conn.Params
	}
	params, err = prov.checkParams(params)
	if err != nil {
		return "", err
	}
	spec := oauthSpec(prov)
	authorizeURL, err := prov.endpoint(spec.GetAuthorizeUrl(), params)
	if err != nil {
		return "", err
	}

	state, err := oauth2.NewState()
	if err != nil {
		return "", &Error{Code: CodeInternal, Message: "generating state", Err: err}
	}
	verifier, err := oauth2.NewVerifier()
	if err != nil {
		return "", &Error{Code: CodeInternal, Message: "generating pkce verifier", Err: err}
	}
	hash := stateHash(state)
	sealed, err := b.vault.Seal(ctx, vault.UserTenant(p.UserID), []byte(verifier.Secret()), flowAAD(hash))
	if err != nil {
		return "", &Error{Code: CodeInternal, Message: "sealing pkce verifier", Err: err}
	}
	if err := b.store.CreateOAuthFlow(ctx, &core.OAuthFlow{
		StateHash: hash, UserID: p.UserID, SessionIDHash: binderHash(userBinder(p.UserID)), IntegrationID: prov.ID,
		PKCEVerifierSealed: sealed, RedirectAfter: redirectAfter, ReconnectConnectionID: p.ReconnectID,
		ConnectionName: strings.TrimSpace(p.Name), Params: params, ReturnTo: returnTo, ExpiresAt: b.now().Add(FlowTTL),
	}); err != nil {
		return "", mapStoreErr(err)
	}
	challenge := verifier.Challenge()
	if spec.GetPkce() == string(oauth2.MethodPlain) {
		if challenge, err = verifier.ChallengeWithMethod(oauth2.MethodPlain); err != nil {
			return "", &Error{Code: CodeInternal, Message: "deriving pkce challenge", Err: err}
		}
	}
	return oauth2.AuthRequest{
		Endpoint:       authorizeURL,
		ClientID:       prov.ClientID,
		RedirectURI:    redirectURI,
		Scopes:         spec.GetScopes(),
		ScopeSeparator: scopeSeparator(spec),
		State:          state,
		Challenge:      challenge,
		Extra:          authorizeExtra(spec),
	}.URL()
}

// scopeSeparator is the manifest's scope_separator as forge/pkg/oauth2 takes
// it: "" (a space, RFC 6749) or "," (Slack's v2 bot scopes). The loader
// refused anything else.
func scopeSeparator(o *reliantv1.OAuth2Auth) string {
	if o.GetScopeSeparator() == "," {
		return ","
	}
	return ""
}

// CompleteParams is one relayed callback, finished by the signed-in client.
type CompleteParams struct {
	// ProviderID, when set, must be the flow's integration.
	ProviderID string
	State      string
	Code       string
	// UserID is the authenticated caller finishing the flow.
	UserID string
}

// Completion is what a finished flow produced.
type Completion struct {
	Connection    *core.Connection
	RedirectAfter string
}

// Complete consumes the flow, exchanges the code and stores the connection.
//
// The flow is consumed BEFORE anything else is checked, so a replay, a wrong
// session, or a failed exchange all burn the state: an attacker gets one try
// with a captured state, not unlimited ones.
func (b *Broker) Complete(ctx context.Context, p CompleteParams) (*Completion, error) {
	if p.State == "" || p.Code == "" {
		return nil, newError(CodeInvalidArgument, "state and code are required")
	}
	if p.UserID == "" {
		return nil, newError(CodeInvalidArgument, "an authenticated user is required")
	}
	hash := stateHash(p.State)
	flow, err := b.store.ConsumeOAuthFlow(ctx, hash, b.now())
	if err != nil {
		return nil, flowErr(err)
	}
	// Login-CSRF defence: the flow must be finished by the user who started
	// it, not merely by someone holding a valid state and code.
	if !constantTimeEqual(flow.SessionIDHash, binderHash(userBinder(p.UserID))) {
		return nil, newError(CodeFailedPrecondition, "this authorization was started by a different account")
	}
	if p.ProviderID != "" && p.ProviderID != flow.IntegrationID {
		return nil, newError(CodeFailedPrecondition, "this authorization was started for a different integration")
	}
	prov, err := b.provider(flow.IntegrationID)
	if err != nil {
		return nil, err
	}
	redirectURI, err := b.redirectURI(prov.ID)
	if err != nil {
		return nil, err
	}

	verifierBytes, err := b.vault.Open(ctx, vault.UserTenant(flow.UserID), flow.PKCEVerifierSealed, flowAAD(hash))
	if err != nil {
		return nil, &Error{Code: CodeInternal, Message: "opening pkce verifier", Err: err}
	}
	verifier, err := oauth2.ParseVerifier(string(verifierBytes))
	clear(verifierBytes)
	if err != nil {
		return nil, &Error{Code: CodeInternal, Message: "stored pkce verifier is invalid", Err: err}
	}

	tokenURL, err := prov.endpoint(oauthSpec(prov).GetTokenUrl(), flow.Params)
	if err != nil {
		return nil, err
	}
	var clientSecret string
	_ = prov.ClientSecret.Use(func(b []byte) error { clientSecret = string(b); return nil })
	token, err := b.exchanger.Exchange(ctx, oauth2.TokenRequest{
		Endpoint: tokenURL, ClientID: prov.ClientID, ClientSecret: clientSecret,
		RedirectURI: redirectURI, Code: p.Code, Verifier: verifier,
	})
	if err != nil {
		// The authorization code and the provider's error description stay out
		// of the message; only a class is logged.
		slog.Warn("oauth code exchange failed", "integration", prov.ID, "state", hex8(hash), "class", refreshFailureClass(err))
		var oerr *oauth2.Error
		if errors.As(err, &oerr) && oerr.StatusCode >= 500 {
			return nil, newError(CodeUnavailable, "%s is temporarily unavailable", prov.DisplayName)
		}
		return nil, newError(CodeFailedPrecondition, "%s rejected the authorization", prov.DisplayName)
	}

	who, err := prov.identify(ctx, b.doer, flow.Params, func(r *http.Request) error {
		r.Header.Set("Authorization", "Bearer "+token.AccessToken)
		return nil
	})
	if err != nil {
		return nil, newError(CodeUnavailable, "could not identify the %s account", prov.DisplayName)
	}

	plain := []plainField{{field: core.SecretFieldAccessToken, value: token.AccessToken}}
	if token.RefreshToken != "" {
		plain = append(plain, plainField{field: core.SecretFieldRefreshToken, value: token.RefreshToken})
	}
	var expires *time.Time
	if exp := token.Expiry(b.now()); !exp.IsZero() {
		expires = &exp
	}
	scopes := strings.Fields(strings.ReplaceAll(token.Scope, ",", " "))

	conn, err := b.upsert(ctx, flow, prov, who, scopes, expires, plain)
	if err != nil {
		return nil, err
	}
	slog.Info("connection authorized", "integration", prov.ID, "connection_id", conn.ID, "state", hex8(hash))
	return &Completion{Connection: conn, RedirectAfter: flow.RedirectAfter}, nil
}

// plainField is a credential awaiting sealing; the connection id its AAD binds
// to is only known inside upsert.
type plainField struct{ field, value string }

func (b *Broker) upsert(ctx context.Context, flow *core.OAuthFlow, prov *Provider, who Identity, scopes []string, expires *time.Time, plain []plainField) (*core.Connection, error) {
	var existing *core.Connection
	switch {
	case flow.ReconnectConnectionID != "":
		c, err := b.store.GetConnection(ctx, flow.UserID, flow.ReconnectConnectionID)
		if err != nil {
			return nil, mapStoreErr(err)
		}
		// Reconnecting must not silently swap which account the connection is.
		if c.ExternalAccountID != nil && *c.ExternalAccountID != who.ExternalAccountID {
			return nil, newError(CodeFailedPrecondition, "that login is a different %s account than the one this connection was made with", prov.DisplayName)
		}
		existing = c
	case who.ExternalAccountID != "":
		c, err := b.store.FindByExternalAccount(ctx, flow.UserID, prov.ID, who.ExternalAccountID)
		if err == nil {
			existing = c
		} else if !errors.Is(err, core.ErrConnectionNotFound) {
			return nil, mapStoreErr(err)
		}
	}

	id := uuidConnID()
	if existing != nil {
		id = existing.ID
	}
	sealed := make([]core.ConnectionSecret, 0, len(plain))
	for _, s := range plain {
		ct, err := b.vault.Seal(ctx, vault.UserTenant(flow.UserID), []byte(s.value), SecretAAD(id, s.field))
		if err != nil {
			return nil, &Error{Code: CodeInternal, Message: "sealing credential", Err: err}
		}
		keyID, err := vault.KeyIDOf(ct)
		if err != nil {
			return nil, &Error{Code: CodeInternal, Message: "reading key id", Err: err}
		}
		sealed = append(sealed, core.ConnectionSecret{ConnectionID: id, Field: s.field, VaultKeyID: keyID, Ciphertext: ct})
	}

	if existing != nil {
		kind := core.ConnectionEventReconnected
		ev := userEvent(kind, flow.UserID)
		conn, err := b.store.ReauthorizeConnection(ctx, flow.UserID, id, core.ConnectionUpdate{
			AccountLabel: who.AccountLabel, ExternalAccountID: who.ExternalAccountID, Scopes: scopes,
			OAuthClient: prov.ClientID, AccessExpiresAt: expires,
		}, sealed, ev)
		return conn, mapStoreErr(err)
	}

	name := flow.ConnectionName
	if name == "" {
		name = who.AccountLabel
	}
	if name == "" {
		name = prov.DisplayName
	}
	conn := &core.Connection{
		ID: id, OwnerKind: core.ConnectionOwnerUser, UserID: flow.UserID, IntegrationID: prov.ID,
		AuthKind: core.ConnectionAuthOAuth2, Name: name, Scopes: scopes, Status: core.ConnectionStatusActive,
		AccessExpiresAt: expires, Params: flow.Params,
	}
	if who.AccountLabel != "" {
		conn.AccountLabel = &who.AccountLabel
	}
	if who.ExternalAccountID != "" {
		conn.ExternalAccountID = &who.ExternalAccountID
	}
	client := prov.ClientID
	conn.OAuthClient = &client
	if err := b.store.CreateConnection(ctx, conn, sealed, userEvent(core.ConnectionEventCreated, flow.UserID)); err != nil {
		return nil, mapStoreErr(err)
	}
	return b.store.GetConnection(ctx, flow.UserID, id)
}

func uuidConnID() string { return "conn_" + strings.ReplaceAll(uuid.NewString(), "-", "") }

func hex8(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, 8)
	for _, c := range b[:4] {
		out = append(out, hexdigits[c>>4], hexdigits[c&0xf])
	}
	return string(out)
}

func constantTimeEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

func flowErr(err error) error {
	if errors.Is(err, core.ErrOAuthFlowInvalid) {
		return newError(CodeFailedPrecondition, "this authorization link is invalid, expired or was already used")
	}
	return mapStoreErr(err)
}

// RelayParams is the provider's redirect to the callback, as received.
type RelayParams struct {
	ProviderID string
	State      string
	Code       string
	// Error is the provider's error parameter (access_denied, ...). Only
	// whether it is set is used; its text is never passed on.
	Error string
}

// Relay answers the provider's redirect with the absolute URL to send the
// browser on to: the flow's return target, carrying either the code and
// state for the client to finish with CompleteOAuth, or an error class. The
// flow's relative redirect_after rides along so the client knows where to
// land afterwards.
//
// A successful redirect leaves the flow for CompleteOAuth to consume. A
// refusal, or a redirect without a code, burns it here: nothing will finish
// it, and its state must not stay usable.
func (b *Broker) Relay(ctx context.Context, p RelayParams) (string, error) {
	if p.State == "" {
		return "", newError(CodeInvalidArgument, "the provider sent no state")
	}
	failed := p.Error != "" || p.Code == ""
	hash := stateHash(p.State)
	var (
		flow *core.OAuthFlow
		err  error
	)
	if failed {
		flow, err = b.store.ConsumeOAuthFlow(ctx, hash, b.now())
	} else {
		flow, err = b.store.PeekOAuthFlow(ctx, hash, b.now())
	}
	if err != nil {
		return "", flowErr(err)
	}
	if p.ProviderID != flow.IntegrationID {
		return "", newError(CodeFailedPrecondition, "this authorization was started for a different integration")
	}
	if flow.ReturnTo == "" {
		return "", newError(CodeFailedPrecondition, "this authorization has nowhere to return to; start it again")
	}
	u, err := url.Parse(flow.ReturnTo)
	if err != nil {
		return "", &Error{Code: CodeInternal, Message: "stored return target is invalid", Err: err}
	}
	q := u.Query()
	switch {
	case p.Error != "":
		q.Set("error", "denied")
	case p.Code == "":
		q.Set("error", "invalid")
	default:
		q.Set("code", p.Code)
		q.Set("state", p.State)
	}
	if flow.RedirectAfter != "" {
		q.Set("redirect_after", flow.RedirectAfter)
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}
