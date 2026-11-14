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

	assert.Equal(t, "", usageFunding(nil, "anthropic"))
	assert.Equal(t, models.FundingLocal, usageFunding(unrouted, "local"))
	assert.Equal(t, "", usageFundingOrg(nil))
	assert.Equal(t, "", usageFundingOrg(unrouted))
	seat := &resolvedLLMCall{Account: &routedAccount{Config: models.AccountConfig{DriverConfig: models.DriverConfig{AttributedOrgID: "org-x"}}}}
	assert.Equal(t, "org-x", usageFundingOrg(seat))

	assert.Equal(t, "", usageAccountID(nil))
	assert.Equal(t, "", usageAccountID(unrouted))
	assert.Equal(t, "acct-served", usageAccountID(routed))
}

// The ledger row names who paid (llm_call_usage.funding) and the run's org; an
// org-funded call is also counted in the org's rollup, which an org admin
// reads, and a personal one never is (org-provider-accounts.md §3.7, §5).
func TestUsageMeter_RecordsFundingAndTheOrgRollup(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { answered(w) }))
	defer srv.Close()

	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "proj-usage-funding", "user-usage-funding")
	chat := h.CreateTestChat(ctx, "chat-usage-funding", project.ID, project.UserID)

	driver := reliantTestDriver(srv.URL)
	served := func(id, org, funding string) *resolvedLLMCall {
		return &resolvedLLMCall{Driver: driver, Model: driver.Model(), OrgID: "org-acme",
			Account: &routedAccount{Config: models.AccountConfig{Funding: funding,
				DriverConfig: models.DriverConfig{AccountID: id, AccountOrgID: org}}}}
	}
	exec := func(ctx context.Context, _ string) (string, error) {
		meter := newUsageMeter(h.Repo(), project.UserID, chat.ID, "title")
		// The org-less managed key bills the person's default org, which is not
		// necessarily the chat's: it never lands in the chat org's rollup.
		managed := served("managed", "", models.FundingOrgWallet)
		for _, r := range []*resolvedLLMCall{
			served("org-key", "org-acme", models.FundingOrgKey),
			served("mine", "", models.FundingPersonal),
			served("org-key", "org-acme", models.FundingOrgKey),
			managed,
		} {
			if _, err := meter.streamAndAccumulate(ctx, r, nil, nil, nil); err != nil {
				return "", err
			}
		}
		return "", nil
	}
	var suite temporaltest.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.RegisterActivityWithOptions(exec, activity.RegisterOptions{Name: "MeteredFunding"})
	_, err := env.ExecuteActivity("MeteredFunding", "x")
	require.NoError(t, err)

	type row struct{ account, org, funding string }
	var got []row
	rows, err := h.DB().QueryContext(ctx, `SELECT account_id, org_id, funding FROM llm_call_usage WHERE chat_id = $1 ORDER BY id`, chat.ID)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var r row
		require.NoError(t, rows.Scan(&r.account, &r.org, &r.funding))
		got = append(got, r)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []row{
		{"org-key", "org-acme", "org_key"}, {"mine", "org-acme", "personal"}, {"org-key", "org-acme", "org_key"},
		{"managed", "org-acme", "org_wallet"},
	}, got)

	var orgRows, orgCalls int
	require.NoError(t, h.DB().QueryRowContext(ctx,
		`SELECT count(*), coalesce(sum(calls), 0) FROM llm_org_usage_daily WHERE org_id = 'org-acme'`).Scan(&orgRows, &orgCalls))
	assert.Equal(t, 1, orgRows, "one rollup row: the org key, never the personal account nor a wallet of unknown org")
	assert.Equal(t, 2, orgCalls)
	var accountInRollup string
	require.NoError(t, h.DB().QueryRowContext(ctx, `SELECT account_id FROM llm_org_usage_daily WHERE org_id = 'org-acme'`).Scan(&accountInRollup))
	assert.Equal(t, "org-key", accountInRollup)
}
