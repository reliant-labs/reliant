package provider_account

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/llm/accountlimits"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Store is the slice of the repository this service uses; *db.Repo satisfies it.
type Store interface {
	ListProviderAccounts(ctx context.Context, userID string) ([]core.ProviderAccount, error)
	ListAccountLimits(ctx context.Context, userID string) (map[string]core.LimitState, error)
	UpsertProviderAccount(ctx context.Context, u core.AccountUpsert) (core.ProviderAccount, bool, error)
	PatchProviderAccount(ctx context.Context, userID, accountID string, p core.AccountPatch) (core.ProviderAccount, error)
	ReorderProviderAccounts(ctx context.Context, userID, provider string, orderedIDs []string) error
	DeleteProviderAccount(ctx context.Context, userID, accountID string) error
	GetChatRowWithUserCheck(ctx context.Context, id, userID string) (*db.ChatRow, error)
	GetRootThread(ctx context.Context, chatID string) (*db.Thread, error)
	ListThreadAssignments(ctx context.Context, threadID string) ([]core.Assignment, error)
	LatestServingAccount(ctx context.Context, threadID string) (string, error)
	EmitUserRefetch(ctx context.Context, userID string, refetchType db.RefetchType, opts db.RefetchOpts) error
}

// Deps holds dependencies for the provider_account service. The rpc methods on *Service are
// where this service's logic lives; see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config
	// Store holds accounts, their limit state and thread assignments.
	Store Store
	// ValidateKey checks a pasted API key against its provider before it is stored
	// (keyvalidate.Validate in production). ok=false carries a message for the user.
	ValidateKey func(ctx context.Context, provider models.Family, apiKey string) (ok bool, message string)
	// UsageRefresher reads an account's usage on demand
	// (RefreshProviderAccountUsage). A server that cannot reach the account
	// store leaves it nil and the rpc answers Unavailable.
	// forge:optional-dep
	UsageRefresher accountlimits.Refresher
	// ChatWaker sends a capacity wake to the user's chats blocked on a
	// provider once an account is added or un-paused, so each goes on as a
	// whole (chat.Service.WakeUserChats). Nil leaves them to their own resets.
	// forge:optional-dep
	ChatWaker ChatWaker
}

// ChatWaker wakes every chat of a user whose run may have a thread blocked on
// a provider; *chat.Service satisfies it.
type ChatWaker interface {
	WakeUserChats(ctx context.Context, userID, why string) int
}

// wakeChats tells the user's blocked chats there is capacity again.
func (s *Service) wakeChats(ctx context.Context, userID, why string) {
	if s.deps.ChatWaker != nil {
		s.deps.ChatWaker.WakeUserChats(ctx, userID, why)
	}
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("provider_account: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("provider_account: Deps.Config is required")
	}
	if d.Store == nil {
		return fmt.Errorf("provider_account: Deps.Store is required")
	}
	if d.ValidateKey == nil {
		return fmt.Errorf("provider_account: Deps.ValidateKey is required")
	}
	return nil
}

// Service implements the provider_account Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedProviderAccountServiceHandler
	deps Deps
}

// New creates a new provider_account service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "provider_account"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewProviderAccountServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_provider_account_proto
