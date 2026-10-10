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
	key := accountKey(client.accountID, client.accessToken)
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
