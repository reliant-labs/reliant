// Package pipeline is the PipelineService: enrolment of a repository in
// Reliant's PR pipeline and merge queue, its mode, and reads of where it
// stands (pipeline redesign §4.1, §4.6; build plan task M2). Every rpc is
// refused while the server's pipeline.enabled switch is off.
package pipeline

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Store is what the service reads and writes: the pipeline store layer and
// the projects and GitHub access grants enrolment is decided from. *db.Repo
// satisfies it.
type Store interface {
	GetProject(ctx context.Context, id string) (*core.Project, error)
	FreshIntegrationAccessByLabel(ctx context.Context, userID, integrationID, label string, freshAfter time.Time) ([]core.IntegrationAccessGrant, error)

	GetPipelineRepo(ctx context.Context, id string) (*core.PipelineRepo, error)
	ListPipelineReposByRepository(ctx context.Context, provider, repository string) ([]*core.PipelineRepo, error)
	UpsertPipelineRepo(ctx context.Context, e core.PipelineRepoEnrolment) (*core.PipelineRepo, error)
	SetPipelineRepoMode(ctx context.Context, id string, version int64, mode core.PipelineRepoMode) (*core.PipelineRepo, error)

	CountPRPipelinesByState(ctx context.Context, repoID string) ([]core.PRPipelineStateCount, error)
	LatestQueueBatches(ctx context.Context, repoID string, limit int) ([]*core.QueueBatch, error)
	LatestMainHealth(ctx context.Context, repoID string) (*core.MainHealthRecord, error)
	ListActiveQueueEntries(ctx context.Context, repoID string) ([]*core.QueueEntryListing, error)
	CurrentQueueBatch(ctx context.Context, repoID string) (*core.QueueBatch, error)
}

// Deps holds dependencies for the pipeline service. Composition
// (internal/app/compose.go) supplies Logger/Config and resolves added fields
// by type. The rpc methods on *Service are where this service's logic lives;
// see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config
	// Pipeline is the server's pipeline switch: every rpc is refused while
	// Enabled is off. nil is off. compose passes only Enabled, so the API
	// server's config reach excludes the worker-only queue caller secret.
	//
	// forge:optional-dep
	Pipeline *configv1.PipelineConfig
	// Store is the pipeline store layer, the projects and the access grants.
	Store Store
	// Now is the clock access freshness is judged by; nil is time.Now.
	//
	// forge:optional-dep
	Now func() time.Time
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("pipeline: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("pipeline: Deps.Config is required")
	}
	if d.Store == nil {
		return fmt.Errorf("pipeline: Deps.Store is required")
	}
	return nil
}

// Service implements the pipeline Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedPipelineServiceHandler
	deps Deps
}

// New creates a new pipeline service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	if deps.Pipeline.GetEnabled() {
		deps.Logger.Info("Reliant's PipelineService is ON (pipeline.enabled)")
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
	return "pipeline"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewPipelineServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_pipeline_proto
