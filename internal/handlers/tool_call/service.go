package tool_call

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"
	"go.temporal.io/sdk/client"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the tool_call service. internal/grpc/server.go
// constructs it today; forge's composition root takes over in P3c
// (research/forge-migration/PLAN.md). The rpc methods on *Service are where
// this service's logic lives; see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Repo reads the tool call, its message and chat, and records the
	// cancelled / backgrounded transition.
	Repo Repository
	// Temporal signals a spawn's root workflow to stop the spawn. Without it
	// a spawn cannot be cancelled, and CancelToolCall reports Unavailable.
	// forge:optional-dep
	Temporal client.Client
	// Router delivers cancel and background requests to the daemon running
	// the tool. Without it cancel relies on the in-memory signal alone, and
	// ConvertToBackground reports Unavailable.
	// forge:optional-dep
	Router ExecutionSignaler
}

// Repository is the slice of db.Repository the tool_call handlers use. It
// includes db.ToolCallStatusStore (the durable status write) and
// temporal.SpawnWorkflowReconciler (the spawn stopper's reconcile).
type Repository interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	GetMessage(ctx context.Context, id string) (*db.Message, error)
	GetContentBlockByToolCallID(ctx context.Context, toolCallID string) (*db.MessageContentBlock, error)
	GetWorkflow(ctx context.Context, id string) (*db.Workflow, error)
	CompareAndSwapWorkflowStatus(ctx context.Context, id string, newStatus, expectedStatus db.WorkflowStatus) (bool, error)
	GetToolCall(ctx context.Context, id string) (*db.ToolCall, error)
	ListToolCallsByIDs(ctx context.Context, toolCallIDs []string) ([]*db.ToolCall, error)
	UpsertToolCall(ctx context.Context, call *db.ToolCall) error
	EmitToolCallCancelledUpdate(ctx context.Context, chatID string, update db.ToolCallUpdate) error
	EmitToolCallBackgroundedUpdate(ctx context.Context, chatID string, update db.ToolCallUpdate) error
}

// ExecutionSignaler reaches the daemon running one tool execution, addressed
// by tool call id. toolexec.DaemonRouter implements it.
type ExecutionSignaler interface {
	SendToolExecutionCancel(ctx context.Context, userID, requestID, reason string) error
	SendToolExecutionBackground(ctx context.Context, userID, requestID, toolCallID string) error
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("tool_call: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("tool_call: Deps.Config is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("tool_call: Deps.Repo is required")
	}
	return nil
}

// Service implements the tool_call Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedToolCallServiceHandler
	deps Deps
}

// New creates a new tool_call service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "tool_call"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewToolCallServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_tool_call_proto
