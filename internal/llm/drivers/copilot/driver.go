// Copyright (c) 2025 Reliant Labs
package copilot

import (
	"context"
	"strings"

	anthropicopt "github.com/anthropics/anthropic-sdk-go/option"
	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/anthropic"
	"github.com/reliant-labs/reliant/internal/llm/drivers/openai"
	"github.com/reliant-labs/reliant/internal/llm/models"
	toolsPkg "github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/models/message"
)

const (
	// individualBaseURL is the single GitHub Copilot host. Auth is the raw GitHub
	// OAuth token as a Bearer credential — no tier, no token exchange, no
	// copilot-session-token (those were free-tier / enterprise artifacts and are
	// gone). The Anthropic SDK appends /v1/messages; the OpenAI SDK appends
	// /chat/completions.
	individualBaseURL = "https://api.individual.githubcopilot.com"

	// Editor-identity fidelity headers, taken from .dev/copilot/gpt5.curl (the
	// GitHub Copilot CLI). GitHub gates these endpoints on a recognizable editor
	// identity, so both dialects send the same set.
	copilotIntegrationID = "copilot-developer-cli"
	copilotEditorVersion = "copilot/1.0.69"
	copilotUserAgent     = "copilot/1.0.69 (client/github/cli darwin v24.16.0) term/Apple_Terminal"
	copilotAPIVersion    = "2026-07-01"
	copilotOpenAIIntent  = "conversation-agent"
)

// dialectClient is the per-model implementation the dispatcher delegates to.
// Both Reliant's Anthropic and OpenAI drivers satisfy it (it is a subset of
// registry.Client), so the Copilot driver reuses their full serialization,
// streaming, tool, and thinking logic — pointed at the Copilot host.
type dialectClient interface {
	SendMessages(ctx context.Context, prompts []string, messages []message.Message, tools []toolsPkg.Tool) (*llm.DriverResponse, error)
	StreamResponse(ctx context.Context, prompts []string, messages []message.Message, tools []toolsPkg.Tool) <-chan llm.DriverEvent
	ValidateKey(ctx context.Context) error
}

// CopilotClient is the GitHub Copilot driver. It is a thin dispatcher that
// routes each model to the API dialect its vendor speaks against the Copilot
// host:
//
//   - claude-* -> POST /v1/messages   (Anthropic Messages, via the anthropic driver)
//   - gpt-* / gemini-* / other -> POST /chat/completions (OpenAI Chat, via the openai driver)
//
// Routing is by model-vendor prefix rather than a hardcoded per-model list, so
// new Copilot models of a known vendor route correctly without code changes.
type CopilotClient struct {
	options llm.DriverOptions
	impl    dialectClient
}

// Name returns the name of the driver.
func (c *CopilotClient) Name() string { return "copilot" }

// NewClient constructs a Copilot client for the model carried in opts. Auth is
// the raw GitHub OAuth token (from the device flow) sent as a Bearer credential.
func NewClient(opts llm.DriverOptions) (*CopilotClient, error) {
	if opts.ReasoningEffort == "" {
		opts.ReasoningEffort = "medium"
	}

	// The stored credential is a GitHub OAuth token (device flow or GitHub CLI).
	// The resolver passes it via ApiKey/BearerToken.
	githubToken, err := resolveGitHubToken(opts)
	if err != nil {
		return nil, err
	}

	headers := copilotHeaders(opts)

	client := &CopilotClient{options: opts}
	dialect := "openai-chat"
	if isAnthropicModel(opts.Model.APIModel) {
		dialect = "anthropic-messages"
		client.impl = newAnthropicDialect(opts, githubToken, headers)
	} else {
		client.impl = newOpenAIDialect(opts, githubToken, headers)
	}

	logging.Debug("Copilot client created", "model", opts.Model.APIModel, "dialect", dialect)
	return client, nil
}

// isAnthropicModel reports whether the Copilot api_model is an Anthropic model
// (served on /v1/messages). Everything else (gpt-*, gemini-*, …) speaks OpenAI
// Chat Completions.
func isAnthropicModel(apiModel string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(apiModel)), "claude")
}

