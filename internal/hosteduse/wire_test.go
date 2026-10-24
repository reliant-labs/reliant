package hosteduse

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/reliant-labs/reliant/pkg/hostedhttp"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
)

func TestExecuteWireDigestPreservesNumbersAndNoCallerClaims(t *testing.T) {
	store := &memoryStore{}
	req := hostedhttp.Request{URL: "https://trader.example/rpc", Method: "POST", Body: map[string]any{"integer": json.Number("9007199254740993"), "fraction": json.Number("1.2300"), "escaped": "<tag>&é"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/internal/hosted-http/execute", r.URL.Path)
		var payload struct {
			GrantID  string             `json:"grant_id"`
			TicketID string             `json:"ticket_id"`
			Request  hostedhttp.Request `json:"request"`
		}
		decoder := json.NewDecoder(r.Body)
		decoder.UseNumber()
		decoder.DisallowUnknownFields()
		require.NoError(t, decoder.Decode(&payload))
		require.Equal(t, "grant", payload.GrantID)
		require.Len(t, store.tickets, 1)
		require.Equal(t, store.tickets[0].ID, payload.TicketID)
		digest, err := hostedhttp.RequestDigest(payload.Request)
		require.NoError(t, err)
		require.Equal(t, store.tickets[0].RequestSHA256, digest)
		require.Equal(t, json.Number("9007199254740993"), payload.Request.Body.(map[string]any)["integer"])
		_, _ = w.Write([]byte(`{"content":"ok","data":{},"status_code":200,"is_error":false,"retryable":false}`))
	}))
	defer server.Close()
	c := newTestClient(t, store, server.URL)
	actionTest(t, "Action", func(ctx context.Context) error {
		_, err := c.ExecuteAction(ctx, activity.GetInfo(ctx).WorkflowExecution.ID, "node", "grant", req)
		require.NoError(t, err)
		return nil
	})
}

func TestOversizeRequestNeverPersistsTicket(t *testing.T) {
	store := &memoryStore{}
	c := newTestClient(t, store, "https://controller.example")
	actionTest(t, "Action", func(ctx context.Context) error {
		req := sampleRequest
		req.Body = strings.Repeat("x", hostedhttp.MaxRequestBytes)
		_, err := c.ExecuteAction(ctx, activity.GetInfo(ctx).WorkflowExecution.ID, "node", "grant", req)
		require.Error(t, err)
		_, err = c.Issue(ctx, activity.GetInfo(ctx).WorkflowExecution.ID, "node", "grant", req)
		require.Error(t, err)
		return nil
	})
	require.Empty(t, store.tickets)
}
