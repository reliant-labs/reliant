// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"

	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// The ledger row names the provider account that served the request
// (llm_call_usage.account_id, org-provider-accounts.md §3.7), and "" when the
// request was not routed to one.
func TestUsageMeter_RecordsTheServingAccount(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { answered(w) }))
	defer srv.Close()

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "proj-usage-account", "user-usage-account")
	chat := h.CreateTestChat(ctx, "chat-usage-account", project.ID, project.UserID)

	driver := reliantTestDriver(srv.URL)
	routed := &resolvedLLMCall{Driver: driver, Model: driver.Model(),
		Account: &routedAccount{Config: models.AccountConfig{DriverConfig: models.DriverConfig{AccountID: "acct-served"}}}}
	unrouted := &resolvedLLMCall{Driver: driver, Model: driver.Model()}
	exec := func(ctx context.Context, _ string) (string, error) {
		meter := newUsageMeter(h.Repo(), project.UserID, chat.ID, "title")
		_, err := meter.streamAndAccumulate(ctx, routed, nil, nil, nil)
		require.NoError(t, err)
		_, err = meter.streamAndAccumulate(ctx, unrouted, nil, nil, nil)
		require.NoError(t, err)
		return "", nil
	}
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivityWithOptions(exec, activity.RegisterOptions{Name: "MeteredAccount"})
	_, err := env.ExecuteActivity("MeteredAccount", "x")
	require.NoError(t, err)

	var got []string
	rows, err := h.DB().QueryContext(ctx, `SELECT account_id FROM llm_call_usage WHERE chat_id = $1 AND purpose = 'title' ORDER BY id`, chat.ID)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		got = append(got, id)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"acct-served", ""}, got, "ordinal 1 was routed, ordinal 2 was not")

	assert.Equal(t, "", usageAccountID(nil))
	assert.Equal(t, "", usageAccountID(unrouted))
	assert.Equal(t, "acct-served", usageAccountID(routed))
}
