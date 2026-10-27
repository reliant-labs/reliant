package store_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/reliant-labs/reliant/internal/integrations/webhook"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/triggers/triggerspec"
	"github.com/stretchr/testify/require"
)

func TestReviewAdmissionKeyFromVerifiedGitHubProjection(t *testing.T) {
	// Test reads the real repo fixture in-process: cache tracks its content.
	raw, err := os.ReadFile("../integrations/webhook/github/testdata/pull_request.opened.json")
	require.NoError(t, err)
	provider := webhook.NewGitHubProvider("fixture-secret")
	mac := hmac.New(sha256.New, []byte("fixture-secret"))
	_, err = mac.Write(raw)
	require.NoError(t, err)
	request := &webhook.Request{Body: raw, Header: http.Header{"X-Github-Event": []string{"pull_request"}, "X-Github-Delivery": []string{"private-proof"}, "X-Hub-Signature-256": []string{"sha256=" + hex.EncodeToString(mac.Sum(nil))}}, ReceivedAt: time.Now()}
	require.NoError(t, provider.Verify(context.Background(), request))
	delivery, err := provider.Parse(context.Background(), request)
	require.NoError(t, err)
	require.Len(t, delivery.Events, 1)
	event := delivery.Events[0]
	// Exact envelope built by webhook.inboundPayload: stable normalized resource
	// is separate from projected data, not a raw repository field at the root.
	envelope := map[string]any{"integration": "github", "event": event.Type, "resource": event.ResourceKey, "attributes": event.Attributes, "data": event.Data}
	key, err := triggerspec.ConcurrencyKey(store.ReviewAdmissionKeyExpression, triggerspec.Scope{Trigger: map[string]any{"payload": envelope}})
	require.NoError(t, err)
	require.Equal(t, "github:123456:pr:7", key)
	request.Header.Set("X-Hub-Signature-256", "sha256:forged")
	require.Error(t, provider.Verify(context.Background(), request), "unverified delivery cannot enter trusted assignment intake")
}
