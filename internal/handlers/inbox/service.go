package inbox

import (
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"connectrpc.com/connect"
	"golang.org/x/sync/singleflight"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the inbox service. The composition graph
// constructs it (internal/app/compose.go). The rpc methods on *Service are
// where this service's logic lives; see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Repo reads what waits on the caller (pending rows, automation
	// health, machines and their storage) and records dismissals.
	Repo Repository
	// Sweeper runs the confirmed storage clean-up on a machine's daemon.
	// nil when no daemon router is wired; CleanupStorage then reports
	// unavailable.
	// forge:optional-dep
	Sweeper Sweeper
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("inbox: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("inbox: Deps.Config is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("inbox: Deps.Repo is required")
	}
	return nil
}

// Service implements the inbox Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedInboxServiceHandler
	deps Deps
	// storageCache holds each user's storage view for storageViewTTL.
	storageCache sync.Map
	// inboxReads shares one user's concurrent ListInbox reads (sharedRead).
	inboxReads singleflight.Group
}

// New creates a new inbox service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "inbox"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewInboxServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_inbox_proto
