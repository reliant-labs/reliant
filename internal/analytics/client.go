// Copyright (c) 2025 Reliant Labs
package analytics

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/denisbrodbeck/machineid"
	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/config"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/version"
)

const (
	defaultStatsigEndpoint = "https://events.statsigapi.net/v1/log_event"
	maxBatchSize           = 100
	flushInterval          = 30 * time.Second
	maxRetries             = 3
	retryBackoff           = 2 * time.Second
)

func getAPIKey() string {
	return os.Getenv("STATSIG_CLIENT_KEY")
}

var (
	statsigEndpoint   = defaultStatsigEndpoint
	statsigEndpointMu sync.RWMutex
)

var (
	instance   AnalyticsClient
	instanceMu sync.RWMutex
)

// Client implements AnalyticsClient for Statsig
type Client struct {
	apiKey       string
	deviceID     string       // Stable machine identifier (never changes)
	userID       string       // Account identifier (anonymous or authenticated)
	userJWT      string       // Raw JWT for authenticated Supabase REST calls
	userIDMu     sync.RWMutex // Protects userID and userJWT for concurrent updates
	httpClient   *http.Client
	eventQueue   []Event
	mu           sync.Mutex
	flushTimer   *time.Timer
	ctx          context.Context
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	sessionID    string
	sessionStart time.Time

	// GTM event sink (opt-in, configured via env vars)
	gtmURL    string // SUPABASE_URL e.g. https://xxx.supabase.co
	gtmAPIKey string // SUPABASE_ANON_KEY (public, safe to ship)

	// Deduplication: track recently sent events to avoid duplicates
	recentEvents   map[string]time.Time // eventKey -> timestamp
	recentEventsMu sync.Mutex
}

const (
	// dedupWindow is the time window for deduplicating identical events
	dedupWindow = 5 * time.Second
)

// Ensure Client implements AnalyticsClient
var _ AnalyticsClient = (*Client)(nil)
var _ AnalyticsClient = (*NoopClient)(nil)

func getStatsigEndpoint() string {
	statsigEndpointMu.RLock()
	defer statsigEndpointMu.RUnlock()
	return statsigEndpoint
}

func setStatsigEndpoint(endpoint string) {
	statsigEndpointMu.Lock()
	defer statsigEndpointMu.Unlock()
	statsigEndpoint = endpoint
}

// NewClientFromSettings creates a new analytics client based on privacy settings
// Returns a NoopClient if analytics is disabled, otherwise returns a real Client
func NewClientFromSettings(ctx context.Context, userID string, analyticsEnabled bool) AnalyticsClient {
	// Check environment override
	if os.Getenv("RELIANT_ANALYTICS_DISABLED") == "true" {
		logging.Info("[Analytics] Analytics disabled by environment variable")
		return NewNoopClient()
	}

	if !analyticsEnabled {
		logging.Info("[Analytics] Analytics disabled by user privacy settings")
		return NewNoopClient()
	}

	// Get stable device ID from filesystem
	deviceID, err := getUserID()
	if err != nil {
		logging.Warn("[Statsig] Failed to get device ID, using random UUID", "error", err)
		deviceID = uuid.New().String()
	}

	// If no userID provided, leave empty (Statsig treats empty as anonymous)
	// userID will be set when user authenticates

	clientCtx, cancel := context.WithCancel(ctx)
	client := &Client{
		apiKey:       getAPIKey(),
		deviceID:     deviceID,
		userID:       userID,
		httpClient:   &http.Client{Timeout: 10 * time.Second},
		eventQueue:   make([]Event, 0, maxBatchSize),
		ctx:          clientCtx,
		cancel:       cancel,
		sessionID:    uuid.New().String(),
		sessionStart: time.Now(),
		recentEvents: make(map[string]time.Time),
		gtmURL:       os.Getenv("SUPABASE_URL"),
		gtmAPIKey:    os.Getenv("SUPABASE_ANON_KEY"),
	}

	client.startBackgroundFlusher()
	return client
}

