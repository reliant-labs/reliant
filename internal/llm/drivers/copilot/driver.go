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
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
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
	endpoints := cachedEndpoints(githubToken, opts.Model.APIModel)
	dialect := "openai-chat"
	switch copilotWireFor(opts.Model, endpoints) {
	case wireMessages:
		dialect = "anthropic-messages"
		client.impl = newAnthropicDialect(opts, githubToken, headers)
	case wireResponses:
		dialect = "openai-responses"
		opts.Model.PreferredEndpoint = "responses"
		client.impl = newOpenAIDialect(opts, githubToken, headers)
	default:
		opts.Model.PreferredEndpoint = "chat_completions"
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
	return newAnthropicDialectAt(individualBaseURL, opts, githubToken, headers)
}

// newAnthropicDialectAt is newAnthropicDialect against an explicit base URL.
//
// Adaptive-thinking models carry their level in output_config.effort, which the
// shared anthropic client only sets on the Claude Code (OAuth) path. Without it
// Copilot receives thinking:{type:"adaptive"} alone and every level behaves the
// same, so the effort is injected here, as GitHub's own client does
// (vscode-copilot-chat messagesApi.ts: output_config:{effort}).
func newAnthropicDialectAt(baseURL string, opts llm.DriverOptions, githubToken string, headers map[string]string) dialectClient {
	sdkOpts := []anthropicopt.RequestOption{
		anthropicopt.WithBaseURL(baseURL),
		anthropicopt.WithHeader("authorization", "Bearer "+githubToken),
	}
	for k, v := range headers {
		sdkOpts = append(sdkOpts, anthropicopt.WithHeader(k, v))
	}

	if effort := copilotClaudeEffort(opts); effort != "" {
		sdkOpts = append(sdkOpts, anthropicopt.WithJSONSet("output_config", map[string]string{"effort": effort}))
	}

	aopts := opts
	aopts.ApiKey = ""
	return anthropic.NewAnthropicClientWithOptions(aopts, sdkOpts...)
}

// newOpenAIDialect builds an OpenAI Chat Completions client pointed at the
// Copilot host. The OpenAI SDK's WithAPIKey sends `authorization: Bearer <key>`,
// which is exactly Copilot's auth, so the gho_ token goes in as the API key.
// Models Copilot serves only on /responses (gpt-5.3-codex, gpt-5.4-mini,
// gpt-5.6-*, gpt-6-*) keep their catalog preferred_endpoint; everything else
// (gemini, claude-via-openai) is forced to Chat Completions.
func newOpenAIDialect(opts llm.DriverOptions, githubToken string, headers map[string]string) dialectClient {
	oopts := opts
	oopts.ApiKey = githubToken
	oopts.BaseURL = individualBaseURL

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

// ReportAvailability implements registry.AvailabilityReporter from the
// account's GET /models catalog (cached per token). A model the account reports
// policy=disabled is not servable; a model absent from the catalog is treated as
// servable so a stale or renamed catalog never hides a model Reliant maps. If the
// catalog cannot be fetched the report is empty — fail open, so a Copilot outage
// cannot make every Copilot model unresolvable.
func (c *CopilotClient) ReportAvailability(ctx context.Context) (registry.ProviderAvailability, error) {
	token, err := resolveGitHubToken(c.options)
	if err != nil {
		return registry.ProviderAvailability{}, err
	}
	entry, err := accountModels(ctx, token)
	if err != nil {
		return registry.ProviderAvailability{}, err
	}
	report := registry.ProviderAvailability{Models: make(map[string]models.ModelAvailability, len(entry.enabled))}
	for apiModel, enabled := range entry.enabled {
		report.Models[apiModel] = models.ModelAvailability{
			Disabled:      !enabled,
			Reason:        copilotDisabledReason,
			ContextWindow: entry.limits[apiModel],
		}
	}
	return report, nil
}

// copilotDisabledReason is the user-facing explanation for a model the account's
// Copilot policy disables.
const copilotDisabledReason = "not enabled on your Copilot plan — enable it in GitHub Copilot settings"

// GetAvailableModels implements registry.ModelLister for the model picker,
// derived from the same report resolution uses.
func (c *CopilotClient) GetAvailableModels(ctx context.Context) ([]models.ModelInfo, error) {
	report, err := c.ReportAvailability(ctx)
	if err != nil {
		return nil, err
	}
	return registry.ApplyAvailability(models.MustGetRegistry().ModelsForDriver(string(Family)), report), nil
}

// copilotClaudeEffort returns the output_config.effort for an adaptive-thinking
// Claude model, or "" when the model takes no effort (budget-tier models such
// as haiku-4.5 must not send output_config).
func copilotClaudeEffort(opts llm.DriverOptions) string {
	if opts.Model.ThinkingMode != "adaptive" {
		return ""
	}
	switch opts.ReasoningEffort {
	case "low", "medium", "high", "xhigh", "max":
		return opts.ReasoningEffort
	default:
		return "high"
	}
}

type copilotWire int

const (
	wireChat copilotWire = iota
	wireResponses
	wireMessages
)

// copilotWireFor picks the endpoint a model is called on. Copilot's /models
// declares supported_endpoints per model, and a model called on an endpoint it
// does not list 400s ("unsupported_api_for_model"), so that list decides:
// /v1/messages for Claude, else /responses when listed, else /chat/completions.
//
// When /models is unavailable (endpoints == nil) fall back to the vendor/catalog
// rule: claude-* on Messages; the catalog's preferred_endpoint otherwise,
// except gemini-* which speaks Chat Completions only.
func copilotWireFor(m models.Model, endpoints []string) copilotWire {
	has := func(want string) bool {
		for _, e := range endpoints {
			if e == want {
				return true
			}
		}
		return false
	}
	if len(endpoints) > 0 {
		switch {
		case isAnthropicModel(m.APIModel) && has("/v1/messages"):
			return wireMessages
		case has("/responses"):
			return wireResponses
		default:
			return wireChat
		}
	}
	switch {
	case isAnthropicModel(m.APIModel):
		return wireMessages
	case m.PreferredEndpoint == "responses" && !strings.HasPrefix(strings.ToLower(m.APIModel), "gemini-"):
		return wireResponses
	default:
		return wireChat
	}
}
