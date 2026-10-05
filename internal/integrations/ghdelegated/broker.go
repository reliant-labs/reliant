// Package ghdelegated authenticates the `github` integration with a token
// DELEGATED by control-plane, the GitHub token authority, instead of one
// reliant stores.
//
// There is ONE GitHub App, control-plane's. In hosted mode (a control plane is
// configured) each user's GitHub App user token lives in control-plane's
// git_credentials, and control-plane renews it. reliant never stores it: at
// call time the worker resolves the RUN OWNER (from the run record, never from
// anything the workflow supplies), asks control-plane for that user's current
// token, applies it as a bearer to exactly one host, and scrubs it from
// everything the call returns.
//
// Self-hosted (no control plane) keeps reliant's own GitHub provider
// (connections.GitHubApp, RELIANT_GITHUB_APP_CLIENT_ID/SECRET): Source routes
// the `github` integration here only when a Broker is configured, and to the
// saved-connection source otherwise.
//
// ── PLUGGING INTO THE DELEGATED AUTH TYPE ─────────────────────────────
//
// Manifest auth (stream A) adds `connection.type: delegated` naming a broker
// id that Go registers. Until that registry lands, this package declares the
// broker contract locally (DelegatedBroker) and Source routes on the
// integration id. When A's registry exists, *Broker registers under BrokerID
// and Source's routing collapses into the registry lookup; the broker itself
// does not change.
package ghdelegated

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	"github.com/reliant-labs/reliant/internal/integrations/httpaction"
)

const (
	// BrokerID is the id a `delegated` connection names to use this broker.
	BrokerID = "controlplane-github"
	// IntegrationID is the integration this broker authenticates.
	IntegrationID = "github"
	// APIHost is the only host a delegated GitHub token is ever sent to.
	APIHost = "api.github.com"
	// ConnectionID is the stable id a delegated credential reports in place of
	// a saved connection's: there is no row, but results and audit still need
	// to say which credential authenticated the call.
	ConnectionID = "controlplane:github"
)

// DelegatedBroker turns a run owner into a credential obtained from an
// external authority. It is the local shape of stream A's delegated-broker
// contract (see the package doc). ownerUserID is reliant's user id for the
// run owner — the IdP subject, which is what control-plane calls the EXTERNAL
// id.
type DelegatedBroker interface {
	Credential(ctx context.Context, ownerUserID string) (httpaction.Credential, error)
}

// tokenSource is what the broker needs from control-plane.
// *gitcredentialclient.Client satisfies it.
type tokenSource interface {
	UserAccessToken(ctx context.Context, externalUserID, provider string) (gitcredentialclient.Token, error)
}

// Broker asks control-plane for the owner's current GitHub token.
type Broker struct {
	tokens tokenSource
	host   string
	logger *slog.Logger
}

// NewBroker returns a broker that pins every credential to api.github.com.
func NewBroker(tokens tokenSource, logger *slog.Logger) *Broker {
	return newBroker(tokens, APIHost, logger)
}

// newBroker lets tests pin to an httptest host. Production code uses NewBroker.
func newBroker(tokens tokenSource, host string, logger *slog.Logger) *Broker {
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	return &Broker{tokens: tokens, host: strings.ToLower(host), logger: logger}
}

var _ DelegatedBroker = (*Broker)(nil)

// Credential fetches the owner's token from control-plane, every call. The
// token is not cached here: control-plane renews it under a lock and is the
// one place that knows whether it was revoked, and a GitHub call is already a
// network round trip.
func (b *Broker) Credential(ctx context.Context, ownerUserID string) (httpaction.Credential, error) {
	if ownerUserID == "" {
		return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition, Message: "the run has no owner to act as"}
	}
	tok, err := b.tokens.UserAccessToken(ctx, ownerUserID, IntegrationID)
	switch {
	case errors.Is(err, gitcredentialclient.ErrNotConnected):
		return nil, &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition,
			Message: "GitHub is not connected: connect GitHub in Settings to let workflows act as you", Err: err}
	case errors.Is(err, gitcredentialclient.ErrNeedsReconnect):
		return nil, &httpaction.CredentialError{Code: httpaction.CodeNeedsReauth,
			Message: "GitHub needs to be reconnected: reconnect GitHub in Settings", Err: err}
	case err != nil:
		// Transient (control-plane or GitHub unreachable). The error text
		// cannot carry the token — it was never received — but it is logged
		// rather than surfaced, so a control-plane URL never reaches a user.
		b.logger.Warn("fetching delegated GitHub token from control-plane failed", "err", err)
		return nil, &httpaction.CredentialError{Code: httpaction.CodeUnavailable,
			Message: "could not get a GitHub token right now; try again", Err: err}
	}
	if tok.Empty() {
		return nil, &httpaction.CredentialError{Code: httpaction.CodeInternal, Message: "control-plane returned no GitHub token"}
	}
	return &credential{token: tok, host: b.host}, nil
}

// credential applies a delegated token to requests for exactly one host.
type credential struct {
	token gitcredentialclient.Token
	host  string
}

func (c *credential) ConnectionID() string { return ConnectionID }

// Apply writes the bearer token, but ONLY to an https request for the pinned
// host. httpaction pins a credential to the host a call STARTED at; this is
// the other half — the host the call is allowed to start at. A manifest whose
// base_url points anywhere else cannot receive the token, whatever it says.
func (c *credential) Apply(req *http.Request) error {
	if req.URL == nil || req.URL.Scheme != "https" || !strings.EqualFold(req.URL.Host, c.host) {
		host := ""
		if req.URL != nil {
			host = req.URL.Host
		}
		return &httpaction.CredentialError{Code: httpaction.CodeFailedPrecondition,
			Message: fmt.Sprintf("a GitHub token is only ever sent to https://%s, not %q", c.host, host)}
	}
	return c.token.Use(func(accessToken string) error {
		req.Header.Set("Authorization", "Bearer "+accessToken)
		return nil
	})
}

// Scrub removes the token from s, whether or not it has been applied yet, so
// an error raised before Apply is covered too.
func (c *credential) Scrub(s string) string {
	_ = c.token.Use(func(accessToken string) error {
		if len(accessToken) >= 4 {
			s = strings.ReplaceAll(s, accessToken, "[redacted]")
		}
		return nil
	})
	return s
}

// A credential holds plaintext, so it must not print it.
func (c *credential) String() string             { return "ghdelegated.credential{" + c.host + "}" }
func (c *credential) GoString() string           { return c.String() }
func (c *credential) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, c.String()) }
func (c *credential) LogValue() slog.Value       { return slog.StringValue(c.String()) }
