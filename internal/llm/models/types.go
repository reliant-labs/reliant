// Copyright (c) 2025 Reliant Labs
package models

import (
	"encoding/json"
	"strings"

	"gopkg.in/yaml.v3"
)

// ModelSelector specifies how to select a model for an LLM call.
// It supports two selection modes: direct ID selection for precise control,
// or tag-based selection for flexible, preference-aware resolution.
//
// Exactly one of ID or Tags must be set. If both are set, ID takes precedence.
// If neither is set, the selector is invalid.
//
// Example YAML (direct ID):
//
//	model:
//	  id: claude-sonnet-4-20250514
//
// Example YAML (tag-based):
//
//	model:
//	  tags: [flagship, reasoning]
//
// Example YAML (tag with provider constraint):
//
//	model:
//	  tags: [fast]
//	  provider: anthropic
type ModelSelector struct {
	// ID specifies an exact model identifier for direct selection.
	// When set, the system bypasses tag resolution and uses this model directly.
	// The ID must match a model in the registry (built-in or custom).
	//
	// Common model IDs:
	//   - claude-sonnet-4-20250514 (Claude 4 Sonnet)
	//   - claude-opus-4-20250514 (Claude 4 Opus)
	//   - gpt-4.1 (GPT-4.1)
	//   - o3 (OpenAI o3)
	//
	// YAML key: id
	ID string `yaml:"id,omitempty" json:"id,omitempty"`

	// Tags specifies tag-based model resolution using weighted best-match scoring.
	// Earlier tags in the list have higher priority. Models are scored by how many
	// tags they match, with earlier tags weighted more heavily.
	//
	// Scoring: For tags [t1, t2, t3], weights are [4, 2, 1] (powers of 2).
	// This ensures earlier tags always outweigh combinations of later tags.
	//
	// This enables graceful degradation: [local, fast] will prefer models with
	// both tags, but will fall back to models with just "local" or just "fast"
	// if no perfect match exists.
	//
	// Built-in tags:
	//   - flagship: Best overall quality for complex tasks
	//   - moderate: Balanced quality and cost
	//   - fast: Optimized for quick responses
	//   - cheap: Lowest cost per token
	//   - reasoning: Extended thinking capability
	//   - local: Runs on local hardware
	//   - meta: Internal operations (titling, compaction)
	//
	// Tag resolution order can be customized via TagPreferences in user config.
	//
	// Examples:
	//   - [flagship]: Best overall model
	//   - [local, fast]: Prefer local+fast, fall back to local-only or fast-only
	//   - [cheap, flagship]: Prefer cheap, fall back to flagship if no cheap available
	//
	// YAML key: tags
	Tags []string `yaml:"tags,omitempty" json:"tags,omitempty"`

	// Providers specifies an ordered list of preferred providers for model selection.
	// The resolver tries each provider in order and uses the first available one.
	//
	// Semantics:
	//   - Empty/nil: System selects best provider by priority (default behavior)
	//   - Single element: Hard constraint - fails if provider unavailable
	//   - Multiple elements: Ordered fallback - tries each until one works
	//
	// When combined with Tags, only models available through these providers
	// are considered. When combined with ID, forces using these providers
	// even if the model is available through other providers.
	//
	// Valid providers:
	//   - anthropic: Anthropic's direct API
	//   - openai: OpenAI's direct API
	//   - openrouter: OpenRouter aggregator
	//   - local: Local provider (Ollama, LM Studio, etc.)
	//
	// YAML key: providers
	Providers []string `yaml:"providers,omitempty" json:"providers,omitempty"`
}

// UnmarshalJSON handles both string and object formats for ModelSelector.
// String format: "model-id" -> ModelSelector{ID: "model-id"}
// Object format: {"id": "model-id"} or {"tags": ["fast"]} -> normal struct
func (m *ModelSelector) UnmarshalJSON(data []byte) error {
	// Try string format first
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		str = strings.TrimSpace(str)
		m.ID = str
		m.Tags = nil
		m.Providers = nil
		return nil
	}

	// Try object format using an alias to avoid infinite recursion
	type alias ModelSelector
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*m = ModelSelector(a)
	return nil
}

