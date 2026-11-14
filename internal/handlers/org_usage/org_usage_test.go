package org_usage_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/accesstokenclient"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/handlers/org_usage"
	"github.com/reliant-labs/reliant/internal/orgs"
	"github.com/reliant-labs/reliant/internal/store"
)

// These tests run the service over a real database and the real usage writer
// (RecordLLMCallUsage), so "an admin never sees a personal row" is proven
// against what the worker actually writes, not against a stub's rows.

var now = time.Date(2026, 11, 14, 15, 0, 0, 0, time.UTC)

// directory is control-plane's membership answer: who is a live member of
// which org, with which org-wide grants. It counts the asks, to prove every
// read asks fresh.
type directory struct {
	mu     sync.Mutex
	grants map[string]map[string][]string // org -> user -> scopes
	err    error
	asks   int
}

func (d *directory) ResolveOrgMembership(_ context.Context, userID, orgID string) (accesstokenclient.OrgMembership, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.asks++
	if d.err != nil {
		return accesstokenclient.OrgMembership{}, d.err
	}
	scopes, ok := d.grants[orgID][userID]
	if !ok {
		return accesstokenclient.OrgMembership{OrgID: orgID}, nil
	}
	return accesstokenclient.OrgMembership{OrgID: orgID, Member: true, Role: "member", OrgScopes: accesstokenclient.NewOrgScopeSet(scopes...)}, nil
}

func (d *directory) set(org, user string, scopes ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.grants[org] == nil {
		d.grants[org] = map[string][]string{}
	}
	d.grants[org][user] = scopes
}

type listPrices struct {
	err   error
	asked [][]controlplane.ModelRef
}

func (p *listPrices) GetModelListPrices(_ context.Context, refs []controlplane.ModelRef) ([]controlplane.ModelListPrice, time.Time, error) {
	p.asked = append(p.asked, refs)
	if p.err != nil {
		return nil, time.Time{}, p.err
	}
	out := make([]controlplane.ModelListPrice, len(refs))
	for i, r := range refs {
		out[i] = controlplane.ModelListPrice{Ref: r}
		if r.Model == "claude-sonnet-4-5" {
			// $3 per million input tokens, $15 per million output.
			out[i] = controlplane.ModelListPrice{Ref: r, Priced: true, CatalogKey: r.Model, InputNanosPerMTok: 3e9, OutputNanosPerMTok: 15e9,
				CacheReadNanosPerMTok: 0.3e9, CacheWriteNanosPerMTok: 3.75e9}
		}
	}
	return out, now.Add(-time.Hour), nil
}

type env struct {
	t      *testing.T
	repo   *db.Repo
	dir    *directory
	prices *listPrices
	svc    *org_usage.Service
	seq    int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	e := &env{t: t, repo: repo, dir: &directory{grants: map[string]map[string][]string{}}, prices: &listPrices{}}
	e.svc = org_usage.NewTestOrgUsage(t, org_usage.WithOrgUsageDeps(org_usage.Deps{
		Store:  repo,
		Prices: e.prices,
		Now:    func() time.Time { return now },
	}))
	// ada is org-a's billing admin; bob a member of org-a without the grant;
	// gus the billing admin of org-b only.
	e.dir.set("org-a", "ada", string(fat.ScopeBillingRead))
	e.dir.set("org-a", "bob")
	e.dir.set("org-a", "carl")
	e.dir.set("org-b", "gus", string(fat.ScopeBillingRead))
	return e
}

// asPerson is userID signed in to the app.
func (e *env) asPerson(userID string) context.Context {
	ctx := auth.WithUser(context.Background(), userID, "authenticated", "")
	ctx = auth.WithCredential(ctx, auth.CredentialSession)
	return authz.WithMembership(ctx, orgs.New(e.dir))
}

type call struct {
	user, org, provider, model, funding, fundingOrg, account string
	in, out                                                  int64
	at                                                       time.Time
}

