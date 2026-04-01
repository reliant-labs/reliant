// Copyright (c) 2025 Reliant Labs
package analytics

// AnalyticsClient defines the interface for analytics tracking
type AnalyticsClient interface {
	// Track tracks a generic event with metadata
	Track(eventType EventType, metadata map[string]interface{})

	// TrackWithUser tracks an event with a specific user ID
	TrackWithUser(userID string, eventType EventType, metadata map[string]interface{})

	// TrackWithUserInfo tracks an event with full user information
	TrackWithUserInfo(userInfo UserInfo, eventType EventType, metadata map[string]interface{})

	// TrackSessionStart tracks the start of a new session
	TrackSessionStart()

	// TrackMessageSent tracks when a user sends a message in a chat
	TrackMessageSent(metrics MessageSentMetrics)

	// TrackProjectOpened tracks when a user opens/views a project
	TrackProjectOpened(metrics ProjectOpenedMetrics)

	// TrackWorkflowStarted tracks when a workflow execution starts
	TrackWorkflowStarted(metrics WorkflowMetrics)

	// TrackWorkflowEnded tracks when a workflow execution completes
	TrackWorkflowEnded(metrics WorkflowMetrics)

	// TrackWorkflowDraftSaved tracks when a workflow draft is created or modified
	TrackWorkflowDraftSaved(metrics WorkflowDraftSavedMetrics)

	// TrackWorkflowDraftCreated tracks when a new workflow draft is initially created
	TrackWorkflowDraftCreated(metrics WorkflowDraftCreatedMetrics)

	// TrackPreferencesUpdated tracks user preferences/settings updates
	TrackPreferencesUpdated(metrics PreferencesUpdatedMetrics)

	// TrackProviderSettingsUpdated tracks provider credential/config updates
	TrackProviderSettingsUpdated(metrics ProviderSettingsUpdatedMetrics)

	// TrackPageVisited tracks page/panel navigation
	TrackPageVisited(metrics PageVisitedMetrics)

	// TrackLLMCallCompleted tracks an individual LLM API call completion
	TrackLLMCallCompleted(metrics LLMCallMetrics)

	// TrackAPIKeyConfigured tracks when a provider API key is configured
	TrackAPIKeyConfigured(metrics APIKeyConfiguredMetrics)

	// TrackFirstMessageSent tracks when a user sends their very first message
	TrackFirstMessageSent(metrics FirstMessageSentMetrics)

	// TrackOnboardingEvent tracks onboarding flow events
	TrackOnboardingEvent(eventType EventType, metrics OnboardingMetrics)

	// SetUserID updates the default user ID for the client
	// This should be called when a user logs in or out
	SetUserID(userID string)

	// GetUserID returns the current default user ID
	GetUserID() string

	// SetUserJWT stores the raw JWT for authenticated Supabase REST API calls
	SetUserJWT(token string)

	// Shutdown gracefully shuts down the client
	Shutdown()
}