// MarshalJSON serializes ModelSelector.
// If only ID is set (no tags/providers), outputs as string for cleaner output.
// Otherwise outputs as object.
func (m ModelSelector) MarshalJSON() ([]byte, error) {
	if len(m.Tags) == 0 && len(m.Providers) == 0 && m.ID != "" {
		return json.Marshal(m.ID)
	}
	type alias ModelSelector
	return json.Marshal(alias(m))
}

// UnmarshalYAML handles both string and object formats for ModelSelector.
// String format: model-id -> ModelSelector{ID: "model-id"}
// Object format: id: model-id or tags: [fast] -> normal struct
func (m *ModelSelector) UnmarshalYAML(node *yaml.Node) error {
	// Try string format first (scalar node)
	if node.Kind == yaml.ScalarNode {
		m.ID = strings.TrimSpace(node.Value)
		m.Tags = nil
		m.Providers = nil
		return nil
	}

	// Try object format using an alias to avoid infinite recursion
	type alias ModelSelector
	var a alias
	if err := node.Decode(&a); err != nil {
		return err
	}
	*m = ModelSelector(a)
	return nil
}

// ModelDefinition represents a model with its capabilities and provider mappings.
// This is the canonical model definition used in both the embedded model registry
// and user-defined custom models.
//
// When defining custom models, only ID and Providers are required. All other
// fields have sensible defaults.
//
// Example YAML (minimal):
//
//   - id: local-llama
//     providers:
//   - driver: local
//     api_model: llama3.2:latest
//
// Example YAML (full):
//
//   - id: my-custom-model
//     name: My Custom Model
//     tags: [fast, local]
//     visibility: user
//     capabilities:
//     can_reason: false
//     supports_tools: true
//     supports_attachments: false
//     supports_streaming: true
//     supports_caching: false
//     max_context_window: 32000
//     max_output_tokens: 4096
//     cost:
//     input_per_1m: 0.0
//     output_per_1m: 0.0
//     providers:
//   - driver: local
//     api_model: custom-model:latest
type ModelDefinition struct {
	// ID is the unique identifier for this model.
	// This is used in model selectors, configuration, and logging.
	// Must not conflict with built-in model IDs when defining custom models.
	//
	// Convention: Use lowercase with hyphens, typically including version info.
	//
	// Examples: "claude-sonnet-4-20250514", "gpt-4.1", "local-llama3"
	//
	// YAML key: id
	// Required: Yes
	ID string `yaml:"id" json:"id" mapstructure:"id"`

	// Name is the human-readable display name for the model.
	// Shown in UIs and logs for easier identification.
	// Defaults to ID if not specified.
	//
	// Examples: "Claude 4 Sonnet", "GPT-4.1", "Llama 3.2 8B"
	//
	// YAML key: name
	// Required: No (defaults to ID)
	Name string `yaml:"name" json:"name" mapstructure:"name"`

	// Tags are labels used for tag-based model selection.
	// When a ModelSelector specifies tags, the resolver uses weighted best-match
	// scoring to find models that match as many requested tags as possible.
	//
	// Built-in tags:
	//   - flagship: Best overall quality for complex tasks
	//   - moderate: Balanced quality and cost
	//   - fast: Optimized for quick responses
	//   - cheap: Lowest cost per token
	//   - reasoning: Extended thinking capability
	//   - local: Runs on local hardware
	//   - meta: Internal operations (titling, compaction)
	//
	// You can define custom tags for your own organizational purposes.
	//
	// Example: ["fast", "local"]
	//
	// YAML key: tags
	// Required: No (defaults to empty)
	Tags []string `yaml:"tags" json:"tags" mapstructure:"tags"`

	// Visibility controls where this model appears in the system.
	// Use this to hide internal or experimental models from user selection.
	//
	// Valid values:
	//   - "user": Shown in user-facing model selection (default)
	//   - "meta": Used for internal operations only (titling, summarization)
	//   - "dev": Only available in development builds
	//
	// YAML key: visibility
	// Required: No (defaults to "user")
	Visibility ModelVisibility `yaml:"visibility" json:"visibility" mapstructure:"visibility"`

	// Capabilities describes what features this model supports.
	// Used by the system to determine which models can handle specific tasks
	// and to set appropriate request parameters.
	//
	// For custom models, you typically only need to set max_context_window
	// and supports_tools. Other capabilities default to reasonable values.
	//
	// YAML key: capabilities
	// Required: No (has sensible defaults)
	Capabilities ModelCapabilities `yaml:"capabilities" json:"capabilities" mapstructure:"capabilities"`

	// Cost defines pricing information for cost tracking and estimation.
	// Prices are in USD per 1 million tokens.
	// Set to 0 for local models or when cost tracking is not needed.
	//
	// YAML key: cost
	// Required: No (defaults to 0)
	Cost ModelCost `yaml:"cost" json:"cost" mapstructure:"cost"`

	// Providers lists the API providers through which this model is available.
	// At least one provider is required. When multiple providers are listed,
	// the system selects based on API key availability and provider priority.
	//
	// The first provider in the list is preferred when multiple are available.
	//
	// YAML key: providers
	// Required: Yes (at least one)
	Providers []ProviderMapping `yaml:"providers" json:"providers" mapstructure:"providers"`

	// DriverSettings contains driver-specific configuration that varies by
	// model type. Most models don't need these settings.
	//
	// Use this for models that require non-standard API parameters,
	// such as OpenAI's reasoning models or models with special temperature handling.
	//
	// YAML key: driver_settings
	// Required: No
	DriverSettings *DriverSettings `yaml:"driver_settings,omitempty" json:"driver_settings,omitempty" mapstructure:"driver_settings"`

	// Model-owned defaults — used when preset/workflow doesn't specify these.
	// These provide sensible per-model defaults that can be overridden at
	// the preset or workflow level.

	// DefaultThinkingLevel is the default thinking/reasoning effort level.
	// Valid values: "low", "medium", "high", "xhigh".
	// Empty means the system picks a default.
	//
	// YAML key: default_thinking_level
	// Required: No
	DefaultThinkingLevel string `yaml:"default_thinking_level,omitempty" json:"default_thinking_level,omitempty" mapstructure:"default_thinking_level"`

	// DefaultTemperature is the default sampling temperature for the model.
	// Pointer type so we can distinguish "not set" from zero.
	//
	// YAML key: default_temperature
	// Required: No
	DefaultTemperature *float64 `yaml:"default_temperature,omitempty" json:"default_temperature,omitempty" mapstructure:"default_temperature"`

	// DefaultCompactionThreshold is the token count at which context compaction triggers.
	// Typically set to 90-95% of max_context_window. Pointer type so we can
	// distinguish "not set" from zero.
	//
	// YAML key: default_compaction_threshold
	// Required: No
	DefaultCompactionThreshold *int `yaml:"default_compaction_threshold,omitempty" json:"default_compaction_threshold,omitempty" mapstructure:"default_compaction_threshold"`
}

