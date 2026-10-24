package hosteduse

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
)

func TestLostPOSTResponseDoesNotReexecute(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		require.NoError(t, err)
		_ = conn.Close()
	}))
	defer server.Close()
	store := &memoryStore{}
	c := newTestClient(t, store, server.URL)
	actionTest(t, "Action", func(ctx context.Context) error {
		run := activity.GetInfo(ctx).WorkflowExecution.ID
		_, err := c.ExecuteAction(ctx, run, "node", "grant", sampleRequest)
		require.ErrorContains(t, err, "outcome is unknown")
		_, err = c.ExecuteAction(ctx, run, "node", "grant", sampleRequest)
		require.ErrorContains(t, err, "already attempted")
		return nil
	})
	require.EqualValues(t, 1, calls.Load())
}