// SetClient sets the global analytics client instance
// This allows dynamic switching between real and no-op clients
func SetClient(client AnalyticsClient) {
	instanceMu.Lock()
	defer instanceMu.Unlock()

	// Shutdown old client if it exists
	if instance != nil {
		instance.Shutdown()
	}
	instance = client
}

// GetClient returns the global analytics client instance
func GetClient() AnalyticsClient {
	instanceMu.RLock()
	defer instanceMu.RUnlock()

	if instance == nil {
		return NewNoopClient()
	}
	return instance
}

// SetUserID updates the default user ID on the global analytics client
// This should be called when a user logs in/out to ensure all events
// are tracked with the correct user ID
func SetUserID(userID string) {
	instanceMu.RLock()
	defer instanceMu.RUnlock()

	if instance == nil {
		return
	}
	instance.SetUserID(userID)
}

// GetUserID returns the current default user ID
func GetUserID() string {
	instanceMu.RLock()
	defer instanceMu.RUnlock()

	if instance == nil {
		return ""
	}
	return instance.GetUserID()
}

// SetUserJWT updates the JWT on the global analytics client
// This should be called from auth middleware when a user authenticates
func SetUserJWT(token string) {
	instanceMu.RLock()
	defer instanceMu.RUnlock()

	if instance == nil {
		return
	}
	instance.SetUserJWT(token)
}

// Shutdown is a package-level function that shuts down the analytics client
func Shutdown() {
	if instance != nil {
		instance.Shutdown()
	}
}

func (c *Client) Track(eventType EventType, metadata map[string]interface{}) {
	c.TrackWithUser("", eventType, metadata)
}

// TrackWithUser tracks an event with a specific user ID
// If userID is empty, uses the client's default user ID
func (c *Client) TrackWithUser(userID string, eventType EventType, metadata map[string]interface{}) {
	c.TrackWithUserInfo(UserInfo{UserID: userID}, eventType, metadata)
}

// SetUserID updates the default user ID for this client
// This should be called when a user logs in or out
func (c *Client) SetUserID(userID string) {
	c.userIDMu.Lock()
	oldUserID := c.userID
	c.userID = userID
	c.userIDMu.Unlock()
	_ = oldUserID // avoid unused variable warning
}

// GetUserID returns the current default user ID
func (c *Client) GetUserID() string {
	c.userIDMu.RLock()
	defer c.userIDMu.RUnlock()
	return c.userID
}

// SetUserJWT stores the raw JWT for authenticated Supabase REST API calls
func (c *Client) SetUserJWT(token string) {
	c.userIDMu.Lock()
	defer c.userIDMu.Unlock()
	c.userJWT = token
}

// TrackWithUserInfo tracks an event with full user information
// This method allows passing email, IP, user agent, and other user properties
func (c *Client) TrackWithUserInfo(userInfo UserInfo, eventType EventType, metadata map[string]interface{}) {
	if metadata == nil {
		metadata = make(map[string]interface{})
	}

	// Use provided userID or fall back to client's default
	if userInfo.UserID == "" {
		c.userIDMu.RLock()
		userInfo.UserID = c.userID
		c.userIDMu.RUnlock()
	}

	c.addStandardMetadata(userInfo, metadata)

	// Check for duplicate events within the dedup window
	eventKey := c.computeEventKey(eventType, metadata)
	if c.isDuplicateEvent(eventKey) {
		return
	}

	// Set app version if not provided
	if userInfo.AppVersion == "" {
		userInfo.AppVersion = version.Version
	}

	// Set stableID for device tracking (Statsig's standard field)
	userInfo.StableID = c.deviceID

	// Initialize custom map if nil
	if userInfo.Custom == nil {
		userInfo.Custom = make(map[string]interface{})
	}

	// Add system info to custom fields
	userInfo.Custom["os"] = runtime.GOOS
	userInfo.Custom["arch"] = runtime.GOARCH
	userInfo.Custom["sessionID"] = c.sessionID

	event := Event{
		Name:      string(eventType),
		User:      userInfo,
		Metadata:  metadata,
		Timestamp: time.Now().UnixMilli(),
	}

	c.mu.Lock()
	c.eventQueue = append(c.eventQueue, event)
	queueLen := len(c.eventQueue)
	shouldFlush := queueLen >= maxBatchSize
	c.mu.Unlock()

	if shouldFlush {
		go c.flush()
	}
}