// ToModel converts a ModelDefinition to the legacy Model struct.
func (def ModelDefinition) ToModel() Model {
	model := Model{
		ID:                  ModelID(def.ID),
		Name:                def.Name,
		ContextWindow:       int64(def.Capabilities.MaxContextWindow),
		DefaultMaxTokens:    int64(def.Capabilities.MaxOutputTokens),
		CostPer1MIn:         def.Cost.InputPer1M,
		CostPer1MOut:        def.Cost.OutputPer1M,
		CostPer1MInCached:   def.Cost.CachedInputPer1M,
		CanReason:           def.Capabilities.CanReason,
		SupportsAttachments: def.Capabilities.SupportsAttachments,
	}

	// Set APIModel from the first provider
	if len(def.Providers) > 0 {
		model.APIModel = def.Providers[0].APIModel
	}

	// Apply driver settings if present
	if def.DriverSettings != nil {
		model.PreferredEndpoint = def.DriverSettings.PreferredEndpoint
		if def.DriverSettings.TemperatureMode == "omit" {
			model.TemperatureMode = TemperatureModeOmit
		} else {
			model.TemperatureMode = TemperatureModeAny
		}
		model.UseMaxCompletionTokens = def.DriverSettings.UseMaxCompletionTokens
		if def.DriverSettings.ReasoningSummaryMode != "" {
			model.ReasoningSummaryMode = ReasoningSummaryMode(def.DriverSettings.ReasoningSummaryMode)
		}
	}

	// Set visibility-based flags
	model.DevOnly = def.Visibility == VisibilityDev

	return model
}