func (e *env) record(c call) {
	e.t.Helper()
	e.seq++
	if c.at.IsZero() {
		c.at = now.Add(-2 * time.Hour)
	}
	mode := store.LLMAuthModeBYOK
	if c.funding == "org_seat" {
		mode = store.LLMAuthModeSubscription
	}
	_, err := e.repo.RecordLLMCallUsage(context.Background(), db.LLMCallUsage{
		ID: fmt.Sprintf("act-%d", e.seq), UserID: c.user, OrgID: c.org, ChatID: "chat", Provider: c.provider, Model: c.model,
		AuthMode: mode, Purpose: store.LLMPurposeChat, InputTokens: c.in, OutputTokens: c.out, Outcome: store.LLMOutcomeSuccess,
		Attempt: 1, CreatedAt: c.at, AccountID: c.account, Funding: c.funding, FundingOrgID: c.fundingOrg,
	})
	require.NoError(e.t, err)
}

// seed records, in org-a's chats: two org-key calls (one model priced, one
// not), a seat call, Reliant credits, and alice's PERSONAL calls; and an
// org-key call of org-b.
func (e *env) seed() {
	e.record(call{user: "ada", org: "org-a", provider: "anthropic", model: "claude-sonnet-4-5", funding: "org_key", fundingOrg: "org-a", account: "acct-acme-key", in: 1_000_000, out: 100_000})
	e.record(call{user: "bob", org: "org-a", provider: "openai", model: "mystery-1", funding: "org_key", fundingOrg: "org-a", account: "acct-acme-openai", in: 500, out: 50})
	e.record(call{user: "bob", org: "org-a", provider: "claude", model: "claude-sonnet-4-5", funding: "org_seat", fundingOrg: "org-a", account: "acct-bob-seat", in: 2000, out: 200})
	e.record(call{user: "carl", org: "org-a", provider: "reliant", model: "claude-sonnet-4-5", funding: "org_wallet", fundingOrg: "org-a", account: "acct-carl-managed", in: 300, out: 30})
	// alice's own account in org-a's chat: the writer is handed the org as
	// FundingOrgID, and still must not make it the org's.
	e.record(call{user: "alice", org: "org-a", provider: "anthropic", model: "claude-sonnet-4-5", funding: "personal", fundingOrg: "org-a", account: "acct-alice-own", in: 9_000_000, out: 900_000})
	e.record(call{user: "alice", org: "org-a", provider: "claude", model: "claude-sonnet-4-5", funding: "personal", account: "acct-alice-sub", in: 7000, out: 700})
	e.record(call{user: "gus", org: "org-b", provider: "anthropic", model: "claude-sonnet-4-5", funding: "org_key", fundingOrg: "org-b", account: "acct-globex-key", in: 4_000_000, out: 400_000})
}

func (e *env) read(ctx context.Context, org string, groupBy pb.OrgLLMUsageGroupBy) (*pb.GetOrgLLMUsageResponse, error) {
	resp, err := e.svc.GetOrgLLMUsage(ctx, connect.NewRequest(&pb.GetOrgLLMUsageRequest{
		OrgId: org, StartDate: "2026-11-01", EndDate: "2026-11-14", GroupBy: groupBy,
	}))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func keys(buckets []*pb.OrgLLMUsageBucket) []string {
	out := make([]string, 0, len(buckets))
	for _, b := range buckets {
		out = append(out, b.GetKey())
	}
	return out
}

func reason(err error) string {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Meta().Get(svcerr.ReasonHeader)
	}
	return ""
}

func scopes(t *testing.T, names ...fat.Scope) fat.Set {
	t.Helper()
	raw := make([]string, len(names))
	for i, n := range names {
		raw[i] = string(n)
	}
	set, err := fat.NewSet(raw)
	require.NoError(t, err)
	return set
}

