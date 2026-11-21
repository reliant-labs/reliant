// Package pull_request_record is the PullRequestRecordService: reads of PR
// records (internal/prrecords, dev-docs/pr-records.md), gated on the caller's
// own access to the repository.
package pull_request_record

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the pull_request_record service. Composition
// (internal/app/compose.go) supplies Logger/Config and resolves added fields
// by type. The rpc methods on *Service are where this service's logic lives;
// see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config
	// Store reads the records and the caller's access grants.
	Store Store
	// Now is the clock access freshness is judged by; nil is time.Now.
	//
	// forge:optional-dep
	Now func() time.Time
}

// Store is the slice of the repository this service reads; *db.Repo
// satisfies it.
type Store interface {
	GetPRRecord(ctx context.Context, provider, repository string, number int, repositoryIDs []string) (*core.PRRecord, error)
	ListPRRecordRepositories(ctx context.Context, provider, repository string) ([]core.PRRecordRepository, error)
	ListPRRecords(ctx context.Context, f core.PRRecordFilter) ([]*core.PRRecord, error)
	ListPRRecordHeads(ctx context.Context, recordID string) ([]core.PRRecordHead, error)
	ListPRRecordReviews(ctx context.Context, recordID string, commits []string) ([]core.PRRecordReview, error)
	ListPRRecordChecks(ctx context.Context, provider, repositoryID string, shas []string) ([]core.PRRecordCheck, error)
	HasFreshIntegrationAccess(ctx context.Context, userID, integrationID, account, resource string, freshAfter time.Time) (bool, error)
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("pull_request_record: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("pull_request_record: Deps.Config is required")
	}
	if d.Store == nil {
		return fmt.Errorf("pull_request_record: Deps.Store is required")
	}
	return nil
}

// Service implements the pull_request_record Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedPullRequestRecordServiceHandler
	deps Deps
}

// New creates a new pull_request_record service instance with the supplied Deps.
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
	return "pull_request_record"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewPullRequestRecordServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_pull_request_record_proto