// copilotHeaders builds the editor-identity headers both dialects send. The
// per-request correlation ids are minted once per client (per conversation),
// which the endpoint accepts.
func copilotHeaders(opts llm.DriverOptions) map[string]string {
	return map[string]string{
		"copilot-integration-id": copilotIntegrationID,
		"editor-version":         copilotEditorVersion,
		"user-agent":             copilotUserAgent,
		"x-github-api-version":   copilotAPIVersion,
		"openai-intent":          copilotOpenAIIntent,
		"x-initiator":            "user",
		"x-interaction-type":     "conversation-user",
		"x-client-machine-id":    deviceID(opts),
		"x-client-session-id":    sessionID(opts),
		"x-interaction-id":       uuid.New().String(),
		"x-agent-task-id":        uuid.New().String(),
	}
}

// newAnthropicDialect builds an Anthropic Messages client pointed at the Copilot
// host. Copilot /v1/messages requires `authorization: Bearer <gho_>` (x-api-key
// alone 400s), so we clear ApiKey (to avoid a stray x-api-key) and set the
// Authorization header explicitly.
func newAnthropicDialect(opts llm.DriverOptions, githubToken string, headers map[string]string) dialectClient {
	sdkOpts := []anthropicopt.RequestOption{
		anthropicopt.WithBaseURL(individualBaseURL),
		anthropicopt.WithHeader("authorization", "Bearer "+githubToken),
	}
	for k, v := range headers {
		sdkOpts = append(sdkOpts, anthropicopt.WithHeader(k, v))
	}

	aopts := opts
	aopts.ApiKey = ""
	return anthropic.NewAnthropicClientWithOptions(aopts, sdkOpts...)
}

// newOpenAIDialect builds an OpenAI Chat Completions client pointed at the
// Copilot host. The OpenAI SDK's WithAPIKey sends `authorization: Bearer <key>`,
// which is exactly Copilot's auth, so the gho_ token goes in as the API key.
// PreferredEndpoint is forced to Chat Completions for broad model support.
func newOpenAIDialect(opts llm.DriverOptions, githubToken string, headers map[string]string) dialectClient {
	oopts := opts
	oopts.ApiKey = githubToken
	oopts.BaseURL = individualBaseURL
	oopts.Model.PreferredEndpoint = "chat_completions"

	// Merge the Copilot headers into a private copy of ExtraHeaders so we never
	// mutate the caller's map.
	merged := make(map[string]string, len(opts.ExtraHeaders)+len(headers))
	for k, v := range opts.ExtraHeaders {
		merged[k] = v
	}
	for k, v := range headers {
		merged[k] = v
	}
	oopts.ExtraHeaders = merged

	return openai.NewClient(oopts)
}

// SendMessages delegates to the per-model dialect implementation.
func (c *CopilotClient) SendMessages(ctx context.Context, prompts []string, messages []message.Message, tools []toolsPkg.Tool) (*llm.DriverResponse, error) {
	return c.impl.SendMessages(ctx, prompts, messages, tools)
}

// StreamResponse delegates to the per-model dialect implementation.
func (c *CopilotClient) StreamResponse(ctx context.Context, prompts []string, messages []message.Message, tools []toolsPkg.Tool) <-chan llm.DriverEvent {
	return c.impl.StreamResponse(ctx, prompts, messages, tools)
}

// Model returns the model configuration for this driver.
func (c *CopilotClient) Model() models.Model { return c.options.Model }

// ValidateKey delegates to the per-model dialect implementation.
func (c *CopilotClient) ValidateKey(ctx context.Context) error {
	return c.impl.ValidateKey(ctx)
}

// GetAvailableModels implements registry.ModelLister: it reports the Copilot
// models this account may use for the model picker. GitHub Copilot is a DYNAMIC
// provider — a model Reliant maps may be disabled by the account's policy (it
// 400s upstream) — so we start from the registry's curated Copilot list and set
// each model's Enabled flag from the account's GET /models catalog (cached per
// token). Unknown models (absent from the catalog) are treated as enabled so a
// stale/renamed catalog never hides a model Reliant maps.
//
// Tags/tag-resolution are intentionally out of scope here: this is the picker
// view, not workflow tag resolution (which stays registry-driven).
func (c *CopilotClient) GetAvailableModels(ctx context.Context) ([]models.ModelInfo, error) {
	token, err := resolveGitHubToken(c.options)
	if err != nil {
		return nil, err
	}
	enabled, err := EnabledModels(ctx, token)
	if err != nil {
		return nil, err
	}

	infos := models.MustGetRegistry().ModelsForDriver(string(Family))
	for i := range infos {
		state, known := enabled[infos[i].APIModel]
		infos[i].Enabled = !known || state
	}
	return infos, nil
}
