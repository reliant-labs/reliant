// Package gitcredentialclient asks control-plane for a user's CURRENT git
// provider token.
//
// forge:outbound-io
//
// It is the outbound adapter to control-plane's GitCredentialInternalService:
// it owns the wire format, the internal-service bearer and the vendor→domain
// mapping, and nothing calls in.
//
// control-plane is the GitHub token authority (one GitHub App, control-plane's).
// Its git_credentials table holds each user's App user token and renews it.
// Reliant never stores that token: the worker asks for it at call time, for
// the owner of the run, authenticated with an internal-service JWT
// (INTERNAL_SERVICE_SECRET).
//
// The service is deliberately NOT in the exported public contract
// (control-plane proto/public-api.txt withholds it), so this package speaks the
// Connect unary protocol as plain HTTP+JSON rather than a generated client —
// exactly as internal/accesstokenclient does. The procedure path, the JSON
// field names and the reason codes in client.go ARE the contract; control-
// plane pins them in internal/handlers/git_credential_internal/wire_json_test.go.
//
// User ids are EXTERNAL (the IdP subject) — the id reliant already holds for
// every user. control-plane translates at its boundary.
package gitcredentialclient

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Token is a provider access token and when it stops working.
//
// It holds plaintext. It is a value the caller applies to ONE outbound request
// and drops; String/GoString/Format never print it.
type Token struct {
	accessToken string
	// ExpiresAt is nil for a token the provider never said expires.
	ExpiresAt *time.Time
}

// NewToken builds a Token (tests and fakes).
func NewToken(accessToken string, expiresAt *time.Time) Token {
	return Token{accessToken: accessToken, ExpiresAt: expiresAt}
}

// Use exposes the plaintext to fn and nowhere else.
func (t Token) Use(fn func(accessToken string) error) error { return fn(t.accessToken) }

// Empty reports whether the token carries no credential.
func (t Token) Empty() bool { return t.accessToken == "" }

// Service is the adapter's surface. *Client implements it.
type Service interface {
	// UserAccessToken returns the user's current token for provider (empty
	// means "github"), renewed by control-plane first when it is near expiry.
	//
	// Errors: ErrNotConnected and ErrNeedsReconnect are permanent until the
	// user acts (never retry). Anything else — transport, a control-plane
	// outage, the provider unreachable during renewal — is transient.
	UserAccessToken(ctx context.Context, externalUserID, provider string) (Token, error)
}

// Signer mints the internal-service bearer for each call.
type Signer func() (string, error)

// Deps are the adapter's collaborators.
type Deps struct {
	// BaseURL is the control-plane origin serving GitCredentialInternalService.
	BaseURL string
	// Sign supplies a fresh internal-service token per call.
	Sign Signer
	// HTTPClient performs the calls. nil uses a client with a 10s timeout.
	HTTPClient *http.Client
}

// Typed outcomes. Both are FailedPrecondition at control-plane, told apart by
// the X-Forge-Error-Reason header.
var (
	// ErrNotConnected: the user has no credential for the provider at
	// control-plane (or control-plane does not know the user).
	ErrNotConnected = errors.New("gitcredentialclient: no credential is connected")
	// ErrNeedsReconnect: the credential exists but the user must re-authorize.
	ErrNeedsReconnect = errors.New("gitcredentialclient: the connection needs to be reconnected")
)

var _ Service = (*Client)(nil)
