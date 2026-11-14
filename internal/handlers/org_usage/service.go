// Package org_usage serves reliant.v1.OrgUsageService: an organization's
// admins who hold billing:read read the LLM usage the organization pays for
// (control-plane docs/design/org-provider-accounts.md §5, decision D8).
//
// It reads ONE table, llm_org_usage_daily, the rollup of org-funded calls
// (org_key, org_seat, org_wallet). A member's personal-account call is never
// written there (store.addLLMOrgUsageDaily), and the table's funding CHECK
// refuses one, so no read here can return a personal row: the privacy promise
// UsageService makes ("members' own-key usage is not visible to an
// organization's admins") holds by construction, not by a filter.
package org_usage

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Store is the slice of the repository this service reads; *db.Repo
// satisfies it.
type Store interface {
	// ListLLMOrgUsageDaily is the org's rollup of org-funded calls.
	ListLLMOrgUsageDaily(ctx context.Context, orgID string, from, to time.Time) ([]*db.LLMOrgUsageDay, error)
	// ProviderAccountNames names the accounts a response groups by.
	ProviderAccountNames(ctx context.Context, ids []string) (map[string]db.ProviderAccountName, error)
}

// ListPrices is the list price of each model, from control-plane
// (LLMPriceInternalService: LiteLLM's model catalog). controlplane.Service
// satisfies it.
type ListPrices interface {
	GetModelListPrices(ctx context.Context, refs []controlplane.ModelRef) ([]controlplane.ModelListPrice, time.Time, error)
}

// Deps holds dependencies for the org_usage service. Composition
// (internal/app/compose.go) supplies Logger/Config and resolves added fields
// by type. The rpc methods on *Service are where this service's logic lives;
// see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config
	// Store holds the org rollup.
	Store Store
	// Prices is nil when this deployment has no control plane to ask (self
	// hosted, or no internal-service secret): usage is then shown without
	// cost estimates.
	//
	// forge:optional-dep
	Prices ListPrices
	// Now reports the current time; time.Now when nil.
	//
	// forge:optional-dep
	Now func() time.Time
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("org_usage: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("org_usage: Deps.Config is required")
	}
	if d.Store == nil {
		return fmt.Errorf("org_usage: Deps.Store is required")
	}
	return nil
}

// Service implements the org_usage Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedOrgUsageServiceHandler
	deps Deps
}

// New creates a new org_usage service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "org_usage"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewOrgUsageServiceHandler(s, opts...)
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

func (s *Service) now() time.Time {
	if s.deps.Now != nil {
		return s.deps.Now().UTC()
	}
	return time.Now().UTC()
}

// Ensure the generated protobuf package import remains referenced even before methods exist.
var _ = pb.File_reliant_v1_org_usage_proto
