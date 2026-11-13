// Package accesstokenclient validates and mints Reliant's ONE machine
// credential (`rlat_`, forge/pkg/accesstoken) without owning a table.
//
// forge:outbound-io
//
// It is the outbound adapter to control-plane's AccessTokenInternalService:
// it owns the wire format, the internal-service bearer and the vendor→domain
// mapping, and nothing calls in.
//
// Every machine credential — a daemon's connection credential, a CLI token, a
// connector credential — lives in control-plane's controlplane.access_tokens.
// Reliant's api-server and daemon-gateway reach it through control-plane's
// AccessTokenInternalService, authenticated with an internal-service JWT
// (INTERNAL_SERVICE_SECRET). That service is deliberately NOT in the exported
// public contract, so this package speaks the Connect unary protocol as plain
// HTTP+JSON rather than a generated client: the request/response field names
// in client.go ARE the contract, pinned on control-plane's side by
// internal/handlers/access_token_internal's wire test.
//
// ── REVOCATION WINDOW ─────────────────────────────────────────────────
//
// control-plane revokes immediately at its table. Callers of Introspect here
// choose their own staleness:
//
//   - the daemon-gateway introspects ONCE PER CONNECT with no cache
//     (Introspect) — a connect is rare and a daemon stream is the
//     high-value surface;
//   - the api-server's per-request interceptor uses CachedIntrospector, which
//     caches for at most CacheTTL. A revoked token therefore keeps working at
//     the API for up to CacheTTL. That bound is the documented window;
//     TestCacheTTL_IsBounded pins it so it cannot silently grow.
//
// `forge generate` writes this package's mock (mock_gen.go). A consumer that
// needs a working fake uses tokenauthority.Memory, the conformance-tested
// in-process implementation; adapter tests stand up an httptest server.
//
//forge:not-a-component: built by internal/tokenauthority, internal/grpc and internal/llm/drivers — never a node of the composition graph
package accesstokenclient

import (
	"context"
	"net/http"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"

	"github.com/reliant-labs/reliant/internal/auth"
)

// Service is the adapter's surface: one method per operation reliant needs
// from control-plane's token table. *Client implements it.
type Service interface {
	// Introspect resolves a presented token, uncached.
	Introspect(ctx context.Context, token string) (*fat.Principal, error)
	// IntrospectAccount is Introspect plus the acting user's email, which
	// control-plane reads from its users row ("" from one that predates it).
	IntrospectAccount(ctx context.Context, token string) (auth.AccessTokenAccount, error)
	// MintForUser mints a token acting as req.UserID in their primary org.
	MintForUser(ctx context.Context, req MintRequest) (Minted, error)
	// ListForUser lists a user's live tokens, optionally one scope.
	ListForUser(ctx context.Context, userID string, scope fat.Scope) ([]TokenInfo, error)
	// RevokeForUser revokes one of a user's tokens.
	RevokeForUser(ctx context.Context, userID, tokenID string) error
	// UpdateForUser changes one of a user's tokens' name and/or scopes
	// WITHOUT reissuing the secret. A nil name or nil scopes leaves that half
	// alone.
	UpdateForUser(ctx context.Context, userID, tokenID string, name *string, scopes fat.Set) (TokenInfo, error)
	// RevokeResource revokes every live token bound to resource.
	RevokeResource(ctx context.Context, resource fat.Resource) (int64, error)
	// RevokeEphemeral revokes a user's ephemeral tokens (session end).
	RevokeEphemeral(ctx context.Context, userID string) (int64, error)
	// ResolveOrgMembership asks whether userID is a live member of orgID or,
	// with orgID "", which org is their default. Every negative is
	// Member=false; an error is never a negative. A control plane that
	// predates the rpc answers ErrUnsupported.
	ResolveOrgMembership(ctx context.Context, userID, orgID string) (OrgMembership, error)
	// ResolveGoneOrgs reports which of orgIDs (at most MaxResolveGoneOrgs)
	// are gone, and since when. An org not reported is live or unknown. A
	// control plane that predates the rpc answers ErrUnsupported.
	ResolveGoneOrgs(ctx context.Context, orgIDs []string) ([]GoneOrg, error)
}

// Introspector is what a validator needs. *Client and *CachedIntrospector
// both satisfy it.
type Introspector interface {
	Introspect(ctx context.Context, token string) (*fat.Principal, error)
}

// Signer mints the internal-service bearer for each call.
type Signer func() (string, error)

// Deps are the adapter's collaborators.
type Deps struct {
	// BaseURL is the control-plane origin serving AccessTokenInternalService.
	BaseURL string
	// Sign supplies a fresh internal-service token per call.
	Sign Signer
	// HTTPClient performs the calls. nil uses a client with a 10s timeout.
	HTTPClient *http.Client
}

var (
	_ Service      = (*Client)(nil)
	_ Introspector = (*Client)(nil)
	_ Introspector = (*CachedIntrospector)(nil)
)
