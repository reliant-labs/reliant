// Copyright (c) 2025 Reliant Labs
package analytics

import "time"

type EventType string

const (
	EventSessionStart EventType = "session_start"
	EventPageVisited  EventType = "page_visited"

	// Chat and Workflow events
	EventMessageSent          EventType = "message_sent"
	EventWorkflowStarted      EventType = "workflow_started"
	EventWorkflowEnded        EventType = "workflow_ended"
	EventWorkflowDraftCreated EventType = "workflow_draft_created"
	EventWorkflowDraftSaved   EventType = "workflow_draft_saved"

	// Project and settings usage events
	EventProjectOpened          EventType = "project_opened"
	EventPreferencesUpdated     EventType = "preferences_updated"
	EventProviderSettingsUpdate EventType = "provider_settings_updated"

	// LLM call tracking
	EventLLMCallCompleted EventType = "llm_call_completed"

	// API key configuration
	// #nosec G101 -- analytics event identifier, not a credential
	EventAPIKeyConfigured EventType = "api_key_configured"

	// First message milestone
	EventFirstMessageSent EventType = "first_message_sent"

	// Onboarding events
	EventOnboardingStarted       EventType = "onboarding_started"
	EventOnboardingStepCompleted EventType = "onboarding_step_completed"
	EventOnboardingStepSkipped   EventType = "onboarding_step_skipped"
	EventOnboardingCompleted     EventType = "onboarding_completed"
	EventOnboardingSkipped       EventType = "onboarding_skipped"
)

