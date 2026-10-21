package catalog

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/connections"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/integrations/catalogindex"
	"github.com/reliant-labs/reliant/internal/integrations/inputoptions"
	"github.com/reliant-labs/reliant/internal/llm/drivers/local"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the catalog service. internal/grpc/server.go
// constructs it today; forge's composition root takes over in P3c
// (research/forge-migration/PLAN.md). The rpc methods on *Service are where
// this service's logic lives; see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Tools builds the tools whose descriptions ListTools and ListNodes
	// report; server.go passes the API server's tools factory. Without it
	// they are read from a bare factory.
	// forge:optional-dep
	Tools ToolBuilder
	// TagPrefs supplies the caller's model.tag_config.<tag> preferences, so
	// the tiers ListModels reports match what a tag selector actually runs
	// on. Without it the tiers are the registry defaults.
	// forge:optional-dep
	TagPrefs TagPrefsReader
	// LocalModels supplies the caller's daemons' published local model
	// inventories. Without it local models are left out of the catalog.
	// forge:optional-dep
	LocalModels LocalModelDirectory
	// Search backs SearchCatalog, GetCatalogEntry and
	// ListCatalogIntegrations. Without it the three rpcs answer
	// Unimplemented, which only a composition with no index (a test) does.
	// forge:optional-dep
	Search Searcher
	// InputOptions backs ListInputOptions: it lists the values of an
	// integration-defined input type as the caller. Without it the rpc answers
	// Unimplemented, which only a composition with no option source (a test)
	// does.
	// forge:optional-dep
	InputOptions InputOptions
	// IntegrationMethods reports, for one integration, each declared auth
	// method with whether this deployment offers it (IntegrationMethodsFrom).
	// Without it GetCatalogEntry reports every method as available with no
	// detail.
	// forge:optional-dep
	IntegrationMethods func(integrationID string) (connections.Integration, bool)
}

// ToolBuilder builds a registered tool. *tools.ToolsFactory implements it.
type ToolBuilder interface {
	ToolFor(def tools.ToolDefinition) tools.Tool
}

// TagPrefsReader reads a user's settings by key pattern; it is
// modelprefs.SettingsReader, declared here where it is consumed.
type TagPrefsReader interface {
	ListSettingsByKey(ctx context.Context, userID string, keyPattern string) ([]*db.Setting, error)
}

// LocalModelDirectory lists a user's daemons' local model inventories; it is
// local.Directory, declared here where it is consumed. A directory that also
// implements local.EndpointLister contributes the user's configured
// endpoints.
type LocalModelDirectory interface {
	LocalDaemons(ctx context.Context, userID string) ([]local.DaemonInventory, error)
}

// Searcher is what the integration catalog rpcs need: the caller-aware
// index. catalogindex.Service implements it.
type Searcher interface {
	Search(ctx context.Context, userID string, q catalogindex.Query) (*catalogindex.Result, error)
	Get(ctx context.Context, userID, ref string) (*catalogindex.Entry, bool, error)
	ListIntegrations(ctx context.Context, userID string, q catalogindex.IntegrationQuery) (*catalogindex.IntegrationResult, error)
}

// InputOptions is what ListInputOptions needs: the option service, declared
// here where it is consumed. inputoptions.Service implements it.
type InputOptions interface {
	List(ctx context.Context, req inputoptions.ListRequest) (*inputoptions.ListResult, error)
	Resolve(ctx context.Context, req inputoptions.ResolveRequest) (*inputoptions.Resolution, error)
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("catalog: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("catalog: Deps.Config is required")
	}
	return nil
}

// Service implements the catalog Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedCatalogServiceHandler
	deps Deps

	// GetCELCompletions' response is static: built once, on first use.
	celCompletionsOnce sync.Once
	celCompletionsResp *pb.GetCELCompletionsResponse
}

// New creates a new catalog service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "catalog"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewCatalogServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_catalog_proto