// ModelCapabilities describes what a model can do and its token limits.
// These capabilities inform the system about which features can be used
// with each model and how to configure requests appropriately.
//
// For custom models, the most important fields to set are:
//   - max_context_window: Determines how much context can be sent
//   - supports_tools: Whether function calling is available
//   - supports_streaming: Whether streaming responses work
//
// Example YAML:
//
//	capabilities:
//	  can_reason: false
//	  supports_tools: true
//	  supports_attachments: false
//	  supports_streaming: true
//	  supports_caching: false
//	  max_context_window: 128000
//	  max_output_tokens: 8192
type ModelCapabilities struct {
	// CanReason indicates whether the model supports extended thinking/reasoning.
	// When true, the model can produce detailed reasoning traces and may use
	// additional tokens for chain-of-thought processing.
	//
	// Models with reasoning capability are tagged with "reasoning" and are
	// preferred for complex problem-solving tasks.
	//
	// YAML key: can_reason
	// Default: false
	CanReason bool `yaml:"can_reason" json:"can_reason" mapstructure:"can_reason"`

	// SupportsTools indicates whether the model supports function/tool calling.
	// When true, the model can invoke tools like file reading, code execution,
	// and web fetching. Essential for most Reliant workflows.
	//
	// Most modern models support tools. Set to false only for models that
	// lack function calling capability (e.g., some older or specialized models).
	//
	// YAML key: supports_tools
	// Default: false
	SupportsTools bool `yaml:"supports_tools" json:"supports_tools" mapstructure:"supports_tools"`

	// SupportsAttachments indicates whether the model can process file attachments.
	// When true, files like images, PDFs, and documents can be included in
	// conversations for the model to analyze.
	//
	// YAML key: supports_attachments
	// Default: false
	SupportsAttachments bool `yaml:"supports_attachments" json:"supports_attachments" mapstructure:"supports_attachments"`

	// SupportsStreaming indicates whether the model supports streaming responses.
	// When true, partial responses are sent as they're generated, providing
	// faster time-to-first-token and better user experience.
	//
	// Most cloud API models support streaming. Some local models may not.
	//
	// YAML key: supports_streaming
	// Default: false
	SupportsStreaming bool `yaml:"supports_streaming" json:"supports_streaming" mapstructure:"supports_streaming"`

	// SupportsCaching indicates whether the model supports prompt caching.
	// When true, repeated portions of prompts can be cached to reduce
	// latency and cost on subsequent requests.
	//
	// Currently primarily supported by Anthropic models.
	//
	// YAML key: supports_caching
	// Default: false
	SupportsCaching bool `yaml:"supports_caching" json:"supports_caching" mapstructure:"supports_caching"`

	// MaxContextWindow is the maximum number of tokens the model can process
	// in a single request (input + output combined).
	//
	// This determines how much conversation history and context can be
	// included in requests. Larger values allow more context but may
	// increase latency and cost.
	//
	// Common values:
	//   - 8000-32000: Smaller/faster models
	//   - 128000-200000: Standard large models
	//   - 1000000+: Extended context models
	//
	// YAML key: max_context_window
	// Required: Recommended for custom models
	MaxContextWindow int `yaml:"max_context_window" json:"max_context_window" mapstructure:"max_context_window"`

	// MaxOutputTokens is the maximum number of tokens the model can generate
	// in a single response.
	//
	// This limits response length. For code generation and detailed analysis,
	// higher values (8192+) are preferred. For quick responses, lower values
	// may be appropriate.
	//
	// Common values:
	//   - 4096: Standard output limit
	//   - 8192: Extended output for detailed responses
	//   - 16384+: Long-form generation
	//
	// YAML key: max_output_tokens
	// Default: 4096 (if not specified)
	MaxOutputTokens int `yaml:"max_output_tokens" json:"max_output_tokens" mapstructure:"max_output_tokens"`

	// ThinkingLevels lists the supported thinking/reasoning effort levels for this model.
	// When empty and CanReason is true, defaults to ["low", "medium", "high"].
	// Common values: "low", "medium", "high", "xhigh"
	ThinkingLevels []string `yaml:"thinking_levels,omitempty" json:"thinking_levels,omitempty" mapstructure:"thinking_levels"`

	// SupportedFileTypes lists the file types this model can process as attachments.
	// Common values: "image", "pdf", "text", "audio", "video"
	// When empty and SupportsAttachments is true, assumed to support common types.
	SupportedFileTypes []string `yaml:"supported_file_types,omitempty" json:"supported_file_types,omitempty" mapstructure:"supported_file_types"`
}