// The structural guarantee (D8): whatever the grouping, an admin's read never
// returns a personal row, and the totals hold only org-funded calls.
func TestGetOrgLLMUsage_NeverReturnsAPersonalRow(t *testing.T) {
	e := newEnv(t)
	e.seed()
	ctx := e.asPerson("ada")

	for _, g := range []pb.OrgLLMUsageGroupBy{
		pb.OrgLLMUsageGroupBy_ORG_LLM_USAGE_GROUP_BY_DAY, pb.OrgLLMUsageGroupBy_ORG_LLM_USAGE_GROUP_BY_MEMBER,
		pb.OrgLLMUsageGroupBy_ORG_LLM_USAGE_GROUP_BY_ACCOUNT, pb.OrgLLMUsageGroupBy_ORG_LLM_USAGE_GROUP_BY_MODEL,
		pb.OrgLLMUsageGroupBy_ORG_LLM_USAGE_GROUP_BY_FUNDING, pb.OrgLLMUsageGroupBy_ORG_LLM_USAGE_GROUP_BY_PROVIDER,
	} {
		got, err := e.read(ctx, "org-a", g)
		require.NoError(t, err, g)
		for _, b := range got.GetBuckets() {
			assert.NotContains(t, []string{"alice", "personal", "acct-alice-own", "acct-alice-sub"}, b.GetKey(), "group %v leaked a personal row", g)
		}
		// 1M+500+2000+300 in, 100k+50+200+30 out: alice's 9M never.
		assert.Equal(t, int64(4), got.GetTotals().GetCalls(), g)
		assert.Equal(t, int64(1_002_800), got.GetTotals().GetInputTokens(), g)
		assert.Equal(t, int64(100_280), got.GetTotals().GetOutputTokens(), g)
	}

	byFunding, err := e.read(ctx, "org-a", pb.OrgLLMUsageGroupBy_ORG_LLM_USAGE_GROUP_BY_FUNDING)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"org_key", "org_seat", "org_wallet"}, keys(byFunding.GetBuckets()))

	byMember, err := e.read(ctx, "org-a", pb.OrgLLMUsageGroupBy_ORG_LLM_USAGE_GROUP_BY_MEMBER)
	require.NoError(t, err)
	assert.Equal(t, []string{"ada", "bob", "carl"}, keys(byMember.GetBuckets()))

	// Asking for a personal funding filter is not even expressible: the enum
	// has no personal value, and an unknown one is refused.
	_, err = e.svc.GetOrgLLMUsage(ctx, connect.NewRequest(&pb.GetOrgLLMUsageRequest{
		OrgId: "org-a", StartDate: "2026-11-01", EndDate: "2026-11-14", Funding: pb.OrgLLMFunding(4).Enum(),
	}))
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
}

