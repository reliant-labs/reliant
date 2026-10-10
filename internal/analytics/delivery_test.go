// Copyright (c) 2025 Reliant Labs
package analytics

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests swap the process's default slog handler to read what the
// client logs, so none of them is parallel.

func captureAnalyticsLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logs
}

// fakeStatsig answers every log_event with status and counts the requests.
func fakeStatsig(t *testing.T, status int) *atomic.Int32 {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"error":"x"}`))
	}))
	t.Cleanup(server.Close)
	original := getStatsigEndpoint()
	setStatsigEndpoint(server.URL)
	t.Cleanup(func() { setStatsigEndpoint(original) })
	return &requests
}

func newDeliveryTestClient(t *testing.T, apiKey string) *Client {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	client := &Client{
		apiKey:       apiKey,
		userID:       "user-1",
		httpClient:   &http.Client{Timeout: 5 * time.Second},
		eventQueue:   make([]Event, 0, maxBatchSize),
		sessionID:    "session-1",
		sessionStart: time.Now(),
		ctx:          ctx,
		cancel:       cancel,
	}
	t.Cleanup(client.Shutdown)
	return client
}

// trackAndFlush sends one batch. Each event differs, so the client's dedup
// window cannot swallow it.
func trackAndFlush(client *Client, n int) {
	client.Track("workflow_started", map[string]interface{}{"batch": n})
	client.flush()
}

// countLines counts log lines carrying every one of parts.
func countLines(logs string, parts ...string) int {
	count := 0
	for _, line := range strings.Split(logs, "\n") {
		matched := line != ""
		for _, part := range parts {
			matched = matched && strings.Contains(line, part)
		}
		if matched {
			count++
		}
	}
	return count
}

// In prod the server's root filesystem is read-only and no app data directory
// is configured. A batch Statsig did not take used to be written under
// os.UserConfigDir() (/.config there), so every failed batch logged
// `ERROR [Statsig] Failed to get analytics directory: mkdir /.config:
// read-only file system` — 92 lines in five hours. A failed batch is now
// dropped with one WARN and the disk is never touched.
func TestFlush_FailedBatchIsDroppedWithoutTouchingTheDisk(t *testing.T) {
	logs := captureAnalyticsLogs(t)
	home := t.TempDir()
	require.NoError(t, os.Chmod(home, 0o500), "a read-only home stands in for the read-only root")
	t.Cleanup(func() { _ = os.Chmod(home, 0o700) })
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("RELIANT_APP_DATA_DIR", "")
	requests := fakeStatsig(t, http.StatusBadRequest)
	client := newDeliveryTestClient(t, "client-key")

	trackAndFlush(client, 1)

	assert.Equal(t, int32(1), requests.Load())
	assert.Zero(t, countLines(logs.String(), "level=ERROR"), "a dropped analytics batch is not an error:\n%s", logs)
	assert.Equal(t, 1, countLines(logs.String(), "level=WARN", "batch dropped"))
	entries, err := os.ReadDir(home)
	require.NoError(t, err)
	assert.Empty(t, entries, "nothing is written where no data directory was configured")
}

// The desktop app configures a data directory. Failed batches were written
// there too, and nothing ever read them back, so they only accumulated.
func TestFlush_FailedBatchIsNotPersistedInAConfiguredDataDir(t *testing.T) {
	captureAnalyticsLogs(t)
	appData := t.TempDir()
	t.Setenv("RELIANT_APP_DATA_DIR", appData)
	fakeStatsig(t, http.StatusBadRequest)
	client := newDeliveryTestClient(t, "client-key")

	trackAndFlush(client, 1)

	persisted, err := filepath.Glob(filepath.Join(appData, "analytics", "failed_*.json"))
	require.NoError(t, err)
	assert.Empty(t, persisted)
}

// Prod sent every batch with a key Statsig rejects (401), and logged each
// rejection. One rejection settles it: the key will not start working
// mid-process, so sending stops and the cause is logged once, at WARN, naming
// the variable to fix.
func TestFlush_RejectedKeyStopsSendingAndWarnsOnce(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			logs := captureAnalyticsLogs(t)
			t.Setenv("RELIANT_APP_DATA_DIR", "")
			requests := fakeStatsig(t, status)
			client := newDeliveryTestClient(t, "client-revoked")

			for batch := 1; batch <= 3; batch++ {
				trackAndFlush(client, batch)
			}

			assert.Equal(t, int32(1), requests.Load(), "batches after a rejected key are not sent")
			assert.Equal(t, 1, countLines(logs.String(), "level=WARN", "STATSIG_CLIENT_KEY"), "logged once, naming the variable:\n%s", logs)
			assert.Zero(t, countLines(logs.String(), "level=ERROR"))
			assert.Zero(t, countLines(logs.String(), "Failed to send events"), "a rejected key is not a per-batch failure")
		})
	}
}

// With no key at all there is nothing to send with: the client says so once
// at startup and never contacts Statsig.
func TestNewClientFromSettings_WithoutAKeySendsNothing(t *testing.T) {
	logs := captureAnalyticsLogs(t)
	t.Setenv("RELIANT_ENV", "prod")
	t.Setenv("RELIANT_ANALYTICS_DISABLED", "")
	t.Setenv("STATSIG_CLIENT_KEY", "")
	t.Setenv("RELIANT_APP_DATA_DIR", "")
	t.Setenv("SUPABASE_URL", "")
	requests := fakeStatsig(t, http.StatusUnauthorized)

	client, ok := NewClientFromSettings(context.Background(), "", true).(*Client)
	require.True(t, ok, "prod with analytics on builds the real client")
	client.Track("workflow_started", nil)
	client.Shutdown()

	assert.Zero(t, requests.Load())
	assert.Equal(t, 1, countLines(logs.String(), "level=WARN", "STATSIG_CLIENT_KEY is not set"), "%s", logs)
	assert.Zero(t, countLines(logs.String(), "level=ERROR"))
}