// ModelCost represents pricing information in USD per 1 million tokens.
// Used for cost tracking, estimation, and budget management.
//
// For local models or when cost tracking is not needed, leave all values at 0.
//
// Example YAML:
//
//	cost:
//	  input_per_1m: 3.00
//	  output_per_1m: 15.00
//	  cached_input_per_1m: 0.30
type ModelCost struct {
	// InputPer1M is the cost in USD per 1 million input tokens.
	// Input tokens are the tokens sent to the model (prompts, context, etc.).
	//
	// Example values:
	//   - 0.25: Budget models (Haiku, GPT-4.1 Mini)
	//   - 3.00: Standard models (Sonnet, GPT-4.1)
	//   - 15.00: Premium models (Opus)
	//   - 0.00: Local models
	//
	// YAML key: input_per_1m
	// Default: 0.0
	InputPer1M float64 `yaml:"input_per_1m" json:"input_per_1m" mapstructure:"input_per_1m"`

	// OutputPer1M is the cost in USD per 1 million output tokens.
	// Output tokens are the tokens generated by the model in responses.
	// Typically 3-5x higher than input costs.
	//
	// Example values:
	//   - 1.25: Budget models
	//   - 15.00: Standard models
	//   - 75.00: Premium models
	//   - 0.00: Local models
	//
	// YAML key: output_per_1m
	// Default: 0.0
	OutputPer1M float64 `yaml:"output_per_1m" json:"output_per_1m" mapstructure:"output_per_1m"`

	// CachedInputPer1M is the cost in USD per 1 million cached input tokens.
	// When prompt caching is available (see SupportsCaching), repeated prompt
	// portions can be cached at a reduced rate.
	//
	// Typically 10% of the regular input cost. Only relevant for models
	// that support caching (currently Anthropic models).
	//
	// YAML key: cached_input_per_1m
	// Default: 0.0 (no discount)
	CachedInputPer1M float64 `yaml:"cached_input_per_1m,omitempty" json:"cached_input_per_1m,omitempty" mapstructure:"cached_input_per_1m"`
}

