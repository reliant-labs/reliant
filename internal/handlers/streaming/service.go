package streaming

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
	"github.com/reliant-labs/reliant/internal/streaming"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the streaming service. The composition graph
// constructs it (internal/app/compose.go). The rpc methods on *Service are
// where this service's logic lives; see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Repo reads the user and chat update logs and everything a chat
	// snapshot is assembled from. Repository (deps.go) is what the handlers
	// call plus the roles of threads.Service and messageproto, which the
	// snapshot reads messages and tool calls through.
	Repo Repository
	// Deltas carries a chat's ephemeral streaming deltas (LLM output as it
	// is generated) to a subscriber of that chat. The API server always has
	// one; without it a chat subscription still gets its snapshot and its
	// persisted updates, only no deltas.
	// forge:optional-dep
	Deltas DeltaSubscriber
	// UserUpdates pushes user-level events as they are persisted. Without
	// it the stream reads them from the user update log on each tick.
	// forge:optional-dep
	UserUpdates UserUpdateSubscriber
	// ChatUpdates pushes chat-level events as they are persisted. Without
	// it the stream reads them from the chat update log on each tick.
	// forge:optional-dep
	ChatUpdates ChatUpdateSubscriber
}

// DeltaSubscriber subscribes to one chat's streaming deltas.
// streaming.StreamingHub implements it.
type DeltaSubscriber interface {
	Subscribe(ctx context.Context, chatID string) streaming.Subscription
}

// UserUpdateSubscriber subscribes to one user's persisted user updates.
// streaming.UpdateHub[db.UserUpdate] implements it.
type UserUpdateSubscriber interface {
	Subscribe(ctx context.Context, userID string) streaming.UpdateSubscription[db.UserUpdate]
}

// ChatUpdateSubscriber subscribes to one chat's persisted chat updates.
// streaming.UpdateHub[db.ChatUpdate] implements it.
type ChatUpdateSubscriber interface {
	Subscribe(ctx context.Context, chatID string) streaming.UpdateSubscription[db.ChatUpdate]
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("streaming: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("streaming: Deps.Config is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("streaming: Deps.Repo is required")
	}
	return nil
}

// Service implements the streaming Connect RPC service. Authentication is
// handled by the API server's interceptor chain, as for every service.
type Service struct {
	reliantv1connect.UnimplementedStreamingServiceHandler
	deps Deps

	// heartbeatInterval is how often an idle user stream sends a heartbeat.
	// Always defaultHeartbeatInterval in production; a field only so a test
	// can observe a real heartbeat frame without waiting 30 seconds for it.
	heartbeatInterval time.Duration
}

// New creates a new streaming service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps, heartbeatInterval: defaultHeartbeatInterval}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "streaming"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewStreamingServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_streaming_proto
