package usage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
)

type fundedCall struct {
	org, funding, fundingOrg, account string
	at                                time.Time
	in                                int64
}

func (e *env) recordFunded(user string, c fundedCall) {
	e.t.Helper()
	e.seq++
	_, err := e.repo.RecordLLMCallUsage(context.Background(), db.LLMCallUsage{
		ID: fmt.Sprintf("funded-%d", e.seq), UserID: user, OrgID: c.org, ChatID: "chat", Provider: "anthropic", Model: "claude-opus",
		AuthMode: store.LLMAuthModeBYOK, Purpose: store.LLMPurposeChat, InputTokens: c.in, Outcome: store.LLMOutcomeSuccess,
		Attempt: 1, CreatedAt: c.at, AccountID: c.account, Funding: c.funding, FundingOrgID: c.fundingOrg,
	})
	require.NoError(e.t, err)
}

func bucketsByKey(bs []*pb.LLMUsageBucket) map[string]*pb.LLMUsageBucket {
	out := map[string]*pb.LLMUsageBucket{}
	for _, b := range bs {
		out[b.GetKey()] = b
	}
	return out
}

// The caller's own usage breaks down by who paid and by which account
// served, and filters to one org; the totals never change with the grouping.
func TestGetLLMUsage_ByFundingAccountAndOrg(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.recordFunded("alice", fundedCall{org: "org-a", funding: "org_key", fundingOrg: "org-a", account: "acct-acme", at: day(28), in: 100})
	e.recordFunded("alice", fundedCall{org: "org-a", funding: "personal", account: "acct-own", at: day(28), in: 20})
	e.recordFunded("alice", fundedCall{org: "org-b", funding: "personal", account: "acct-own", at: day(29), in: 3})
	e.recordFunded("alice", fundedCall{org: "", at: day(29), in: 1}) // before funding was recorded
	e.recordFunded("bob", fundedCall{org: "org-a", funding: "org_key", fundingOrg: "org-a", account: "acct-acme", at: day(29), in: 5000})

	req := func(g pb.LLMUsageGroupBy, org *string) *pb.GetLLMUsageRequest {
		return &pb.GetLLMUsageRequest{StartDate: "2026-10-25", EndDate: "2026-10-30", GroupBy: g, OrgId: org}
	}
	byFunding := e.mustGet("alice", req(pb.LLMUsageGroupBy_LLM_USAGE_GROUP_BY_FUNDING, nil))
	f := bucketsByKey(byFunding.GetBuckets())
	assert.EqualValues(t, 100, f["org_key"].GetInputTokens())
	assert.EqualValues(t, 23, f["personal"].GetInputTokens())
	assert.EqualValues(t, 1, f[""].GetInputTokens(), "a call with no funding recorded is \"not recorded\"")
	assert.EqualValues(t, 124, byFunding.GetTotals().GetInputTokens(), "bob's calls are never alice's")
	assert.Equal(t, pb.LLMUsageCompleteness_LLM_USAGE_COMPLETENESS_COMPLETE, byFunding.GetCompleteness())

	byDay := e.mustGet("alice", req(pb.LLMUsageGroupBy_LLM_USAGE_GROUP_BY_DAY, nil))
	assert.Equal(t, byDay.GetTotals().GetInputTokens(), byFunding.GetTotals().GetInputTokens())

	byAccount := e.mustGet("alice", req(pb.LLMUsageGroupBy_LLM_USAGE_GROUP_BY_ACCOUNT, nil))
	assert.ElementsMatch(t, []string{"acct-acme", "acct-own", ""}, keys(byAccount.GetBuckets()))

	inA := e.mustGet("alice", req(pb.LLMUsageGroupBy_LLM_USAGE_GROUP_BY_FUNDING, ptr("org-a")))
	assert.ElementsMatch(t, []string{"org_key", "personal"}, keys(inA.GetBuckets()))
	assert.EqualValues(t, 120, inA.GetTotals().GetInputTokens())
}

// Days whose individual calls are past retention come from the rollup, which
// has no funding: they are summed under "", and the response says so.
func TestGetLLMUsage_ByFundingBeforeRetentionIsNotRecorded(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	old := now.Add(-db.LLMCallUsageRetention - 48*time.Hour)
	e.recordFunded("alice", fundedCall{org: "org-a", funding: "org_key", fundingOrg: "org-a", account: "acct-acme", at: old, in: 7})
	e.recordFunded("alice", fundedCall{org: "org-a", funding: "org_key", fundingOrg: "org-a", account: "acct-acme", at: day(28), in: 100})
	// The retention job deletes the old call's row; its day stays in the rollup.
	_, err := e.repo.PruneLLMCallUsage(context.Background(), now)
	require.NoError(t, err)

	resp := e.mustGet("alice", &pb.GetLLMUsageRequest{StartDate: old.Format("2006-01-02"), EndDate: "2026-10-30", GroupBy: pb.LLMUsageGroupBy_LLM_USAGE_GROUP_BY_FUNDING})
	f := bucketsByKey(resp.GetBuckets())
	assert.EqualValues(t, 7, f[""].GetInputTokens())
	assert.EqualValues(t, 100, f["org_key"].GetInputTokens())
	assert.EqualValues(t, 107, resp.GetTotals().GetInputTokens())
	assert.Equal(t, pb.LLMUsageCompleteness_LLM_USAGE_COMPLETENESS_PARTIAL, resp.GetCompleteness())
	assert.Contains(t, resp.GetNote(), "not recorded")
}