func (c *Client) addStandardMetadata(userInfo UserInfo, metadata map[string]interface{}) {
	envTier := "development"
	switch config.GetEnvironment() {
	case config.EnvironmentProd:
		envTier = "production"
	case config.EnvironmentTest:
		envTier = "test"
	}

	authState := "anonymous"
	if userInfo.UserID != "" {
		authState = "authenticated"
	}

	putIfAbsent := func(key string, value interface{}) {
		if _, exists := metadata[key]; !exists {
			metadata[key] = value
		}
	}

	putIfAbsent("event_version", "v1")
	putIfAbsent("app_version", version.Version)
	putIfAbsent("platform", "backend")
	putIfAbsent("env_tier", envTier)
	putIfAbsent("session_id", c.sessionID)
	putIfAbsent("auth_state", authState)
	putIfAbsent("is_first_seen", false)
	putIfAbsent("stable_id", c.deviceID)
	if userInfo.UserID != "" {
		putIfAbsent("user_id", userInfo.UserID)
	}
}

// computeEventKey generates a unique key for an event based on type and key metadata fields
func (c *Client) computeEventKey(eventType EventType, metadata map[string]interface{}) string {
	// Build key from event type and identifying metadata
	key := string(eventType)

	// Add identifying fields based on event type
	switch eventType {
	case EventWorkflowStarted, EventWorkflowEnded:
		if wfID, ok := metadata["workflowId"].(string); ok {
			key += ":" + wfID
		}
		key += ":" + fmt.Sprintf("%v", metadata["success"])
	case EventMessageSent:
		if messageID, ok := metadata["messageId"].(string); ok {
			key += ":" + messageID
		}
	case EventPageVisited:
		if page, ok := metadata["pageName"].(string); ok {
			key += ":" + page
		}
	case EventProjectOpened:
		if projectID, ok := metadata["projectId"].(string); ok {
			key += ":" + projectID
		}
	case EventWorkflowDraftSaved:
		if slug, ok := metadata["workflowSlug"].(string); ok {
			key += ":" + slug
		}
	case EventWorkflowDraftCreated:
		if slug, ok := metadata["workflowSlug"].(string); ok {
			key += ":" + slug
		}
	case EventPreferencesUpdated:
		if data, err := json.Marshal(metadata["changedKeys"]); err == nil {
			key += ":" + string(data)
		}
	case EventProviderSettingsUpdate:
		if provider, ok := metadata["provider"].(string); ok {
			key += ":" + provider
		}
		if action, ok := metadata["action"].(string); ok {
			key += ":" + action
		}
	default:
		// For other events, use JSON representation of metadata
		if data, err := json.Marshal(metadata); err == nil {
			key += ":" + string(data)
		}
	}

	return key
}

// isDuplicateEvent checks if an event was recently sent and should be deduplicated
func (c *Client) isDuplicateEvent(eventKey string) bool {
	now := time.Now()

	c.recentEventsMu.Lock()
	defer c.recentEventsMu.Unlock()

	// Lazy initialization for backwards compatibility with tests
	if c.recentEvents == nil {
		c.recentEvents = make(map[string]time.Time)
	}

	// Clean up old entries periodically (every 100 checks or so, when map gets large)
	if len(c.recentEvents) > 100 {
		for k, t := range c.recentEvents {
			if now.Sub(t) > dedupWindow {
				delete(c.recentEvents, k)
			}
		}
	}

	// Check if this event was recently sent
	if lastSent, exists := c.recentEvents[eventKey]; exists {
		if now.Sub(lastSent) < dedupWindow {
			return true // Duplicate
		}
	}

	// Record this event
	c.recentEvents[eventKey] = now
	return false
}

