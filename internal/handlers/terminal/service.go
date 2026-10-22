package terminal

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/streamabort"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the terminal service. internal/grpc/server.go
// constructs it today; forge's composition root takes over in P3c
// (research/forge-migration/PLAN.md). The rpc methods on *Service are where
// this service's logic lives; see the forge `api` / `services` skill.
//
// The interactive browser terminal is also served by TerminalWSHandler, a
// websocket endpoint server.go mounts beside this service.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Router reaches the caller's daemon, which owns the terminal sessions.
	Router SessionRouter
}

// SessionRouter is what the terminal handlers ask of the caller's daemon.
// toolexec.DaemonRouter implements it.
type SessionRouter interface {
	SendDaemonCommand(ctx context.Context, userID, commandType string, payload []byte, timeoutMs int32) ([]byte, error)
	SendTerminalInput(ctx context.Context, userID, sessionID string, data []byte) error
	SendTerminalResize(ctx context.Context, userID, sessionID string, cols, rows uint32) error
	SubscribeTerminalOutput(ctx context.Context, userID, sessionID string) (<-chan *toolexec.TerminalOutputEvent, func(), error)
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("terminal: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("terminal: Deps.Config is required")
	}
	if d.Router == nil {
		return fmt.Errorf("terminal: Deps.Router is required")
	}
	return nil
}

// Service implements the terminal Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedTerminalServiceHandler
	deps Deps
}

// New creates a new terminal service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "terminal"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewTerminalServiceHandler(s, opts...)
	// StreamTerminal aborts its request body to stop its input pump
	// (streamabort).
	mux.Handle(path, streamabort.Handler(handler))
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
var _ = pb.File_reliant_v1_terminal_proto
