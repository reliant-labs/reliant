// Copyright (c) 2025 Reliant Labs
package analytics

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrackEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		assert.Equal(t, "test-api-key", r.Header.Get("statsig-api-key"))

		var payload map[string]interface{}
		err := json.NewDecoder(r.Body).Decode(&payload)
		require.NoError(t, err)

		events, ok := payload["events"].([]interface{})
		require.True(t, ok)
		require.Greater(t, len(events), 0)

		event := events[0].(map[string]interface{})
		assert.Equal(t, "test_event", event["eventName"])

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success": true}`))
	}))
	defer server.Close()

	// Override the endpoint
	originalEndpoint := getStatsigEndpoint()
	setStatsigEndpoint(server.URL)
	defer func() { setStatsigEndpoint(originalEndpoint) }()

	// Create test client
	client := &Client{
		apiKey:       "test-api-key",
		userID:       "test-user",
		httpClient:   &http.Client{Timeout: 5 * time.Second},
		eventQueue:   make([]Event, 0, maxBatchSize),
		sessionID:    "test-session",
		sessionStart: time.Now(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	client.ctx = ctx
	client.cancel = cancel

	// Track an event
	client.Track("test_event", map[string]interface{}{
		"test_key": "test_value",
	})

	// Force flush
	client.flush()

	// Cleanup
	client.Shutdown()
}

func TestTrackAddsSnakeCaseStandardMetadata(t *testing.T) {
	var capturedMetadata map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		err := json.NewDecoder(r.Body).Decode(&payload)
		require.NoError(t, err)

		events, ok := payload["events"].([]interface{})
		require.True(t, ok)
		require.Greater(t, len(events), 0)

		event := events[0].(map[string]interface{})
		metadata, ok := event["metadata"].(map[string]interface{})
		require.True(t, ok)
		capturedMetadata = metadata

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success": true}`))
	}))
	defer server.Close()

	originalEndpoint := getStatsigEndpoint()
	setStatsigEndpoint(server.URL)
	defer func() { setStatsigEndpoint(originalEndpoint) }()

	client := &Client{
		apiKey:       "test-api-key",
		userID:       "",
		httpClient:   &http.Client{Timeout: 5 * time.Second},
		eventQueue:   make([]Event, 0, maxBatchSize),
		sessionID:    "test-session",
		sessionStart: time.Now(),
		deviceID:     "test-device",
	}

	ctx, cancel := context.WithCancel(context.Background())
	client.ctx = ctx
	client.cancel = cancel

	client.TrackWithUser("test-user", EventType("test_event"), map[string]interface{}{"k": "v"})
	client.flush()

	require.NotNil(t, capturedMetadata)

	// Standard snake_case metadata keys should exist.
	assert.Equal(t, "v1", capturedMetadata["event_version"])
	assert.NotEmpty(t, capturedMetadata["app_version"])
	assert.Equal(t, "backend", capturedMetadata["platform"])
	assert.Equal(t, "test-session", capturedMetadata["session_id"])
	assert.Equal(t, "authenticated", capturedMetadata["auth_state"])
	assert.Equal(t, false, capturedMetadata["is_first_seen"])
	assert.Equal(t, "test-device", capturedMetadata["stable_id"])
	assert.Equal(t, "test-user", capturedMetadata["user_id"])

	// CamelCase variants should not be present in backend standard metadata.
	assert.NotContains(t, capturedMetadata, "eventVersion")
	assert.NotContains(t, capturedMetadata, "appVersion")
	assert.NotContains(t, capturedMetadata, "envTier")
	assert.NotContains(t, capturedMetadata, "sessionId")
	assert.NotContains(t, capturedMetadata, "authState")
	assert.NotContains(t, capturedMetadata, "isFirstSeen")
	assert.NotContains(t, capturedMetadata, "stableId")
	assert.NotContains(t, capturedMetadata, "userId")

	client.Shutdown()
}

func TestTrackUsesClientUserIDForStandardMetadataFallback(t *testing.T) {
	var capturedMetadata map[string]interface{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		err := json.NewDecoder(r.Body).Decode(&payload)
		require.NoError(t, err)

		events, ok := payload["events"].([]interface{})
		require.True(t, ok)
		require.Greater(t, len(events), 0)

		event := events[0].(map[string]interface{})
		metadata, ok := event["metadata"].(map[string]interface{})
		require.True(t, ok)
		capturedMetadata = metadata

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success": true}`))
	}))
	defer server.Close()

	originalEndpoint := getStatsigEndpoint()
	setStatsigEndpoint(server.URL)
	defer func() { setStatsigEndpoint(originalEndpoint) }()

	client := &Client{
		apiKey:       "test-api-key",
		userID:       "fallback-user",
		httpClient:   &http.Client{Timeout: 5 * time.Second},
		eventQueue:   make([]Event, 0, maxBatchSize),
		sessionID:    "test-session",
		sessionStart: time.Now(),
		deviceID:     "test-device",
	}

	ctx, cancel := context.WithCancel(context.Background())
	client.ctx = ctx
	client.cancel = cancel

	// Track without explicit user ID to force fallback to client.userID.
	client.Track(EventType("test_event_fallback"), map[string]interface{}{"k": "v"})
	client.flush()

	require.NotNil(t, capturedMetadata)
	assert.Equal(t, "fallback-user", capturedMetadata["user_id"])
	assert.Equal(t, "authenticated", capturedMetadata["auth_state"])

	client.Shutdown()
}

