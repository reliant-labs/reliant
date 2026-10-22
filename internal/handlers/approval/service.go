package approval

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/orm"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the approval service. The composition graph
// (internal/app/compose.go) and the e2e story harness construct it. The rpc
// methods on *Service are where this service's logic lives; see the forge
// `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Repo reads and resolves approvals, checks the chats that own them,
	// and writes the denial message a denied step leaves in the chat.
	Repo Repository
	// Signals delivers a resolved approval to the workflow parked on it.
	Signals Signaler
}

// Repository is the slice of db.Repository the approval handlers use.
type Repository interface {
	RunTx(ctx context.Context, policy orm.TxPolicy, f func(ctx context.Context) error) error

	GetChat(ctx context.Context, id string) (*db.Chat, error)
	CreateChatUpdate(ctx context.Context, chatID string, updateType pb.ChatUpdateType, entityID string, data string) error

	GetApproval(ctx context.Context, id string) (*db.Approval, error)
	ListPendingApprovalsByChat(ctx context.Context, chatID string) ([]*db.Approval, error)
	UpdateApprovalStatus(ctx context.Context, id string, status int32, denialReason *string, actionTaken *string, metadata *string) error

	ListMessages(ctx context.Context, chatID string, opts db.MessageListOptions) ([]*db.Message, error)
	ListContentBlocks(ctx context.Context, messageID string) ([]*db.MessageContentBlock, error)
	GetNextOrdinal(ctx context.Context, threadID string) (int64, error)
	GetNextSeq(ctx context.Context, chatID, threadID string) (int64, error)
	CreateMessage(ctx context.Context, msg *db.Message) error
	CreateContentBlock(ctx context.Context, block *db.MessageContentBlock) error
}

// Signaler signals a workflow run, resetting an interrupted one so the
// signal still lands. *workflow.PauseService implements it.
type Signaler interface {
	SignalWithRecovery(ctx context.Context, workflowID, signalName string, signalData interface{}, inputUpdate map[string]interface{}) error
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("approval: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("approval: Deps.Config is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("approval: Deps.Repo is required")
	}
	if d.Signals == nil {
		return fmt.Errorf("approval: Deps.Signals is required")
	}
	return nil
}

// Service implements the approval Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedApprovalServiceHandler
	deps Deps
}

// New creates a new approval service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "approval"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewApprovalServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_approval_proto
