package question

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the question service. The composition graph
// constructs it (internal/app/compose.go). The rpc methods on *Service are
// where this service's logic lives; see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Repo reads and resolves questions and the chats that own them.
	Repo Repository
	// Signals delivers a resolved question to the workflow parked on it.
	Signals Signaler
	// Messages delivers the answer to an async question to the thread that
	// asked it, as a message from the user. It is the chat service: taking
	// its send paths rather than rebuilding them is what makes the answer
	// behave exactly like a message the user typed to that thread.
	Messages Messenger
}

// Repository is the slice of db.Repository the question handlers use.
type Repository interface {
	GetChat(ctx context.Context, id string) (*db.Chat, error)
	GetQuestionByID(ctx context.Context, id string) (*db.Question, error)
	ListAnswerablePendingQuestionsByChat(ctx context.Context, chatID string) ([]*db.Question, error)
	ListAsyncQuestionsByChat(ctx context.Context, chatID string) ([]*db.Question, error)
	PendingQuestionsByThread(ctx context.Context, chatID string) (map[string]*db.Question, error)
	EnqueueAgentMessageOnce(ctx context.Context, msg *db.AgentMessage) (bool, error)
	EmitUserRefetch(ctx context.Context, userID string, refetchType db.RefetchType, opts db.RefetchOpts) error
	ResolveQuestion(ctx context.Context, id string, responseData *string) error
	EmitQuestionUpdate(ctx context.Context, chatID string, update db.QuestionUpdate) error
	SaveMessageToThread(ctx context.Context, chatID, thread string, role int32, content string, workflowID *string, attachmentIDs []string, displayStyle *int32) (*db.Message, error)
}

// Signaler signals a workflow run, resetting an interrupted one so the
// signal still lands. *workflow.PauseService implements it.
type Signaler interface {
	SignalWithRecovery(ctx context.Context, workflowID, signalName string, signalData interface{}, inputUpdate map[string]interface{}) error
}

// Messenger sends a user's message to a chat's thread: SendMessage for the
// main thread, SendAgentMessage for a sub-agent's. *chat.Service implements
// it.
type Messenger interface {
	SendMessage(ctx context.Context, req *connect.Request[pb.SendMessageRequest]) (*connect.Response[pb.SendMessageResponse], error)
	SendAgentMessage(ctx context.Context, req *connect.Request[pb.SendAgentMessageRequest]) (*connect.Response[pb.SendAgentMessageResponse], error)
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("question: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("question: Deps.Config is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("question: Deps.Repo is required")
	}
	if d.Signals == nil {
		return fmt.Errorf("question: Deps.Signals is required")
	}
	if d.Messages == nil {
		return fmt.Errorf("question: Deps.Messages is required")
	}
	return nil
}

// Service implements the question Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedQuestionServiceHandler
	deps Deps
}

// New creates a new question service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "question"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewQuestionServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_question_proto