type Event struct {
	Name      string                 `json:"eventName"`
	User      UserInfo               `json:"user"`
	Value     interface{}            `json:"value,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	Timestamp int64                  `json:"time"`
}

type UserInfo struct {
	UserID     string                 `json:"userID"`
	StableID   string                 `json:"stableID,omitempty"`
	Email      string                 `json:"email,omitempty"`
	IP         string                 `json:"ip,omitempty"`
	UserAgent  string                 `json:"userAgent,omitempty"`
	Locale     string                 `json:"locale,omitempty"`
	AppVersion string                 `json:"appVersion,omitempty"`
	Country    string                 `json:"country,omitempty"`
	Custom     map[string]interface{} `json:"custom,omitempty"`
}

// LLMCallMetrics tracks individual LLM API call completions
type LLMCallMetrics struct {
	Provider         string `json:"provider"` // anthropic, openai, gemini, groq, openrouter
	Model            string `json:"model"`    // claude-sonnet-4-20250514, gpt-4o, etc.
	InputTokens      int    `json:"inputTokens"`
	OutputTokens     int    `json:"outputTokens"`
	LatencyMs        int64  `json:"latencyMs"`
	Success          bool   `json:"success"`
	ErrorType        string `json:"errorType,omitempty"` // rate_limit, context_length, auth, network, etc.
	IsStreaming      bool   `json:"isStreaming"`
	WorkflowID       string `json:"workflowId,omitempty"`
	ChatID           string `json:"chatId,omitempty"`
	StepName         string `json:"stepName,omitempty"` // workflow step that triggered this call
	CacheReadTokens  int    `json:"cacheReadTokens,omitempty"`
	CacheWriteTokens int    `json:"cacheWriteTokens,omitempty"`
}

// APIKeyConfiguredMetrics tracks when a provider API key is configured
type APIKeyConfiguredMetrics struct {
	Provider       string `json:"provider"`
	AuthMethod     string `json:"authMethod"`     // api_key, oauth, cli_auth
	IsFirstKey     bool   `json:"isFirstKey"`     // first provider ever configured
	TotalProviders int    `json:"totalProviders"` // count after this config
}

// FirstMessageSentMetrics tracks when a user sends their very first message
type FirstMessageSentMetrics struct {
	ChatID       string `json:"chatId"`
	ProjectID    string `json:"projectId"`
	WorkflowName string `json:"workflowName"`
	WorkflowType string `json:"workflowType"` // builtin, custom
	Provider     string `json:"provider,omitempty"`
}

// OnboardingMetrics tracks onboarding flow progress
type OnboardingMetrics struct {
	StepID         string `json:"stepId,omitempty"`
	StepName       string `json:"stepName,omitempty"`
	TotalSteps     int    `json:"totalSteps"`
	StepsCompleted int    `json:"stepsCompleted"`
	StepsSkipped   int    `json:"stepsSkipped"`
}

// WorkflowMetrics tracks workflow execution events
type WorkflowMetrics struct {
	WorkflowID   string        `json:"workflowId"`
	WorkflowName string        `json:"workflowName"`
	WorkflowType string        `json:"workflowType"` // "builtin", "custom", "file"
	ChatID       string        `json:"chatId"`
	ProjectID    string        `json:"projectId,omitempty"`
	IsWorkspace  bool          `json:"isWorkspace"`
	Presets      []string      `json:"presets,omitempty"`
	ModelID      string        `json:"modelId,omitempty"`
	Providers    []string      `json:"providers,omitempty"`
	Duration     time.Duration `json:"duration,omitempty"`
	Success      bool          `json:"success,omitempty"`
	ErrorMessage string        `json:"errorMessage,omitempty"`
	Iterations   int           `json:"iterations,omitempty"`  // For loop workflows
	IsChild      bool          `json:"isChild"`               // Is this a child workflow
	PresetTypes  []string      `json:"presetTypes,omitempty"` // "builtin" or "custom" for each preset
}

// WorkflowDraftCreatedMetrics tracks when a workflow draft is initially created.
type WorkflowDraftCreatedMetrics struct {
	WorkflowSlug string `json:"workflowSlug"`
	WorkflowName string `json:"workflowName"`
	ProjectID    string `json:"projectId,omitempty"`
}

// WorkflowDraftSavedMetrics tracks when a workflow draft is created or modified
type WorkflowDraftSavedMetrics struct {
	WorkflowSlug string `json:"workflowSlug"`
	WorkflowName string `json:"workflowName"`
	IsNew        bool   `json:"isNew"`   // true if created, false if modified
	IsValid      bool   `json:"isValid"` // Whether the workflow passed validation
}

// PageVisitedMetrics tracks page/panel navigation
type PageVisitedMetrics struct {
	PageName     string `json:"pageName"`               // e.g., "chat", "settings", "workflow_hub", "workflow_builder"
	PreviousPage string `json:"previousPage,omitempty"` // The page navigated from
}

// MessageSentMetrics tracks when a user sends a message in a chat.
type MessageSentMetrics struct {
	MessageID      string `json:"messageId"`
	ChatID         string `json:"chatId"`
	ProjectID      string `json:"projectId"`
	WorkflowID     string `json:"workflowId,omitempty"`
	ThreadID       string `json:"threadId,omitempty"`
	HasAttachments bool   `json:"hasAttachments"`
	ContentLength  int    `json:"contentLength"`
	WorkflowName   string `json:"workflowName,omitempty"`
	WorkflowType   string `json:"workflowType,omitempty"` // builtin, custom
	IsFirstInChat  bool   `json:"isFirstInChat"`          // first message in this chat
}

// ProjectOpenedMetrics tracks when a user opens/views a project.
type ProjectOpenedMetrics struct {
	ProjectID string `json:"projectId"`
	IsGitRepo bool   `json:"isGitRepo"`
}

// PreferencesUpdatedMetrics tracks preference changes.
type PreferencesUpdatedMetrics struct {
	ChangedKeys                  []string `json:"changedKeys,omitempty"`
	ModelProviderSettingsChanged bool     `json:"modelProviderSettingsChanged"`
}

// ProviderSettingsUpdatedMetrics tracks provider credential/config changes.
type ProviderSettingsUpdatedMetrics struct {
	Provider   string `json:"provider"`
	Action     string `json:"action"` // connected, disconnected, updated, deleted
	AuthMethod string `json:"authMethod"`
}