// ProviderMapping maps a model to a specific provider's API format.
// Each model can be available through multiple providers, and each provider
// may use a different model identifier in its API.
//
// Example YAML:
//
//	providers:
//	  - driver: anthropic
//	    api_model: claude-sonnet-4-20250514
//	  - driver: openrouter
//	    api_model: anthropic/claude-sonnet-4
type ProviderMapping struct {
	// Driver specifies which provider driver handles requests for this model.
	// The driver determines the API format, authentication, and endpoint used.
	//
	// Valid drivers:
	//   - "anthropic": Anthropic's Messages API (api.anthropic.com)
	//   - "openai": OpenAI's Chat Completions API (api.openai.com)
	//   - "openrouter": OpenRouter aggregator (openrouter.ai)
	//   - "local": Local OpenAI-compatible server (Ollama, LM Studio, etc.)
	//
	// Each driver requires appropriate API key configuration:
	//   - anthropic: ANTHROPIC_API_KEY
	//   - openai: OPENAI_API_KEY
	//   - openrouter: OPENROUTER_API_KEY
	//   - local: No API key (uses LocalProviderConfig.BaseURL)
	//
	// YAML key: driver
	// Required: Yes
	Driver string `yaml:"driver" json:"driver" mapstructure:"driver"`

	// APIModel is the model identifier string sent to the provider's API.
	// This may differ from the Reliant model ID, as providers use their own
	// naming conventions.
	//
	// Examples by driver:
	//   - anthropic: "claude-sonnet-4-20250514"
	//   - openai: "gpt-4.1", "o3"
	//   - openrouter: "anthropic/claude-sonnet-4", "openai/gpt-4.1"
	//   - local: "llama3.2:latest", "qwen2.5:32b"
	//
	// For OpenRouter, use the full model path (provider/model-name).
	// For local models, use the name as shown in 'ollama list' or similar.
	//
	// YAML key: api_model
	// Required: Yes
	APIModel string `yaml:"api_model" json:"api_model" mapstructure:"api_model"`
}

// DriverSettings contains driver-specific configuration for a model.
// These settings handle API quirks and model-specific behaviors that vary
// between providers and model types.
//
// Most custom models don't need driver settings. Use these only when a model
// requires non-standard API parameters.
//
// Example YAML (for an OpenAI reasoning model):
//
//	driver_settings:
//	  preferred_endpoint: responses
//	  temperature_mode: omit
//	  use_max_completion_tokens: true
//	  reasoning_summary_mode: auto
type DriverSettings struct {
	// PreferredEndpoint specifies which OpenAI API endpoint to use.
	// Only relevant for the OpenAI driver.
	//
	// Valid values:
	//   - "chat_completions": Standard /v1/chat/completions endpoint (default)
	//   - "responses": Newer /v1/responses endpoint (for reasoning models)
	//
	// The responses endpoint supports additional features like reasoning
	// summaries but may not be available for all models.
	//
	// YAML key: preferred_endpoint
	// Default: "chat_completions"
	PreferredEndpoint string `yaml:"preferred_endpoint,omitempty" json:"preferred_endpoint,omitempty"`

	// TemperatureMode controls whether temperature is sent in API requests.
	// Some models (particularly reasoning models) don't accept temperature
	// and will error if it's included.
	//
	// Valid values:
	//   - "any": Send temperature parameter normally (default)
	//   - "omit": Never send temperature, even if specified
	//
	// YAML key: temperature_mode
	// Default: "any"
	TemperatureMode string `yaml:"temperature_mode,omitempty" json:"temperature_mode,omitempty"`

	// UseMaxCompletionTokens controls which parameter name is used for output
	// length limits with the OpenAI driver.
	//
	// When true, uses "max_completion_tokens" instead of "max_tokens".
	// Required for newer OpenAI models that use the updated parameter name.
	//
	// YAML key: use_max_completion_tokens
	// Default: false
	UseMaxCompletionTokens bool `yaml:"use_max_completion_tokens,omitempty" json:"use_max_completion_tokens,omitempty"`

	// ReasoningSummaryMode controls reasoning summary behavior for models
	// that support extended thinking with summaries.
	//
	// Valid values:
	//   - "auto": Let the API decide when to include summaries (default)
	//   - "always": Always request reasoning summaries
	//   - "none": Never request reasoning summaries
	//
	// Reasoning summaries provide condensed explanations of the model's
	// thought process without exposing full reasoning traces.
	//
	// YAML key: reasoning_summary_mode
	// Default: "" (uses API default behavior)
	ReasoningSummaryMode string `yaml:"reasoning_summary_mode,omitempty" json:"reasoning_summary_mode,omitempty"`
}