func TestBatching(t *testing.T) {
	var mu sync.Mutex
	var receivedBatches [][]Event

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&payload)

		if events, ok := payload["events"].([]interface{}); ok {
			var batch []Event
			for _, e := range events {
				eventBytes, _ := json.Marshal(e)
				var event Event
				_ = json.Unmarshal(eventBytes, &event)
				batch = append(batch, event)
			}

			mu.Lock()
			receivedBatches = append(receivedBatches, batch)
			mu.Unlock()
		}

		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success": true}`))
	}))
	defer server.Close()

	// Override the endpoint
	originalEndpoint := getStatsigEndpoint()
	setStatsigEndpoint(server.URL)
	defer func() { setStatsigEndpoint(originalEndpoint) }()

	client := &Client{
		apiKey:     "test-api-key",
		userID:     "test-user",
		httpClient: &http.Client{Timeout: 5 * time.Second},
		eventQueue: make([]Event, 0, maxBatchSize),

		sessionID:    "test-session",
		sessionStart: time.Now(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	client.ctx = ctx
	client.cancel = cancel

	// Track events to fill exactly one batch
	for i := 0; i < maxBatchSize; i++ {
		client.Track(EventType("test_event"), map[string]interface{}{
			"index": i,
		})
	}

	// Wait for automatic flush
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	assert.GreaterOrEqual(t, len(receivedBatches), 1)
	totalEvents := 0
	for _, batch := range receivedBatches {
		totalEvents += len(batch)
	}
	assert.Equal(t, maxBatchSize, totalEvents)

	// Track one more event to trigger another batch
	client.Track(EventType("test_event"), map[string]interface{}{
		"index": maxBatchSize,
	})

	// Force flush the remaining event
	client.flush()

	// Should have at least one batch, possibly two
	assert.GreaterOrEqual(t, len(receivedBatches), 1)
	mu.Unlock()

	client.Shutdown()
}

func TestRetryLogic(t *testing.T) {
	attemptCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attemptCount++
		if attemptCount < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success": true}`))
	}))
	defer server.Close()

	// Override the endpoint
	originalEndpoint := getStatsigEndpoint()
	setStatsigEndpoint(server.URL)
	defer func() { setStatsigEndpoint(originalEndpoint) }()

	client := &Client{
		apiKey:     "test-api-key",
		userID:     "test-user",
		httpClient: &http.Client{Timeout: 5 * time.Second},
		eventQueue: make([]Event, 0, maxBatchSize),

		sessionID:    "test-session",
		sessionStart: time.Now(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	client.ctx = ctx
	client.cancel = cancel

	// Track an event
	client.Track("test_event", nil)

	// Force flush
	client.flush()

	// Should have retried and eventually succeeded
	assert.Equal(t, 3, attemptCount)

	client.Shutdown()
}

func TestConcurrentTracking(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success": true}`))
	}))
	defer server.Close()

	// Override the endpoint
	originalEndpoint := getStatsigEndpoint()
	setStatsigEndpoint(server.URL)
	defer func() { setStatsigEndpoint(originalEndpoint) }()

	client := &Client{
		apiKey:     "test-api-key",
		userID:     "test-user",
		httpClient: &http.Client{Timeout: 5 * time.Second},
		eventQueue: make([]Event, 0, maxBatchSize),

		sessionID:    "test-session",
		sessionStart: time.Now(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	client.ctx = ctx
	client.cancel = cancel

	// Track events concurrently
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			client.Track(EventType("concurrent_event"), map[string]interface{}{
				"index": index,
			})
		}(i)
	}

	wg.Wait()
	client.flush()
	client.Shutdown()
}

func TestSetUserID(t *testing.T) {
	// Create test client
	client := &Client{
		apiKey:       "test-api-key",
		userID:       "initial-user",
		httpClient:   &http.Client{Timeout: 5 * time.Second},
		eventQueue:   make([]Event, 0, maxBatchSize),
		sessionID:    "test-session",
		sessionStart: time.Now(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	client.ctx = ctx
	client.cancel = cancel
	defer client.Shutdown()

	// Verify initial user ID
	assert.Equal(t, "initial-user", client.GetUserID())

	// Update user ID (simulating login)
	client.SetUserID("authenticated-user")
	assert.Equal(t, "authenticated-user", client.GetUserID())

	// Track an event - should use updated user ID
	client.Track(EventType("test_event"), map[string]interface{}{
		"test": "value",
	})

	// Verify the event has the correct user ID
	client.mu.Lock()
	require.Equal(t, 1, len(client.eventQueue))
	assert.Equal(t, "authenticated-user", client.eventQueue[0].User.UserID)
	client.mu.Unlock()

	// Update to anonymous (simulating logout)
	client.SetUserID("anonymous")
	assert.Equal(t, "anonymous", client.GetUserID())

	// Track another event
	client.Track(EventType("test_event_after_logout"), map[string]interface{}{
		"test": "value2",
	})

	// Verify the second event has anonymous user ID
	client.mu.Lock()
	require.Equal(t, 2, len(client.eventQueue))
	assert.Equal(t, "anonymous", client.eventQueue[1].User.UserID)
	client.mu.Unlock()
}

func TestSetUserIDConcurrent(t *testing.T) {
	// Test concurrent SetUserID and GetUserID calls
	client := &Client{
		apiKey:       "test-api-key",
		userID:       "initial-user",
		httpClient:   &http.Client{Timeout: 5 * time.Second},
		eventQueue:   make([]Event, 0, maxBatchSize),
		sessionID:    "test-session",
		sessionStart: time.Now(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	client.ctx = ctx
	client.cancel = cancel
	defer client.Shutdown()

	var wg sync.WaitGroup

	// Concurrent writes
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			client.SetUserID(fmt.Sprintf("user-%d", index))
		}(i)
	}

	// Concurrent reads
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = client.GetUserID()
		}()
	}

	wg.Wait()

	// Should not panic and should have some valid user ID
	userID := client.GetUserID()
	assert.NotEmpty(t, userID)
}