// Costs are labelled estimates of the org's own keys only, and completeness
// never claims COMPLETE.
func TestGetOrgLLMUsage_EstimateIsLabelledAndNeverComplete(t *testing.T) {
	e := newEnv(t)
	e.seed()
	// The org's key is named by its label; a deleted account keeps none.
	_, err := e.repo.ORM().Bun().NewRaw(`INSERT INTO provider_accounts (id, user_id, provider, auth_kind, identity_key, label, priority, secret_sealed, owner_kind, org_id, org_access)
		VALUES ('acct-acme-key', 'org:org-a', 'anthropic', 'api_key', 'fp-1', 'Acme prod', 0, '\\x00', 'org', 'org-a', 'members')`).Exec(context.Background())
	require.NoError(t, err)
	got, err := e.read(e.asPerson("ada"), "org-a", pb.OrgLLMUsageGroupBy_ORG_LLM_USAGE_GROUP_BY_ACCOUNT)
	require.NoError(t, err)

	assert.Equal(t, pb.LLMUsageCompleteness_LLM_USAGE_COMPLETENESS_PARTIAL, got.GetCompleteness())
	assert.NotEqual(t, pb.LLMUsageCompleteness_LLM_USAGE_COMPLETENESS_COMPLETE, got.GetCompleteness())
	assert.Contains(t, got.GetNote(), "never included")

	est := got.GetEstimate()
	assert.True(t, est.GetIsEstimate())
	assert.Equal(t, pb.OrgLLMCostEstimateStatus_ORG_LLM_COST_ESTIMATE_STATUS_AVAILABLE, est.GetStatus())
	assert.Contains(t, est.GetLabel(), "Estimate")
	assert.Contains(t, est.GetLabel(), "provider bill is authoritative")
	assert.Equal(t, "LiteLLM's model catalog", est.GetPriceSource())
	assert.Equal(t, now.Add(-time.Hour).Format(time.RFC3339), est.GetPricesAsOf())
	assert.Equal(t, []string{"openai/mystery-1"}, est.GetUnpricedModels())

	// Only org-key models are priced: the seat and the wallet are never sent.
	require.Len(t, e.prices.asked, 1)
	assert.ElementsMatch(t, []controlplane.ModelRef{{Provider: "anthropic", Model: "claude-sonnet-4-5"}, {Provider: "openai", Model: "mystery-1"}}, e.prices.asked[0])

	by := map[string]*pb.OrgLLMUsageBucket{}
	for _, b := range got.GetBuckets() {
		by[b.GetKey()] = b
	}
	// 1M in at $3/M + 100k out at $15/M = $4.50.
	assert.Equal(t, "Acme prod", by["acct-acme-key"].GetLabel())
	assert.Empty(t, by["acct-acme-openai"].GetLabel())
	key := by["acct-acme-key"].GetEstimatedListCost()
	require.NotNil(t, key.UsdNanos)
	assert.Equal(t, int64(4_500_000_000), key.GetUsdNanos())
	assert.Equal(t, int64(1), key.GetPricedCalls())
	// An unpriced model is unknown, never $0.
	unpriced := by["acct-acme-openai"].GetEstimatedListCost()
	assert.Nil(t, unpriced.UsdNanos)
	assert.Equal(t, int64(1), unpriced.GetUnpricedCalls())
	// A seat and Reliant credits carry no estimate.
	assert.Nil(t, by["acct-bob-seat"].GetEstimatedListCost().UsdNanos)
	assert.Nil(t, by["acct-carl-managed"].GetEstimatedListCost().UsdNanos)
	assert.Equal(t, int64(4_500_000_000), got.GetTotals().GetEstimatedListCost().GetUsdNanos())
}

// No price source, or one that fails, shows the counts and no cost: never a
// zero cost.
func TestGetOrgLLMUsage_PricesUnavailableShowNoCost(t *testing.T) {
	e := newEnv(t)
	e.seed()
	e.prices.err = errors.New("control plane down")
	got, err := e.read(e.asPerson("ada"), "org-a", pb.OrgLLMUsageGroupBy_ORG_LLM_USAGE_GROUP_BY_DAY)
	require.NoError(t, err)
	assert.Equal(t, pb.OrgLLMCostEstimateStatus_ORG_LLM_COST_ESTIMATE_STATUS_UNAVAILABLE, got.GetEstimate().GetStatus())
	assert.True(t, got.GetEstimate().GetIsEstimate())
	assert.Nil(t, got.GetTotals().GetEstimatedListCost().UsdNanos)
	assert.Equal(t, int64(2), got.GetTotals().GetEstimatedListCost().GetUnpricedCalls())
	assert.Equal(t, int64(4), got.GetTotals().GetCalls())
}

// Org A's admin gets nothing of org B: refused as a non-member, with no
// figure, whichever org's data exists.
func TestGetOrgLLMUsage_CrossTenantGetsNothing(t *testing.T) {
	e := newEnv(t)
	e.seed()
	got, err := e.read(e.asPerson("ada"), "org-b", pb.OrgLLMUsageGroupBy_ORG_LLM_USAGE_GROUP_BY_DAY)
	require.Error(t, err)
	assert.Nil(t, got)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Equal(t, orgs.ReasonNotOrgMember, reason(err))

	// And org B's admin reads only org B.
	own, err := e.read(e.asPerson("gus"), "org-b", pb.OrgLLMUsageGroupBy_ORG_LLM_USAGE_GROUP_BY_MEMBER)
	require.NoError(t, err)
	assert.Equal(t, []string{"gus"}, keys(own.GetBuckets()))
	assert.Equal(t, int64(4_000_000), own.GetTotals().GetInputTokens())
}

