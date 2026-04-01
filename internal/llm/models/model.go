// Copyright (c) 2025 Reliant Labs
package models

// MaxRetries is the maximum number of retries for API calls
const MaxRetries = 8

// These type definitions are kept here to avoid circular dependencies
// They are used by config and other packages
type (
	ModelID string
	Family  string
)

func (m ModelID) String() string {
	return string(m)
}

// Model represents the configuration and metadata for an LLM model
type TemperatureMode string

const (
	// TemperatureModeAny means we can send an explicit temperature value.
	TemperatureModeAny TemperatureMode = "any"
	// TemperatureModeOmit means the provider/model only supports its default temperature.
	// In this case, we must omit the field entirely.
	TemperatureModeOmit TemperatureMode = "omit"
)

// ReasoningSummaryMode controls which reasoning.summary values are supported
type ReasoningSummaryMode string

const (
	// ReasoningSummaryAny means both 'concise' and 'detailed' are supported (default behavior uses 'concise')
	ReasoningSummaryAny ReasoningSummaryMode = ""
	// ReasoningSummaryDetailedOnly means only 'detailed' is supported (e.g., gpt-5.2-codex)
	ReasoningSummaryDetailedOnly ReasoningSummaryMode = "detailed_only"
)

// Model represents the configuration and metadata for an LLM model
type Model struct {
	ID   ModelID `json:"id"`
	Name string  `json:"name"`

	APIModel string `json:"api_model"`
	// PreferredEndpoint tells the driver which OpenAI-compatible API shape to use.
	//
	// For OpenAI: newer models may require the Responses API instead of Chat Completions.
	// Valid values: "chat_completions", "responses".
	PreferredEndpoint string `json:"preferred_endpoint"`

	// TemperatureMode controls whether we should send temperature at all.
	// Some models (e.g., certain OpenAI GPT-5.x endpoints) reject any non-default temperature.
	TemperatureMode TemperatureMode `json:"temperature_mode"`

	CostPer1MIn            float64              `json:"cost_per_1m_in"`
	CostPer1MOut           float64              `json:"cost_per_1m_out"`
	CostPer1MInCached      float64              `json:"cost_per_1m_in_cached"`
	CostPer1MOutCached     float64              `json:"cost_per_1m_out_cached"`
	ContextWindow          int64                `json:"context_window"`
	DefaultMaxTokens       int64                `json:"default_max_tokens"`
	CanReason              bool                 `json:"can_reason"`
	SupportsAttachments    bool                 `json:"supports_attachments"`
	UseMaxCompletionTokens bool                 `json:"use_max_completion_tokens"`
	ReasoningSummaryMode   ReasoningSummaryMode `json:"reasoning_summary_mode"` // Controls allowed reasoning.summary values
	DevOnly                bool                 `json:"dev_only"`               // If true, only available in development builds
}

// Preference represents a single model preference with its configuration
type Preference struct {
	ModelID      ModelID  // Model identifier (e.g., "claude-4-sonnet")
	Temperature  *float64 // Optional temperature override
	ThinkingMode string   // Thinking mode: "low", "medium", "high", "xhigh". Availability is validated per model+driver capability.
	TokenBudget  *int64   // Optional max tokens override
	DisableCache bool     // Disable caching for this preference
}

// Preferences represents an ordered list of model preferences for an agent
// The first model in the list is the most preferred, with subsequent models as fallbacks
type Preferences []Preference

// Helper functions to create common temperature values
func TempZero() *float64 {
	temp := 0.0
	return &temp
}

func TempVeryLow() *float64 {
	temp := 0.2
	return &temp
}

func TempLow() *float64 {
	temp := 0.3
	return &temp
}

func TempMedLow() *float64 {
	temp := 0.5
	return &temp
}

func TempMedium() *float64 {
	temp := 0.7
	return &temp
}

func TempMedHigh() *float64 {
	temp := 0.8
	return &temp
}

func TempHigh() *float64 {
	temp := 1.0
	return &temp
}
