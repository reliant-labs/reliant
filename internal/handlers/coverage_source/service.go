// Package coverage_source serves reliant.v1.CoverageSourceService: the
// source of a file a coverage report covers, read from the project's GitHub
// repository at view time (forge docs/proposals/tests-coverage-flakes.md,
// Amendment 1 part A). Nothing here stores source: a blob read is cached in
// memory for a few minutes, per viewer, and that is all.
//
// What is checked where:
//
//   - tests:read on the forge project, and the blob being in a retained
//     coverage report of it, are control-plane's (TestsService.HasCoverageBlob,
//     asked with the viewer's own bearer on every read, before anything is
//     fetched): the same split as the Security pages, whose reads
//     control-plane authorizes against the caller's own credential.
//   - Org membership, and which Reliant projects lend their GitHub remote,
//     are reliant's: only the viewer's own projects in the org
//     (authz.CanReadProject) are searched.
//   - What the viewer may read on GitHub is GitHub's: the blob was read with
//     their own token. No server role reads it any more (a server holds no
//     repository bytes, owner 2026-11-27), so until a daemon-side reader
//     exists every read is NOT_YET_AVAILABLE.
package coverage_source

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the coverage_source service. Composition
// (internal/app/compose.go) supplies Logger/Config and resolves added fields
// by type. The rpc methods on *Service are where this service's logic lives;
// see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config
	// Projects are the Reliant projects that lend a forge project its
	// GitHub remote.
	Projects Projects
	// Orgs resolves the org a read acts in. nil in a deployment with no
	// orgs: the viewer's own projects, in any org.
	//
	// forge:optional-dep
	Orgs OrgResolver
	// Reports says whether a retained coverage report names a blob. nil
	// when there is no control plane to ask: every read is then
	// NOT_YET_AVAILABLE, and nothing is fetched.
	//
	// forge:optional-dep
	Reports Reports
	// Blobs reads a blob as the viewer. nil on every server role, which
	// holds no repository bytes: NOT_YET_AVAILABLE.
	//
	// forge:optional-dep
	Blobs Blobs
	// Now reports the current time (the cache's clock); time.Now when nil.
	//
	// forge:optional-dep
	Now func() time.Time
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("coverage_source: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("coverage_source: Deps.Config is required")
	}
	if d.Projects == nil {
		return fmt.Errorf("coverage_source: Deps.Projects is required")
	}
	return nil
}

// Service implements the coverage_source Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedCoverageSourceServiceHandler
	deps  Deps
	cache *blobCache
}

// New creates a new coverage_source service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return &Service{deps: deps, cache: newBlobCache(cacheTTL, cacheMaxBytes, now)}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "coverage_source"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewCoverageSourceServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_coverage_source_proto
