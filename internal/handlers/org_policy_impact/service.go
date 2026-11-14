// Package org_policy_impact is the OrgPolicyImpactService: what a proposed org
// policy would do to the org's work in reliant, for an owner or admin to see
// before saving it (control-plane docs/design/org-policies.md §6). It judges
// and counts; it enforces nothing (internal/orgpolicy and its enforcement
// points do).
package org_policy_impact

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Store is the slice of the repository this service reads; *db.Repo
// satisfies it.
type Store interface {
	// LatestOrgChatCalls is each of the org's live chats' latest chat-turn
	// call at or after since.
	LatestOrgChatCalls(ctx context.Context, orgID string, since time.Time) ([]db.OrgChatCall, error)
}

// Deps holds dependencies for the org_policy_impact service. Composition
// (internal/app/compose.go) supplies Logger/Config and resolves added fields
// by type. The rpc methods on *Service are where this service's logic lives;
// see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config
	// Store reads the org's chats' latest model calls.
	Store Store
	// Now is the clock the window is read from; nil is time.Now.
	//
	// forge:optional-dep
	Now func() time.Time
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("org_policy_impact: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("org_policy_impact: Deps.Config is required")
	}
	if d.Store == nil {
		return fmt.Errorf("org_policy_impact: Deps.Store is required")
	}
	return nil
}

// Service implements the org_policy_impact Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedOrgPolicyImpactServiceHandler
	deps Deps
}

// New creates a new org_policy_impact service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// now is the service's clock: Deps.Now, else the wall clock.
func (s *Service) now() time.Time {
	if s.deps.Now != nil {
		return s.deps.Now()
	}
	return time.Now()
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "org_policy_impact"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewOrgPolicyImpactServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_org_policy_impact_proto
