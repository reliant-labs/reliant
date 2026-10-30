package token

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/gitcredentialclient"
	"github.com/reliant-labs/reliant/internal/tokenauthority"
	"github.com/reliant-labs/reliant/pkg/config"
)

// Deps holds dependencies for the token service. The composition graph
// constructs it (internal/app/compose.go). The rpc methods on *Service are
// where this service's logic lives; see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Authority is the deployment's token authority: control-plane when
	// hosted, the local store when self-hosted.
	Authority Authority

	// ControlPlane is the deployment's control plane; its zero value is a
	// self-hosted server.
	ControlPlane TokenControlPlane

	// GitTokens is the git token authority (control-plane). Nil on a
	// self-hosted server, where GetGitToken answers FailedPrecondition.
	// forge:optional-dep
	GitTokens GitTokenSource

	// GitBotTokens mints the GitHub App's installation tokens (control-plane).
	// Nil on a self-hosted server, where GetGitHubBotToken answers
	// FailedPrecondition.
	// forge:optional-dep
	GitBotTokens GitBotTokenSource
}

// Authority is the slice of tokenauthority.Authority the token handlers use.
type Authority interface {
	Introspect(ctx context.Context, token string) (*fat.Principal, error)
	MintForUser(ctx context.Context, req tokenauthority.MintRequest) (tokenauthority.Minted, error)
	ListForUser(ctx context.Context, userID string, scope fat.Scope) ([]tokenauthority.TokenInfo, error)
	RevokeForUser(ctx context.Context, userID, tokenID string) error
	UpdateForUser(ctx context.Context, userID, tokenID string, name *string, scopes fat.Set) (tokenauthority.TokenInfo, error)
}

// GitTokenSource is what GetGitToken needs from the git token authority.
// gitcredentialclient.New's client satisfies it.
type GitTokenSource interface {
	UserAccessToken(ctx context.Context, externalUserID, provider string) (gitcredentialclient.Token, error)
}

// GitBotTokenSource is what GetGitHubBotToken needs from the git token
// authority. gitcredentialclient.New's client satisfies it.
type GitBotTokenSource interface {
	MintInstallationToken(ctx context.Context, req gitcredentialclient.InstallationRequest) (gitcredentialclient.InstallationToken, error)
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("token: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("token: Deps.Config is required")
	}
	if d.Authority == nil {
		return fmt.Errorf("token: Deps.Authority is required")
	}
	return nil
}

// Service is reliant's ONE machine-credential surface — a thin facade over
// the deployment's token authority (control-plane when hosted, the local
// store when self-hosted). It never hashes, validates or stores a token.
type Service struct {
	reliantv1connect.UnimplementedTokenServiceHandler
	deps Deps
}

// New creates a new token service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "token"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewTokenServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_token_proto