// ModelVisibility controls where a model appears.
type ModelVisibility string

const (
	// VisibilityUser means the model is shown to users in the UI
	VisibilityUser ModelVisibility = "user"

	// VisibilityMeta means the model is used for internal meta operations
	// (title generation, compaction, etc.) but not shown in main UI
	VisibilityMeta ModelVisibility = "meta"

	// VisibilityDev means the model is only available in development builds
	VisibilityDev ModelVisibility = "dev"
)

// ResolvedModel is the result of resolving a ModelSelector.
// It contains the full model definition and the specific provider to use.
type ResolvedModel struct {
	Definition ModelDefinition
	Provider   ProviderMapping
}

// ModelTag constants for common tags.
// These are the system-defined tags that presets should use.
const (
	TagFlagship  = "flagship"  // Best overall for complex tasks
	TagModerate  = "moderate"  // Good balance of quality/cost
	TagFast      = "fast"      // Quick responses
	TagCheap     = "cheap"     // Lowest cost
	TagReasoning = "reasoning" // Extended thinking capability
	TagLocal     = "local"     // Runs locally
	TagMeta      = "meta"      // For internal operations
)

// ModelsConfig is the root structure for the embedded models.yaml file.
type ModelsConfig struct {
	Models []ModelDefinition `yaml:"models" json:"models"`
}

// UserModelsConfig is the structure for user-defined model configuration.
// It allows users to customize model selection behavior, add custom models,
// and configure provider settings.
//
// This configuration is typically stored in .reliant/config.yaml under the
// "models" key, or in a dedicated .reliant/models.yaml file.
//
// Example YAML:
//
//	models:
//	  tag_preferences:
//	    flagship: [claude-opus-4-20250514, gpt-4.1]
//	    fast: [claude-haiku-3-5, gpt-4.1-mini]
//	  custom:
//	    - id: local-codellama
//	      name: CodeLlama 34B
//	      tags: [local, fast]
//	      providers:
//	        - driver: local
//	          api_model: codellama:34b
//	  providers:
//	    local:
//	      base_url: http://localhost:11434/v1
type UserModelsConfig struct {
	// TagPreferences overrides the default tag resolution order.
	// Keys are tag names (e.g., "flagship", "moderate", "fast"),
	// values are ordered lists of model IDs to try when resolving that tag.
	//
	// When a tag is resolved, models are tried in this order:
	//   1. Models listed in TagPreferences for this tag (in order)
	//   2. Remaining models with this tag (in default order)
	//
	// Models listed in preferences that don't have the specified tag are
	// silently skipped. Unknown model IDs generate a validation warning.
	//
	// Example YAML:
	//
	//	tag_preferences:
	//	  flagship: [claude-opus-4-20250514, gpt-4.1]    # Prefer Claude, fall back to GPT
	//	  fast: [gpt-4.1-mini, claude-haiku-3-5]         # Prefer GPT for speed
	//	  reasoning: [o3, claude-opus-4-20250514]        # Prefer o3 for reasoning
	//
	// YAML key: tag_preferences
	TagPreferences map[string][]string `yaml:"tag_preferences,omitempty" json:"tag_preferences,omitempty" mapstructure:"tag_preferences"`

	// Custom defines user-added models that extend the built-in model registry.
	// Use this to add local models (Ollama, LM Studio), custom OpenRouter models,
	// or models from other OpenAI-compatible providers.
	//
	// Required fields for each custom model:
	//   - id: Unique identifier (must not conflict with built-in models)
	//   - providers: At least one provider mapping
	//
	// Optional fields:
	//   - name: Human-readable display name (defaults to id)
	//   - tags: List of tags for tag-based selection
	//   - visibility: "user", "meta", or "dev" (defaults to "user")
	//   - capabilities: Model capabilities (context window, features)
	//   - cost: Pricing information for cost tracking
	//
	// Example YAML:
	//
	//	custom:
	//	  - id: local-qwen
	//	    name: Qwen 2.5 32B
	//	    tags: [local, fast]
	//	    capabilities:
	//	      max_context_window: 32000
	//	      supports_tools: true
	//	    providers:
	//	      - driver: local
	//	        api_model: qwen2.5:32b
	//	  - id: openrouter-mixtral
	//	    name: Mixtral 8x22B (OpenRouter)
	//	    tags: [moderate]
	//	    providers:
	//	      - driver: openrouter
	//	        api_model: mistralai/mixtral-8x22b-instruct
	//
	// YAML key: custom
	Custom []ModelDefinition `yaml:"custom,omitempty" json:"custom,omitempty" mapstructure:"custom"`

	// Providers configures provider-specific settings such as API endpoints.
	// Currently supports configuration for local providers; other providers
	// use API keys from environment variables.
	//
	// Example YAML:
	//
	//	providers:
	//	  local:
	//	    base_url: http://localhost:11434/v1
	//
	// YAML key: providers
	Providers UserProvidersConfig `yaml:"providers,omitempty" json:"providers,omitempty" mapstructure:"providers"`
}

