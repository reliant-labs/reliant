// Copyright (c) 2025 Reliant Labs

package connections

import (
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
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

// brokerStore is what the broker needs from persistence.
type brokerStore interface {
	CreateConnection(ctx context.Context, c *core.Connection, secrets []core.ConnectionSecret, ev core.ConnectionEvent) error
	ReauthorizeConnection(ctx context.Context, userID, id string, upd core.ConnectionUpdate, secrets []core.ConnectionSecret, ev core.ConnectionEvent) (*core.Connection, error)
	GetConnection(ctx context.Context, userID, id string) (*core.Connection, error)
	FindByExternalAccount(ctx context.Context, userID, integrationID, externalAccountID string) (*core.Connection, error)
	CreateOAuthFlow(ctx context.Context, f *core.OAuthFlow) error
	ConsumeOAuthFlow(ctx context.Context, stateHash []byte, now time.Time) (*core.OAuthFlow, error)
}

// Broker runs the server side of the authorization-code + PKCE flow. State is
// a row, not a signed token: a signed state cannot be made single-use.
type Broker struct {
	store     brokerStore
	vault     Sealer
	providers *Registry
	doer      HTTPDoer
	publicURL string
	now       func() time.Time
	exchanger *oauth2.Exchanger
}

// NewBroker builds the broker. publicURL is this server's externally reachable
// base URL, which the provider redirects back to.
func NewBroker(store brokerStore, v Sealer, providers *Registry, doer HTTPDoer, publicURL string) *Broker {
	if doer == nil {
		doer = NewHTTPClient()
	}
	return &Broker{
		store: store, vault: v, providers: providers, doer: doer,
		publicURL: strings.TrimRight(publicURL, "/"), now: time.Now,
		exchanger: &oauth2.Exchanger{Client: doer, UserAgent: "reliant-connections"},
	}
}

// StartParams describes one authorization.
type StartParams struct {
	UserID        string
	IntegrationID string
	// Binder ties the flow to the party that started it: a random value held
	// in a __Host- cookie for the browser path, or the caller's identity for
	// the authenticated-RPC path. Only its hash is stored.
	Binder        string
	Name          string
	ReconnectID   string
	RedirectAfter string
	Params        map[string]string
}

// RPCBinder is the binder for flows started and completed over authenticated
// RPC. Cookie-bound and RPC-bound flows cannot be completed through each other.
func RPCBinder(userID string) string { return "rpc:" + userID }

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
	if p.UserID == "" || p.Binder == "" {
		return "", newError(CodeInvalidArgument, "an authenticated user and session binder are required")
	}
	redirectAfter, ok := SafeRedirectAfter(p.RedirectAfter)
	if !ok {
		return "", newError(CodeInvalidArgument, "redirect_after must be a relative path")
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
		StateHash: hash, UserID: p.UserID, SessionIDHash: binderHash(p.Binder), IntegrationID: prov.ID,
		PKCEVerifierSealed: sealed, RedirectAfter: redirectAfter, ReconnectConnectionID: p.ReconnectID,
		ConnectionName: strings.TrimSpace(p.Name), Params: params, ExpiresAt: b.now().Add(FlowTTL),
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

// CompleteParams is one callback.
type CompleteParams struct {
	ProviderID string
	State      string
	Code       string
	Binder     string
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
	hash := stateHash(p.State)
	flow, err := b.store.ConsumeOAuthFlow(ctx, hash, b.now())
	if err != nil {
		if errors.Is(err, core.ErrOAuthFlowInvalid) {
			return nil, newError(CodeFailedPrecondition, "this authorization link is invalid, expired or was already used")
		}
		return nil, mapStoreErr(err)
	}
	// Login-CSRF defence: the callback must come from the party that started
	// the flow, not merely from someone holding a valid state.
	if !constantTimeEqual(flow.SessionIDHash, binderHash(p.Binder)) {
		return nil, newError(CodeFailedPrecondition, "this authorization was started in a different session")
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

// Abandon burns a flow the provider reported as refused (access_denied), so
// its state cannot be replayed afterwards. It returns the flow's landing path.
func (b *Broker) Abandon(ctx context.Context, state, binder string) string {
	if state == "" {
		return ""
	}
	flow, err := b.store.ConsumeOAuthFlow(ctx, stateHash(state), b.now())
	if err != nil || !constantTimeEqual(flow.SessionIDHash, binderHash(binder)) {
		return ""
	}
	return flow.RedirectAfter
}
