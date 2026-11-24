// Package insights serves reliant.v1.InsightsService: Agents › Insights
// (dev-docs/design/agent-analytics.md §3.6, PR C1).
//
// It reads the insights rollups (internal/store/insights_rollup.go), and the
// privacy rule is in what each rpc can reach, not only in an if:
//
//   - GetTeamInsights reaches only TeamStore: the team tables, which have no
//     user column, and the contributor counts the small-group floor needs,
//     which are opaque ordinals. It cannot print a person.
//   - GetMemberInsights and GetMyInsights read one member's rows through
//     MemberStore. A drill-down into another member is for an org owner or
//     admin (role read fresh, a person's credential), and is recorded before
//     anything is read.
package insights

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

// TeamStore is what the team view reads: an org's totals, which name no one,
// and per group the opaque ordinals of its contributors. *db.Repo satisfies it.
type TeamStore interface {
	ListInsightsTeamModelCalls(ctx context.Context, orgID string, from, to time.Time, dim db.InsightsDim) ([]db.InsightsModelRow, error)
	ListInsightsTeamToolCalls(ctx context.Context, orgID string, from, to time.Time, dim db.InsightsDim) ([]db.InsightsToolRow, error)
	ListInsightsContributors(ctx context.Context, src db.InsightsSource, orgID string, from, to time.Time, dim db.InsightsDim) ([]db.InsightsContributor, error)
}

// MemberStore is what a member's own view and an admin's drill-down read and
// record. *db.Repo satisfies it.
type MemberStore interface {
	ListInsightsMemberModelCalls(ctx context.Context, orgID, userID string, from, to time.Time, dim db.InsightsDim) ([]db.InsightsModelRow, error)
	ListInsightsMemberToolCalls(ctx context.Context, orgID, userID string, from, to time.Time, dim db.InsightsDim) ([]db.InsightsToolRow, error)
	RecordInsightsDrilldown(ctx context.Context, orgID, actorUserID, subjectUserID string, from, to, at time.Time) (string, error)
	ListInsightsDrilldowns(ctx context.Context, orgID, subjectUserID string, after *db.InsightsDrilldownCursor, limit int) ([]*db.InsightsDrilldown, error)
}

// Deps holds dependencies for the insights service. Composition
// (internal/app/compose.go) supplies Logger/Config and resolves added fields
// by type. The rpc methods on *Service are where this service's logic lives;
// see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config
	// Team is the team view's only store.
	Team TeamStore
	// Members holds one member's rows and the drill-down audit.
	Members MemberStore
	// Now reports the current time; time.Now when nil.
	//
	// forge:optional-dep
	Now func() time.Time
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("insights: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("insights: Deps.Config is required")
	}
	if d.Team == nil {
		return fmt.Errorf("insights: Deps.Team is required")
	}
	if d.Members == nil {
		return fmt.Errorf("insights: Deps.Members is required")
	}
	return nil
}

// Service implements the insights Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedInsightsServiceHandler
	deps Deps
}

// New creates a new insights service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "insights"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewInsightsServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_insights_proto