// UserProvidersConfig contains user configuration for model providers.
// Each provider that supports user configuration has its own settings struct.
//
// Currently supported providers:
//   - Local: For Ollama, LM Studio, or any OpenAI-compatible local server
//
// Cloud providers (Anthropic, OpenAI, OpenRouter) are configured via
// environment variables for API keys and use default endpoints.
//
// Example YAML:
//
//	providers:
//	  local:
//	    base_url: http://localhost:11434/v1
type UserProvidersConfig struct {
	// Local configures the local model provider for running models on your machine.
	// Supports any server that exposes an OpenAI-compatible chat completions API,
	// including Ollama, LM Studio, llama.cpp server, and vLLM.
	//
	// When configured, models with driver "local" will use this endpoint.
	// If not configured, local models cannot be used.
	//
	// Example YAML:
	//
	//	local:
	//	  base_url: http://localhost:11434/v1
	//
	// YAML key: local
	Local *LocalProviderConfig `yaml:"local,omitempty" json:"local,omitempty" mapstructure:"local"`
}

// LocalProviderConfig configures a local LLM provider endpoint.
// This allows Reliant to communicate with locally-running model servers
// that expose an OpenAI-compatible API.
//
// Example YAML:
//
//	local:
//	  base_url: http://localhost:11434/v1
type LocalProviderConfig struct {
	// BaseURL is the OpenAI-compatible API endpoint URL.
	// This should be the base URL that, when combined with "/chat/completions",
	// produces a valid chat completions endpoint.
	//
	// Common values:
	//   - Ollama: http://localhost:11434/v1
	//   - LM Studio: http://localhost:1234/v1
	//   - llama.cpp: http://localhost:8080/v1
	//   - vLLM: http://localhost:8000/v1
	//
	// The URL should NOT include trailing slashes or endpoint paths.
	// Include /v1 if the server requires it (most OpenAI-compatible servers do).
	//
	// YAML key: base_url
	// Required: Yes (when local provider is configured)
	BaseURL string `yaml:"base_url" json:"base_url" mapstructure:"base_url"`
}
