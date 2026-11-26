// Package chat_trace serves reliant.v1.ChatTraceService: one chat's
// Trace/Cost tab (dev-docs/design/agent-analytics.md §2, plan item B2).
//
// It reads two stores and joins neither: the spans Reliant's own servers
// exported, from control-plane's ConversationTraceInternalService over
// tenant zero's span store (seven days), and the chat's model calls from
// llm_call_usage (ninety days). The person is authorized HERE, once, by the
// chat's own rule (authz.ReadChatAccess); control-plane is asked only for the
// chat's own id and org, and checks nothing about people.
package chat_trace

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Store is the slice of the repository this service reads; *db.Repo
// satisfies it.
type Store interface {
	// ListChatLLMCalls is the chat's model calls, oldest first.
	ListChatLLMCalls(ctx context.Context, q db.ChatLLMCallQuery) ([]*db.ChatLLMCall, error)
}

// ChatRows reads a chat's own row; the generated chat store satisfies it.
type ChatRows interface {
	// GetChatRow is the chat, or core.ErrChatNotFound.
	GetChatRow(ctx context.Context, id string) (*db.ChatRow, error)
}

// Spans reads one page of a chat's spans from control-plane's span store;
// controlplane.Service satisfies it.
type Spans interface {
	GetConversationTrace(ctx context.Context, q controlplane.ConversationTraceQuery) (controlplane.ConversationTrace, error)
}

// Deps holds dependencies for the chat_trace service. Composition
// (internal/app/compose.go) supplies Logger/Config and resolves added fields
// by type. The rpc methods on *Service are where this service's logic lives;
// see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config
	// Store holds the chat's model calls.
	Store Store
	// Chats reads the chat the trace is of.
	Chats ChatRows
	// Spans is nil when this deployment has no control plane to ask (self
	// hosted, or no internal-service secret): every trace is then PARTIAL,
	// spans unavailable, with its model calls.
	//
	// forge:optional-dep
	Spans Spans
	// Now reports the current time; time.Now when nil.
	//
	// forge:optional-dep
	Now func() time.Time
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("chat_trace: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("chat_trace: Deps.Config is required")
	}
	if d.Store == nil {
		return fmt.Errorf("chat_trace: Deps.Store is required")
	}
	if d.Chats == nil {
		return fmt.Errorf("chat_trace: Deps.Chats is required")
	}
	return nil
}

// Service implements the chat_trace Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedChatTraceServiceHandler
	deps Deps
}

// New creates a new chat_trace service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "chat_trace"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewChatTraceServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_chat_trace_proto