func (c *Client) TrackSessionStart() {

	metadata := map[string]interface{}{
		"sessionId": c.sessionID,
		"startTime": c.sessionStart.Format(time.RFC3339),
	}

	c.Track(EventSessionStart, metadata)
}

func (c *Client) TrackMessageSent(metrics MessageSentMetrics) {
	metadata := map[string]interface{}{
		"messageId":      metrics.MessageID,
		"chatId":         metrics.ChatID,
		"projectId":      metrics.ProjectID,
		"hasAttachments": metrics.HasAttachments,
		"contentLength":  metrics.ContentLength,
	}
	if metrics.WorkflowID != "" {
		metadata["workflowId"] = metrics.WorkflowID
	}
	if metrics.ThreadID != "" {
		metadata["threadId"] = metrics.ThreadID
	}
	if metrics.WorkflowName != "" {
		metadata["workflowName"] = metrics.WorkflowName
	}
	if metrics.WorkflowType != "" {
		metadata["workflowType"] = metrics.WorkflowType
	}
	metadata["isFirstInChat"] = metrics.IsFirstInChat
	c.Track(EventMessageSent, metadata)
}

func (c *Client) TrackProjectOpened(metrics ProjectOpenedMetrics) {
	metadata := map[string]interface{}{
		"projectId": metrics.ProjectID,
		"isGitRepo": metrics.IsGitRepo,
	}
	c.Track(EventProjectOpened, metadata)
}

func (c *Client) TrackWorkflowStarted(metrics WorkflowMetrics) {
	metadata := map[string]interface{}{
		"workflowId":   metrics.WorkflowID,
		"workflowName": metrics.WorkflowName,
		"workflowType": metrics.WorkflowType,
		"chatId":       metrics.ChatID,
		"isWorkspace":  metrics.IsWorkspace,
		"isChild":      metrics.IsChild,
	}
	if len(metrics.Presets) > 0 {
		metadata["presets"] = metrics.Presets
	}
	if metrics.ModelID != "" {
		metadata["modelId"] = metrics.ModelID
	}
	if len(metrics.Providers) > 0 {
		metadata["providers"] = metrics.Providers
	}
	if metrics.ProjectID != "" {
		metadata["projectId"] = metrics.ProjectID
	}
	if len(metrics.PresetTypes) > 0 {
		metadata["presetTypes"] = metrics.PresetTypes
	}
	c.Track(EventWorkflowStarted, metadata)
}

func (c *Client) TrackWorkflowEnded(metrics WorkflowMetrics) {
	metadata := map[string]interface{}{
		"workflowId":   metrics.WorkflowID,
		"workflowName": metrics.WorkflowName,
		"workflowType": metrics.WorkflowType,
		"chatId":       metrics.ChatID,
		"duration":     metrics.Duration.Milliseconds(),
		"success":      metrics.Success,
		"isWorkspace":  metrics.IsWorkspace,
		"isChild":      metrics.IsChild,
	}
	if len(metrics.Presets) > 0 {
		metadata["presets"] = metrics.Presets
	}
	if metrics.ModelID != "" {
		metadata["modelId"] = metrics.ModelID
	}
	if len(metrics.Providers) > 0 {
		metadata["providers"] = metrics.Providers
	}
	if metrics.ProjectID != "" {
		metadata["projectId"] = metrics.ProjectID
	}
	if len(metrics.PresetTypes) > 0 {
		metadata["presetTypes"] = metrics.PresetTypes
	}
	if metrics.ErrorMessage != "" {
		metadata["errorMessage"] = metrics.ErrorMessage
	}
	if metrics.Iterations > 0 {
		metadata["iterations"] = metrics.Iterations
	}
	c.Track(EventWorkflowEnded, metadata)
}

