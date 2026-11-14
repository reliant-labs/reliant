package run

import (
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the run service. The composition graph
// constructs it (internal/app/compose.go), and the worker root builds its own
// for the run-management tools (internal/app/providers_worker.go). The rpc
// methods on *Service are where this service's logic lives; see the forge
// `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Repo reads runs, the chats that own them and the run lists.
	Repo Repository
	// Chats is the chat service every run rpc delegates to. Taking the chat
	// service rather than reconstructing its dependencies is the point: a
	// second construction path would be a second set of behaviors to keep
	// in sync, and the guarantee this service needs is that it cannot
	// diverge.
	Chats Chat
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("run: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("run: Deps.Config is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("run: Deps.Repo is required")
	}
	if d.Chats == nil {
		return fmt.Errorf("run: Deps.Chats is required")
	}
	return nil
}

// Service implements the run Connect RPC service: the engine's execution
// API.
//
// It is DELIBERATELY A FAÇADE for now. Every RPC resolves the run to the chat
// that owns it and delegates to the chat service, which holds the real
// implementation. That is what makes this additive: the engine surface starts
// existing without the coding product changing behavior, because there is only
// one implementation and both services call it.
//
// The delegation direction reverses later, once a run can exist without a
// session (see research/ENGINE_SPLIT_PLAN.md — `workflows.chat_id` is NOT NULL
// today, so a chatless run is not yet representable). At that point the logic
// moves here and the chat service becomes the façade. Writing it this way round
// means no behavior is at risk in the meantime.
type Service struct {
	reliantv1connect.UnimplementedRunServiceHandler
	deps Deps
}

// New creates a new run service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "run"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewRunServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_run_proto
