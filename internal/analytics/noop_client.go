// Copyright (c) 2025 Reliant Labs
package analytics

// NoopClient is a no-op implementation of AnalyticsClient that does nothing
// This is used when analytics is disabled for privacy
type NoopClient struct{}

// NewNoopClient creates a new no-op analytics client
func NewNoopClient() *NoopClient {
	return &NoopClient{}
}

func (c *NoopClient) Track(eventType EventType, metadata map[string]interface{}) {
	// No-op
}

func (c *NoopClient) TrackWithUser(userID string, eventType EventType, metadata map[string]interface{}) {
	// No-op
}

func (c *NoopClient) TrackWithUserInfo(userInfo UserInfo, eventType EventType, metadata map[string]interface{}) {
	// No-op
}

func (c *NoopClient) TrackSessionStart() {
	// No-op
}

func (c *NoopClient) TrackMessageSent(metrics MessageSentMetrics) {
	// No-op
}

func (c *NoopClient) TrackProjectOpened(metrics ProjectOpenedMetrics) {
	// No-op
}

func (c *NoopClient) TrackWorkflowStarted(metrics WorkflowMetrics) {
	// No-op
}

func (c *NoopClient) TrackWorkflowEnded(metrics WorkflowMetrics) {
	// No-op
}

func (c *NoopClient) TrackWorkflowDraftSaved(metrics WorkflowDraftSavedMetrics) {
	// No-op
}

func (c *NoopClient) TrackWorkflowDraftCreated(metrics WorkflowDraftCreatedMetrics) {
	// No-op
}

func (c *NoopClient) TrackPreferencesUpdated(metrics PreferencesUpdatedMetrics) {
	// No-op
}

func (c *NoopClient) TrackProviderSettingsUpdated(metrics ProviderSettingsUpdatedMetrics) {
	// No-op
}

func (c *NoopClient) TrackPageVisited(metrics PageVisitedMetrics) {
	// No-op
}

func (c *NoopClient) TrackLLMCallCompleted(metrics LLMCallMetrics) {
	// No-op
}

func (c *NoopClient) TrackAPIKeyConfigured(metrics APIKeyConfiguredMetrics) {
	// No-op
}

func (c *NoopClient) TrackFirstMessageSent(metrics FirstMessageSentMetrics) {
	// No-op
}

func (c *NoopClient) TrackOnboardingEvent(eventType EventType, metrics OnboardingMetrics) {
	// No-op
}

func (c *NoopClient) SetUserID(userID string) {
	// No-op
}

func (c *NoopClient) SetUserJWT(token string) {
	// No-op
}

func (c *NoopClient) GetUserID() string {
	return ""
}

func (c *NoopClient) Shutdown() {
	// No-op
}