func (c *Client) TrackWorkflowDraftSaved(metrics WorkflowDraftSavedMetrics) {
	metadata := map[string]interface{}{
		"workflowSlug": metrics.WorkflowSlug,
		"workflowName": metrics.WorkflowName,
		"isNew":        metrics.IsNew,
		"isValid":      metrics.IsValid,
	}
	c.Track(EventWorkflowDraftSaved, metadata)
}

func (c *Client) TrackWorkflowDraftCreated(metrics WorkflowDraftCreatedMetrics) {
	metadata := map[string]interface{}{
		"workflowSlug": metrics.WorkflowSlug,
		"workflowName": metrics.WorkflowName,
	}
	if metrics.ProjectID != "" {
		metadata["projectId"] = metrics.ProjectID
	}
	c.Track(EventWorkflowDraftCreated, metadata)
}

func (c *Client) TrackPreferencesUpdated(metrics PreferencesUpdatedMetrics) {
	metadata := map[string]interface{}{}
	if len(metrics.ChangedKeys) > 0 {
		metadata["changedKeys"] = metrics.ChangedKeys
	}
	metadata["modelProviderSettingsChanged"] = metrics.ModelProviderSettingsChanged
	c.Track(EventPreferencesUpdated, metadata)
}

func (c *Client) TrackProviderSettingsUpdated(metrics ProviderSettingsUpdatedMetrics) {
	metadata := map[string]interface{}{
		"provider": metrics.Provider,
		"action":   metrics.Action,
	}
	if metrics.AuthMethod != "" {
		metadata["authMethod"] = metrics.AuthMethod
	}
	c.Track(EventProviderSettingsUpdate, metadata)
}

func (c *Client) TrackPageVisited(metrics PageVisitedMetrics) {
	metadata := map[string]interface{}{
		"pageName": metrics.PageName,
	}
	if metrics.PreviousPage != "" {
		metadata["previousPage"] = metrics.PreviousPage
	}
	c.Track(EventPageVisited, metadata)
}

func (c *Client) TrackLLMCallCompleted(metrics LLMCallMetrics) {
	metadata := map[string]interface{}{
		"provider":     metrics.Provider,
		"model":        metrics.Model,
		"inputTokens":  metrics.InputTokens,
		"outputTokens": metrics.OutputTokens,
		"latencyMs":    metrics.LatencyMs,
		"success":      metrics.Success,
		"isStreaming":  metrics.IsStreaming,
	}
	if metrics.ErrorType != "" {
		metadata["errorType"] = metrics.ErrorType
	}
	if metrics.WorkflowID != "" {
		metadata["workflowId"] = metrics.WorkflowID
	}
	if metrics.ChatID != "" {
		metadata["chatId"] = metrics.ChatID
	}
	if metrics.StepName != "" {
		metadata["stepName"] = metrics.StepName
	}
	if metrics.CacheReadTokens > 0 {
		metadata["cacheReadTokens"] = metrics.CacheReadTokens
	}
	if metrics.CacheWriteTokens > 0 {
		metadata["cacheWriteTokens"] = metrics.CacheWriteTokens
	}
	c.Track(EventLLMCallCompleted, metadata)
}

func (c *Client) TrackAPIKeyConfigured(metrics APIKeyConfiguredMetrics) {
	metadata := map[string]interface{}{
		"provider":       metrics.Provider,
		"authMethod":     metrics.AuthMethod,
		"isFirstKey":     metrics.IsFirstKey,
		"totalProviders": metrics.TotalProviders,
	}
	c.Track(EventAPIKeyConfigured, metadata)
}

func (c *Client) TrackFirstMessageSent(metrics FirstMessageSentMetrics) {
	metadata := map[string]interface{}{
		"chatId":       metrics.ChatID,
		"projectId":    metrics.ProjectID,
		"workflowName": metrics.WorkflowName,
		"workflowType": metrics.WorkflowType,
	}
	if metrics.Provider != "" {
		metadata["provider"] = metrics.Provider
	}
	c.Track(EventFirstMessageSent, metadata)
}

