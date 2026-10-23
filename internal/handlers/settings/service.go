package settings

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"connectrpc.com/connect"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	reliantv1connect "github.com/reliant-labs/reliant/gen/reliant/v1/reliantv1connect"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/llm/drivers/antigravity"
	"github.com/reliant-labs/reliant/internal/llm/drivers/claude"
	"github.com/reliant-labs/reliant/internal/llm/drivers/codex"
	"github.com/reliant-labs/reliant/internal/llm/drivers/copilot"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/providerusage"
	"github.com/reliant-labs/reliant/pkg/config"
)

// LLMKeyMinter is the slice of the controlplane adapter SyncReliantProvider
// uses: it mints the caller's LLM gateway key for one device, rotating that
// device's previous key.
type LLMKeyMinter interface {
	MintLLMKey(ctx context.Context, jwt, deviceName string) (controlplane.LLMKey, error)
}

// Deps holds dependencies for the settings service. The composition graph
// constructs it (internal/app/compose.go). The rpc methods on *Service are
// where this service's logic lives; see the forge `api` / `services` skill.
type Deps struct {
	Logger *slog.Logger
	Config *config.Config

	// Repo stores settings, provider credentials and privacy choices
	// (deps.go).
	Repo Repository
	// ControlPlane mints the Reliant provider's LLM key (SyncReliantProvider).
	// server.go passes the deployment's controlplane adapter.
	ControlPlane LLMKeyMinter
	// ExchangeClaudeCode trades a Claude OAuth authorization code for tokens.
	// server.go passes claude.ExchangeClaudeAuthorizationCode; a test passes a
	// fake so CompleteClaudeOAuth never reaches Anthropic's token endpoint.
	ExchangeClaudeCode func(code, codeVerifier, redirectURI, state string) (*claude.ClaudeTokens, error)
	// ExchangeCodexCode and ExchangeAntigravityCode do the same for Codex and Antigravity.
	ExchangeCodexCode       func(code, codeVerifier, redirectURI string) (*codex.CodexTokens, error)
	ExchangeAntigravityCode func(code, codeVerifier, redirectURI string) (*antigravity.AntigravityTokens, error)
	// ValidateKey checks a pasted API key against its provider (keyvalidate.Validate).
	ValidateKey func(ctx context.Context, provider models.Family, apiKey string) (ok bool, message string)
	// PollCopilotDevice performs one poll of GitHub's device flow.
	PollCopilotDevice func(ctx context.Context, deviceCode string) (*copilot.GitHubTokens, error)
	// ClaudeProfile reads the account behind a Claude token (plan, email). A server that cannot
	// reach it leaves it nil and a new Claude account's plan stays unknown until the next poll.
	// forge:optional-dep
	ClaudeProfile func(ctx context.Context, accessToken string) (providerusage.ClaudeAccountProfile, error)
	// GitHubIdentity reads the GitHub user behind a Copilot token. Nil leaves a Copilot account
	// unlabelled, and signing in again replaces it instead of adding another.
	// forge:optional-dep
	GitHubIdentity func(ctx context.Context, accessToken string) (GitHubUser, error)
	// ChatWaker sends a capacity wake to the user's chats blocked on a
	// provider once a sign-in adds or re-auths an account, or the Reliant key
	// is synced, so each goes on as a whole (chat.Service.WakeUserChats). Nil
	// leaves them to their own resets.
	// forge:optional-dep
	ChatWaker ChatWaker
}

// validateDeps returns an error if any required Deps field is nil. The
// gate runs once at construction so handlers need no per-RPC nil-checks.
func (d Deps) validateDeps() error {
	if d.Logger == nil {
		return fmt.Errorf("settings: Deps.Logger is required")
	}
	if d.Config == nil {
		return fmt.Errorf("settings: Deps.Config is required")
	}
	if d.Repo == nil {
		return fmt.Errorf("settings: Deps.Repo is required")
	}
	if d.ControlPlane == nil {
		return fmt.Errorf("settings: Deps.ControlPlane is required")
	}
	if d.ExchangeClaudeCode == nil {
		return fmt.Errorf("settings: Deps.ExchangeClaudeCode is required")
	}
	if d.ExchangeCodexCode == nil {
		return fmt.Errorf("settings: Deps.ExchangeCodexCode is required")
	}
	if d.ExchangeAntigravityCode == nil {
		return fmt.Errorf("settings: Deps.ExchangeAntigravityCode is required")
	}
	if d.ValidateKey == nil {
		return fmt.Errorf("settings: Deps.ValidateKey is required")
	}
	if d.PollCopilotDevice == nil {
		return fmt.Errorf("settings: Deps.PollCopilotDevice is required")
	}
	return nil
}

// Service implements the settings Connect RPC service.
type Service struct {
	reliantv1connect.UnimplementedSettingsServiceHandler
	deps Deps
}

// New creates a new settings service instance with the supplied Deps.
// Returns an error when validateDeps rejects a nil required field.
func New(deps Deps) (*Service, error) {
	if err := deps.validateDeps(); err != nil {
		return nil, err
	}
	return &Service{deps: deps}, nil
}

// Name returns the service name for logging and registry.
func (s *Service) Name() string {
	return "settings"
}

// Register registers the service handlers with the HTTP mux.
func (s *Service) Register(mux *http.ServeMux, opts ...connect.HandlerOption) {
	path, handler := reliantv1connect.NewSettingsServiceHandler(s, opts...)
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
var _ = pb.File_reliant_v1_settings_proto
