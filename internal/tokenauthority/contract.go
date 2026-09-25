// Package tokenauthority is reliant's view of the ONE machine-credential
// system (`rlat_`, forge/pkg/accesstoken). Reliant's handlers, interceptors
// and gateway never mint, hash or store a token themselves; they ask the
// deployment's Authority.
//
// ONE SYSTEM, TWO STORES, EXACTLY ONE CHOSEN:
//
//   - HOSTED — a control-plane is configured (RELIANT_CONTROL_PLANE_URL and
//     INTERNAL_SERVICE_SECRET). The Authority is control-plane's
//     AccessTokenInternalService (the accesstokenclient adapter);
//     controlplane.access_tokens is the one table.
//   - SELF-HOSTED — no control-plane. The Authority is LocalStore over
//     reliant's own access_tokens table, built on the same forge primitive.
//
// The choice is made once, at construction (New), from Deps. There is NO
// runtime fallback: a configured control-plane that is unreachable fails
// every token operation loudly. Falling back to the local store would split
// one user's tokens across two tables and silently authenticate against the
// wrong one — the two-families failure this system exists to end.
//
// Authority is a strategy contract with three implementations — the
// control-plane adapter, LocalStore, and Memory (the in-process store the
// rest of reliant's tests use in its place; TestAuthorityConformance keeps it
// honest against LocalStore). Consumers depend on Authority, never on a store.
//
// Reliant's retired credential families (per-daemon/API PATs in daemon_pats,
// hashed connector credentials in connector_grants) are gone. The only token
// shape anything here accepts is forge/pkg/accesstoken's.
package tokenauthority

import (
	"context"
	"database/sql"

	fat "github.com/reliant-labs/forge/pkg/accesstoken"

	"github.com/reliant-labs/reliant/internal/accesstokenclient"
)

// Shared request/response shapes, identical for every store.
type (
	MintRequest = accesstokenclient.MintRequest
	Minted      = accesstokenclient.Minted
	TokenInfo   = accesstokenclient.TokenInfo
)

// Authority mints, lists, revokes and validates access tokens. user ids are
// reliant's user id (the IdP subject) in both modes.
//
// forge:contract
type Authority interface {
	Introspect(ctx context.Context, token string) (*fat.Principal, error)
	MintForUser(ctx context.Context, req MintRequest) (Minted, error)
	ListForUser(ctx context.Context, userID string, scope fat.Scope) ([]TokenInfo, error)
	RevokeForUser(ctx context.Context, userID, tokenID string) error
	RevokeResource(ctx context.Context, resource fat.Resource) (int64, error)
	RevokeEphemeral(ctx context.Context, userID string) (int64, error)
}

// Deps selects and builds the deployment's Authority.
type Deps struct {
	// ControlPlaneURL, when set, selects the hosted Authority.
	ControlPlaneURL string
	// InternalServiceSecret signs the internal-service bearer. Required with
	// ControlPlaneURL: a URL without it is a misconfiguration, never a reason
	// to fall back to the local store.
	InternalServiceSecret string
	// DB is reliant's database, for the self-hosted store.
	DB *sql.DB
}

// Mode names which store an Authority is.
type Mode string

const (
	ModeControlPlane Mode = "control-plane"
	ModeLocal        Mode = "local"
)

var (
	_ Authority = (accesstokenclient.Service)(nil)
	_ Authority = (*accesstokenclient.Client)(nil)
	_ Authority = (*LocalStore)(nil)
	_ Authority = (*Memory)(nil)
)