func (c *Client) TrackOnboardingEvent(eventType EventType, metrics OnboardingMetrics) {
	metadata := map[string]interface{}{
		"totalSteps":     metrics.TotalSteps,
		"stepsCompleted": metrics.StepsCompleted,
		"stepsSkipped":   metrics.StepsSkipped,
	}
	if metrics.StepID != "" {
		metadata["stepId"] = metrics.StepID
	}
	if metrics.StepName != "" {
		metadata["stepName"] = metrics.StepName
	}
	c.Track(eventType, metadata)
}

func (c *Client) Shutdown() {
	// Stop the background flusher timer first
	c.mu.Lock()
	if c.flushTimer != nil {
		c.flushTimer.Stop()
	}
	c.mu.Unlock()

	// Flush pending events BEFORE canceling context
	// This ensures the HTTP requests can complete
	c.flush()

	// Now cancel the context to stop background workers
	c.cancel()

	// Wait for background workers to finish
	c.wg.Wait()
}

func (c *Client) startBackgroundFlusher() {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		ticker := time.NewTicker(flushInterval)
		defer ticker.Stop()

		for {
			select {
			case <-c.ctx.Done():
				return
			case <-ticker.C:
				c.flush()
				c.cleanupDedupMap()
			}
		}
	}()
}

// cleanupDedupMap removes expired entries from the deduplication map
func (c *Client) cleanupDedupMap() {
	now := time.Now()

	c.recentEventsMu.Lock()
	defer c.recentEventsMu.Unlock()

	for k, t := range c.recentEvents {
		if now.Sub(t) > dedupWindow {
			delete(c.recentEvents, k)
		}
	}
}

func (c *Client) flush() {
	c.mu.Lock()
	if len(c.eventQueue) == 0 {
		c.mu.Unlock()
		return
	}

	events := make([]Event, len(c.eventQueue))
	copy(events, c.eventQueue)
	c.eventQueue = c.eventQueue[:0]
	c.mu.Unlock()

	payload := map[string]interface{}{
		"events": events,
	}

	if err := c.sendEvents(payload); err != nil {
		logging.Warn("[Statsig] Failed to send events", "error", err, "eventCount", len(events))
		c.saveFailedEvents(events)
	}

	// Write qualifying events to GTM (non-blocking, failures don't affect Statsig)
	if c.gtmEnabled() {
		c.writeToGTM(events)
	}
}

func (c *Client) sendEvents(payload map[string]interface{}) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal events: %w", err)
	}

	var lastErr error
	for i := 0; i < maxRetries; i++ {
		if i > 0 {
			time.Sleep(retryBackoff * time.Duration(i))
		}

		req, err := http.NewRequestWithContext(c.ctx, "POST", getStatsigEndpoint(), bytes.NewReader(data))
		if err != nil {
			lastErr = err
			continue
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("statsig-api-key", c.apiKey)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		body, _ := io.ReadAll(resp.Body)
		if err := resp.Body.Close(); err != nil {
			logging.Warn("[Statsig] Failed to close response body", "error", err)
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return nil
		}

		lastErr = fmt.Errorf("statsig API error: status=%d, body=%s", resp.StatusCode, string(body))

		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return lastErr
		}
	}

	return fmt.Errorf("failed after %d retries: %w", maxRetries, lastErr)
}

// gtmEnabled returns true if the GTM event sink is configured.
func (c *Client) gtmEnabled() bool {
	return c.gtmURL != "" && c.gtmAPIKey != ""
}

// gtmEventTypes defines which events are forwarded to the GTM pipeline.
var gtmEventTypes = map[string]bool{
	"session_start":             true,
	"message_sent":              true,
	"workflow_started":          true,
	"workflow_completed":        true,
	"workflow_draft_created":    true,
	"project_opened":            true,
	"api_key_configured":        true,
	"first_message_sent":        true,
	"onboarding_completed":      true,
	"onboarding_skipped":        true,
	"llm_call_completed":        true,
	"provider_settings_updated": true,
}

