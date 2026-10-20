package account

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the account service. internal/grpc/server.go
// constructs it today; forge's composition root takes over in P3c
// (research/forge-migration/PLAN.md). The rpc methods on *Service are where
// this service's logic lives; see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// SQL is the raw database handle the purge runs on. Not a Repository:
	// the purge is an ordered multi-statement transaction across 23 tables,
	// which is not expressible through the per-entity Repository surface and
	// should not be smeared across it.
	SQL *sql.DB

	// ControlPlane deletes the caller's platform account (billing identity,
	// daemons, PII) before the local purge, and quotes their wallet for the
	// preview. Nil when this deployment HAS no control plane
	// (RELIANT_CONTROL_PLANE_URL unset): self-hosted reliant has no platform
	// account to delete, and deletes only local data.
	// forge:optional-dep
	ControlPlane ControlPlane
}

// ControlPlane is the slice of controlplane.Service account deletion uses.
// Each call presents the caller's own bearer, so the control plane acts on
// exactly the account that asked.
type ControlPlane interface {
	PreviewAccountDeletionWallet(ctx context.Context, jwt string) (*controlplane.AccountDeletionWalletQuote, error)
	DeleteCurrentUserAccount(ctx context.Context, jwt string) (*controlplane.AccountDeletionResult, error)
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("account: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("account: Deps.Config is required")
	}
	if d.SQL == nil {
		return fmt.Errorf("account: Deps.SQL is required")
	}
	return nil
}

// Service lets a user delete everything reliant stores about them.
//
// It is a thin Connect surface over internal/accountpurge, which owns the
// ordered delete sequence and the transaction. This layer does auth, the
// confirm-email check, and proto translation — nothing else.
type Service struct {
	reliantv1connect.UnimplementedAccountServiceHandler
	deps Deps
}

// New creates a new account service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "account"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewAccountServiceHandler(s, opts...)
	mux.Handle(path, handler)
}

// RegisterHTTP registers plain HTTP handlers (callbacks, REST endpoints)
// outside Connect RPC. The stack provides recovery/logging/audit; auth is
// per-route. Override to add endpoints — forge.yaml webhooks are wired
// automatically (do not call RegisterWebhookRoutes here). See the forge
// `api` skill.
func (s *Service) RegisterHTTP(_ *http.ServeMux, _ func(http.Handler) http.Handler) {
	// No HTTP endpoints by default. Example:
	//   mux.Handle("POST /api/callbacks/oauth", stack(http.HandlerFunc(s.HandleOAuthCallback)))
}

// Ensure the generated protobuf package import remains referenced even before methods exist.
var _ = pb.File_reliant_v1_account_proto
