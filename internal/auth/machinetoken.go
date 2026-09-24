package auth

import (
	"context"
	"errors"
	"fmt"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"
)

// Machine credentials are `rlat_` access tokens (forge/pkg/accesstoken),
// resolved by the deployment's token authority. internal/auth knows only the
// narrow shapes below, declared here where they are consumed; the authority
// (internal/tokenauthority) satisfies them.

// IsAccessTokenFormat reports whether a bearer has the exact shape of an
// `rlat_` access token (forge/pkg/accesstoken.HasFormat). It is the ONE
// machine-credential recognizer; no other token family is accepted.
func IsAccessTokenFormat(token string) bool { return fat.HasFormat(token) }

// AccessTokenIntrospector resolves a presented access token to its principal,
// or an error for one that is not live. Transport failures must be reported as
// errors that are NOT credential rejections (see IsCredentialRejection), so a
// control-plane outage is never told to a caller as "your token is bad".
type AccessTokenIntrospector interface {
	Introspect(ctx context.Context, token string) (*fat.Principal, error)
}

// machineTokenKey marks a request authenticated by an access token rather than
// an interactive session.
type machineTokenKey struct{}

// WithMachineToken records that ctx is authenticated by the access token p.
func WithMachineToken(ctx context.Context, p *fat.Principal) context.Context {
	return context.WithValue(ctx, machineTokenKey{}, p)
}

// MachineTokenFromContext returns the access-token principal authenticating
// ctx, or (nil, false) for an interactive session. Surfaces that must refuse a
// machine credential (minting tokens) check this.
func MachineTokenFromContext(ctx context.Context) (*fat.Principal, bool) {
	p, ok := ctx.Value(machineTokenKey{}).(*fat.Principal)
	return p, ok && p != nil
}

// ErrAccessTokenNotAPI reports an access token that is live but does not
// carry reliant:api — a daemon credential, an LLM key or a connector
// credential presented to the API. A credential rejection: the caller must use
// a different token, not retry.
var ErrAccessTokenNotAPI = fmt.Errorf("%w: access token does not grant reliant:api", ErrInvalidToken)

// ClaimsForAPIToken authenticates an `rlat_` presented to reliant's API. It
// must be live, hold reliant:api, and act as a user; the claims it yields are
// that user's, exactly as their session would produce.
//
// An inactive token is ErrInvalidToken (a credential rejection); an
// introspection failure is returned as-is (NOT a rejection — the auth layer
// reports it as Unavailable).
func ClaimsForAPIToken(ctx context.Context, introspector AccessTokenIntrospector, token string) (*JWTClaims, *fat.Principal, error) {
	p, err := introspector.Introspect(ctx, token)
	if err != nil {
		if errors.Is(err, ErrAccessTokenInactive) {
			return nil, nil, fmt.Errorf("%w: access token is not live", ErrInvalidToken)
		}
		return nil, nil, err
	}
	if !p.Scopes.Permits(fat.ScopeReliantAPI) || p.ActingUserID == "" {
		return nil, nil, ErrAccessTokenNotAPI
	}
	return &JWTClaims{Sub: p.ActingUserID, Role: "authenticated"}, p, nil
}

// ErrAccessTokenInactive is the sentinel an introspector wraps for a token
// that is unknown, revoked, expired or malformed. accesstokenclient.ErrInactive
// wraps it, so both stores are recognized here without auth importing them.
var ErrAccessTokenInactive = errors.New("access token is not live")