// writeToGTM sends qualifying events to the Supabase REST API for the GTM pipeline.
// This is non-blocking — failures are logged and skipped, never affecting Statsig delivery.
func (c *Client) writeToGTM(events []Event) {
	c.userIDMu.RLock()
	jwt := c.userJWT
	userID := c.userID
	c.userIDMu.RUnlock()

	// Need both user ID and JWT for authenticated writes
	if jwt == "" || userID == "" {
		return
	}

	for _, e := range events {
		if !gtmEventTypes[e.Name] {
			continue
		}

		idempotencyKey := fmt.Sprintf("app:%s:%s:%d", e.Name, userID, e.Timestamp)

		body, err := json.Marshal(map[string]interface{}{
			"idempotency_key": idempotencyKey,
			"source":          "app",
			"event_type":      e.Name,
			"user_id":         userID,
			"payload":         e.Metadata,
		})
		if err != nil {
			continue
		}

		req, err := http.NewRequestWithContext(c.ctx, "POST",
			c.gtmURL+"/rest/v1/events",
			bytes.NewReader(body))
		if err != nil {
			continue
		}

		req.Header.Set("apikey", c.gtmAPIKey)
		req.Header.Set("Authorization", "Bearer "+jwt)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Prefer", "return=minimal")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			// Non-fatal: Statsig delivery is unaffected
			continue
		}
		resp.Body.Close()

		// 201 = created, 409 = conflict on idempotency_key = already exists = fine
		if resp.StatusCode != 201 && resp.StatusCode != 409 {
			logging.Warn("[GTM] Event write failed (non-fatal)",
				"event", e.Name, "status", resp.StatusCode)
		}
	}
}

// getAnalyticsDataDir returns the directory path for analytics data storage.
// Analytics is internal data, stored in the platform-specific app data directory.
func getAnalyticsDataDir() (string, error) {
	// Use RELIANT_APP_DATA_DIR for internal data (analytics, auth, databases)
	if appDataDir := os.Getenv("RELIANT_APP_DATA_DIR"); appDataDir != "" {
		dataDir := filepath.Join(appDataDir, "analytics")
		if err := os.MkdirAll(dataDir, 0700); err != nil {
			return "", fmt.Errorf("failed to create analytics directory: %w", err)
		}
		return dataDir, nil
	}

	// Fall back to platform-specific app data directory
	userConfigDir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("failed to get user config directory: %w", err)
	}

	dataDir := filepath.Join(userConfigDir, "reliant", "analytics")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return "", fmt.Errorf("failed to create analytics directory: %w", err)
	}

	return dataDir, nil
}

func (c *Client) saveFailedEvents(events []Event) {
	dataDir, err := getAnalyticsDataDir()
	if err != nil {
		logging.Error("[Statsig] Failed to get analytics directory", "error", err)
		return
	}

	failedFile := filepath.Join(dataDir, fmt.Sprintf("failed_%d.json", time.Now().Unix()))
	data, err := json.Marshal(events)
	if err != nil {
		logging.Error("[Statsig] Failed to marshal failed events", "error", err)
		return
	}

	if err := os.WriteFile(failedFile, data, 0600); err != nil {
		logging.Error("[Statsig] Failed to save failed events", "error", err)
	}
}

func getUserID() (string, error) {
	dataDir, err := getAnalyticsDataDir()
	if err != nil {
		return "", fmt.Errorf("failed to get analytics directory: %w", err)
	}

	userIDFile := filepath.Join(dataDir, "userid")

	// #nosec G304 -- userIDFile is derived from internal analytics data directory
	if data, err := os.ReadFile(userIDFile); err == nil && len(data) > 0 {
		return string(data), nil
	}

	id, err := machineid.ProtectedID("reliant")
	if err != nil {
		id = uuid.New().String()
	}

	if err := os.WriteFile(userIDFile, []byte(id), 0600); err != nil {
		logging.Warn("[Statsig] Failed to save user ID to file", "error", err)
	}
	return id, nil
}
