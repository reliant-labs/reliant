package usage

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
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Usage reads the caller's LLM usage. store.LlmCallUsageExtStore satisfies it;
// both reads take the owner and fail closed on an empty one.
type Usage interface {
	ListLLMUsageDaily(ctx context.Context, userID string, from, to time.Time) ([]*store.LlmCallUsageDaily, error)
	// ListLLMUsageDetail is the per-call breakdown (funding and account
	// included) of the days whose individual calls are still kept.
	ListLLMUsageDetail(ctx context.Context, userID string, from, to time.Time) ([]*store.LLMUsageDetail, error)
}

// AccountNames labels an account breakdown. *db.Repo satisfies it: provider
// accounts are a sealed table that handlers never reach through the store, so
// this stays on the facade until internal/credentials takes it over.
type AccountNames interface {
	ProviderAccountNames(ctx context.Context, ids []string) (map[string]db.ProviderAccountName, error)
}

// Deps holds dependencies for the usage service. Composition
// (internal/app/compose.go) supplies Logger/Config and resolves added fields
// by type. The rpc methods on *Service are where this service's logic lives;
// see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config
	// Usage holds the daily LLM usage rollup.
	Usage Usage
	// Accounts names the accounts a response groups by.
	Accounts AccountNames
	// Now reports the current time; time.Now when nil.
	// forge:optional-dep
	Now func() time.Time
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("usage: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("usage: Deps.Config is required")
	}
	if d.Usage == nil {
		return fmt.Errorf("usage: Deps.Usage is required")
	}
	if d.Accounts == nil {
		return fmt.Errorf("usage: Deps.Accounts is required")
	}
	return nil
}

// Service implements the usage Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedUsageServiceHandler
	deps Deps
}

// New creates a new usage service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "usage"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewUsageServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_usage_proto
