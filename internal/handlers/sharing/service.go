package sharing

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/orm"

	configv1 "github.com/reliant-labs/reliant/gen/reliant/config/v1"
	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Chats reads the chat a visibility decision is made on.
// store.ChatsWithActivityExtStore satisfies it.
type Chats interface {
	GetChat(ctx context.Context, id string) (*core.Chat, error)
}

// Projects reads the project a visibility decision is made on.
// store.ProjectExtStore satisfies it.
type Projects interface {
	GetProject(ctx context.Context, id string) (*core.Project, error)
}

// WorkflowDrafts reads the saved workflow a sharing decision is made on.
// workflowdrafts.Service satisfies it.
type WorkflowDrafts interface {
	GetWorkflowDraft(ctx context.Context, id string) (*core.WorkflowDraft, error)
}

// Shares is the one writer of a visibility (with its share event) and the
// shared lists. store.ShareEventExtStore satisfies it; its writes must run in
// a transaction (Deps.Tx), so the visibility and its event land together.
type Shares interface {
	ApplyShareChange(ctx context.Context, c core.ShareChange) (bool, error)
	ListSharedChats(ctx context.Context, f core.SharedChatFilters) ([]*core.Chat, error)
	ListSharedProjects(ctx context.Context, f core.SharedProjectFilters) ([]*core.Project, error)
	ListSharedWorkflowDrafts(ctx context.Context, f core.SharedWorkflowDraftFilters) ([]*core.WorkflowDraft, error)
}

// Members is the one writer of a chat's per-person grants (with its share
// event) and their reader (chat-collaboration.md §3).
// store.ChatMemberExtStore satisfies it; ApplyChatMemberChange must run in a
// transaction (Deps.Tx), so the grant and its event land together.
type Members interface {
	LockChatMembers(ctx context.Context, chatID string) error
	ApplyChatMemberChange(ctx context.Context, c core.ChatMemberChange) (core.ShareAction, error)
	GetChatMember(ctx context.Context, chatID, userID string) (*core.ChatMember, error)
	ListChatMembers(ctx context.Context, chatID string) ([]*core.ChatMember, error)
}

// TxRunner opens the transaction a visibility change runs in. *orm.Client
// satisfies it.
type TxRunner interface {
	RunTx(ctx context.Context, policy orm.TxPolicy, fn func(ctx context.Context) error) error
}

// OrgResolver names the org a shared list is of: the one the request names,
// verified a live membership of the caller, else the caller's default.
// *orgs.Resolver is one.
type OrgResolver interface {
	Named(ctx context.Context, userID, requested string) (string, error)
	DefaultOrg(ctx context.Context, userID string) (string, error)
}

// Deps holds dependencies for the sharing service. Composition
// (internal/app/compose.go) supplies Logger/Config and resolves added fields
// by type.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config
	// Chats, Projects and WorkflowDrafts read the rows a decision is made on.
	Chats          Chats
	Projects       Projects
	WorkflowDrafts WorkflowDrafts
	// Shares writes a visibility and lists what is shared.
	Shares Shares
	// Members writes and reads a chat's per-person grants.
	Members Members
	// Tx opens the transaction a visibility or member change runs in.
	Tx TxRunner
	// Orgs resolves the org a shared list is of. Without one (a deployment
	// with no control plane) there are no orgs, so nothing is shared and the
	// lists are empty.
	//
	// forge:optional-dep
	Orgs OrgResolver
	// Collaboration gates granting (chat_collaboration.enabled): only with it
	// set may SetChatMember grant anyone. nil is off. Removing and listing
	// members are never gated.
	//
	// forge:optional-dep
	Collaboration *configv1.ChatCollaborationConfig
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("sharing: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("sharing: Deps.Config is required")
	}
	if d.Chats == nil {
		return fmt.Errorf("sharing: Deps.Chats is required")
	}
	if d.Projects == nil {
		return fmt.Errorf("sharing: Deps.Projects is required")
	}
	if d.WorkflowDrafts == nil {
		return fmt.Errorf("sharing: Deps.WorkflowDrafts is required")
	}
	if d.Shares == nil {
		return fmt.Errorf("sharing: Deps.Shares is required")
	}
	if d.Members == nil {
		return fmt.Errorf("sharing: Deps.Members is required")
	}
	if d.Tx == nil {
		return fmt.Errorf("sharing: Deps.Tx is required")
	}
	return nil
}

// Service implements the sharing Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedSharingServiceHandler
	deps Deps
}

// New creates a new sharing service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "sharing"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewSharingServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_sharing_proto
