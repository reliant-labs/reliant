// Copyright (c) 2025 Reliant Labs
package codex

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers/registry"
)

// A /codex/models refusal makes Codex unavailable (#671), and it must not stay
// that way once the backend accepts the credential again: the verdict is cached
// for availabilityFailureTTL only, then asked again — no reconnect, no restart.
// It is also served from cache inside that window, so a refusing backend is
// not hammered by every resolution.
func TestReportAvailability_RejectionRecoversWithoutReconnect(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile("testdata/codex_models.json")
	require.NoError(t, err)

	var status atomic.Int32
	status.Store(http.StatusUnauthorized)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		assert.Equal(t, CodexVersion, r.URL.Query().Get("client_version"))
		if s := int(status.Load()); s != http.StatusOK {
			w.WriteHeader(s)
			_, _ = w.Write([]byte(`{"detail":"Could not parse your authentication token. Please try signing in again."}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	token := mintJWT(t, "acct-recovers-without-reconnect", time.Now().Add(time.Hour))
	client, err := NewClient(llm.DriverOptions{ApiKey: token, BaseURL: srv.URL})
	require.NoError(t, err)
	ctx := context.Background()

	_, err = client.ReportAvailability(ctx)
	require.ErrorIs(t, err, registry.ErrCredentialRejected, "a 401 from /codex/models is a verdict")

	status.Store(http.StatusOK)
	_, err = client.ReportAvailability(ctx)
	require.ErrorIs(t, err, registry.ErrCredentialRejected, "inside the failure TTL the verdict is served from cache")
	assert.Equal(t, int32(1), hits.Load())

	// The failure TTL elapses.
	key := client.availabilityKey()
	availabilityMu.Lock()
	entry := availabilityCache[key]
	entry.fetchedAt = time.Now().Add(-availabilityFailureTTL - time.Second)
	availabilityCache[key] = entry
	availabilityMu.Unlock()

	report, err := client.ReportAvailability(ctx)
	require.NoError(t, err, "once the backend accepts the credential, Codex is available again")
	assert.Equal(t, int32(2), hits.Load())
	assert.Contains(t, report.Models, "gpt-6.1-sol")
}

// The catalog is the backend's answer for a credential, so it is cached per
// backend AND credential: the same token asked of a second backend asks that
// backend rather than replaying the first one's report. Keyed on the
// credential alone, a test re-run inside the same second (identical minted
// token, fresh httptest backend) was served the previous run's report and its
// backend was never asked.
func TestReportAvailability_CachedPerBackend(t *testing.T) {
	t.Parallel()
	body, err := os.ReadFile("testdata/codex_models.json")
	require.NoError(t, err)

	backend := func(hits *atomic.Int32) *httptest.Server {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(body)
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	var firstHits, secondHits atomic.Int32
	first, second := backend(&firstHits), backend(&secondHits)

	token := mintJWT(t, "acct-cached-per-backend", time.Now().Add(time.Hour))
	ctx := context.Background()
	for _, srv := range []*httptest.Server{first, second, first, second} {
		client, err := NewClient(llm.DriverOptions{ApiKey: token, BaseURL: srv.URL})
		require.NoError(t, err)
		report, err := client.ReportAvailability(ctx)
		require.NoError(t, err)
		assert.Contains(t, report.Models, "gpt-6.1-sol")
	}
	assert.Equal(t, int32(1), firstHits.Load(), "the first backend is asked once, then served from cache")
	assert.Equal(t, int32(1), secondHits.Load(), "the second backend is asked, not answered from the first one's report")
}