// A member without billing:read is refused, and a grant revoked at
// control-plane stops the very next read (it is asked fresh, never cached).
func TestGetOrgLLMUsage_DeniedWithoutBillingRead(t *testing.T) {
	e := newEnv(t)
	e.seed()
	_, err := e.read(e.asPerson("bob"), "org-a", pb.OrgLLMUsageGroupBy_ORG_LLM_USAGE_GROUP_BY_DAY)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Equal(t, org_usage.ReasonMissingScope, reason(err))

	ctx := e.asPerson("ada")
	_, err = e.read(ctx, "org-a", pb.OrgLLMUsageGroupBy_ORG_LLM_USAGE_GROUP_BY_DAY)
	require.NoError(t, err)
	e.dir.set("org-a", "ada") // revoked
	_, err = e.read(ctx, "org-a", pb.OrgLLMUsageGroupBy_ORG_LLM_USAGE_GROUP_BY_DAY)
	assert.Equal(t, org_usage.ReasonMissingScope, reason(err), "a revoked grant was served from a cache")

	e.dir.err = errors.New("control plane down")
	_, err = e.read(ctx, "org-a", pb.OrgLLMUsageGroupBy_ORG_LLM_USAGE_GROUP_BY_DAY)
	assert.Equal(t, connect.CodeUnavailable, connect.CodeOf(err), "an outage must fail closed")
}

// Software credentials: an rlat_ token needs billing:read among its own
// scopes (and its user's grant), so the CLI login (reliant:api alone) is
// refused; any other software credential is too.
func TestGetOrgLLMUsage_Credentials(t *testing.T) {
	e := newEnv(t)
	e.seed()
	base := func() context.Context {
		return authz.WithMembership(auth.WithUser(context.Background(), "ada", "authenticated", ""), orgs.New(e.dir))
	}

	oauthClient := auth.WithCredential(base(), auth.CredentialOAuthClient)
	_, err := e.read(oauthClient, "org-a", pb.OrgLLMUsageGroupBy_ORG_LLM_USAGE_GROUP_BY_DAY)
	assert.Equal(t, auth.ReasonPersonRequired, reason(err))

	apiOnly := auth.WithMachineToken(auth.WithCredential(base(), auth.CredentialAccessToken),
		&fat.Principal{ActingUserID: "ada", OrgID: "org-a", Scopes: scopes(t, fat.ScopeReliantAPI)})
	_, err = e.read(apiOnly, "org-a", pb.OrgLLMUsageGroupBy_ORG_LLM_USAGE_GROUP_BY_DAY)
	assert.Equal(t, fat.ReasonScopeMissing, reason(err))

	billing := auth.WithMachineToken(auth.WithCredential(base(), auth.CredentialAccessToken),
		&fat.Principal{ActingUserID: "ada", OrgID: "org-a", Scopes: scopes(t, fat.ScopeReliantAPI, fat.ScopeBillingRead)})
	got, err := e.read(billing, "org-a", pb.OrgLLMUsageGroupBy_ORG_LLM_USAGE_GROUP_BY_DAY)
	require.NoError(t, err)
	assert.Equal(t, int64(4), got.GetTotals().GetCalls())

	// The same token is bound to org-a: it reads nothing of another org.
	e.dir.set("org-b", "ada", string(fat.ScopeBillingRead))
	_, err = e.read(billing, "org-b", pb.OrgLLMUsageGroupBy_ORG_LLM_USAGE_GROUP_BY_DAY)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
}
