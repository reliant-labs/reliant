package org_usage

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/orgs"
)

// MaxRangeDays bounds one GetOrgLLMUsage range, inclusive of both ends.
const MaxRangeDays = 400

// ReasonMissingScope is the reason of a member refused because they do not
// hold the org grant (billing:read) the read needs.
const ReasonMissingScope = "missing_scope"

// fundingOrgKey is the funding class whose calls are priced: the org's own
// API keys, billed to the org by the provider.
const fundingOrgKey = "org_key"

// EstimateLabel is shown beside every estimated cost.
const EstimateLabel = "Estimate at list prices from LiteLLM's model catalog, for the organization's own API keys. " +
	"Your provider bills these calls directly; your provider bill is authoritative."

// PriceSource names where list prices come from.
const PriceSource = "LiteLLM's model catalog"

// Note is shown with every response: what is and is not in it.
const Note = "Shows only what the organization pays for: its own API keys, seats it pays for, and Reliant credits it pays for. " +
	"Members' own accounts are never included, even in the organization's chats, so this is not all of the AI use in the organization. " +
	"Costs are estimates. Today's figures are still accumulating."

const dateLayout = "2006-01-02"

var fundingNames = map[pb.OrgLLMFunding]string{
	pb.OrgLLMFunding_ORG_LLM_FUNDING_ORG_KEY:    "org_key",
	pb.OrgLLMFunding_ORG_LLM_FUNDING_ORG_SEAT:   "org_seat",
	pb.OrgLLMFunding_ORG_LLM_FUNDING_ORG_WALLET: "org_wallet",
}

var groupKeys = map[pb.OrgLLMUsageGroupBy]func(*db.LLMOrgUsageDay) string{
	pb.OrgLLMUsageGroupBy_ORG_LLM_USAGE_GROUP_BY_DAY:      func(r *db.LLMOrgUsageDay) string { return r.Day.UTC().Format(dateLayout) },
	pb.OrgLLMUsageGroupBy_ORG_LLM_USAGE_GROUP_BY_MEMBER:   func(r *db.LLMOrgUsageDay) string { return r.UserID },
	pb.OrgLLMUsageGroupBy_ORG_LLM_USAGE_GROUP_BY_ACCOUNT:  func(r *db.LLMOrgUsageDay) string { return r.AccountID },
	pb.OrgLLMUsageGroupBy_ORG_LLM_USAGE_GROUP_BY_MODEL:    func(r *db.LLMOrgUsageDay) string { return r.Model },
	pb.OrgLLMUsageGroupBy_ORG_LLM_USAGE_GROUP_BY_FUNDING:  func(r *db.LLMOrgUsageDay) string { return r.Funding },
	pb.OrgLLMUsageGroupBy_ORG_LLM_USAGE_GROUP_BY_PROVIDER: func(r *db.LLMOrgUsageDay) string { return r.Provider },
}

// GetOrgLLMUsage sums the org's org-funded rollup rows over a UTC date range
// into buckets, with an estimated list cost for the org's own keys.
func (s *Service) GetOrgLLMUsage(
	ctx context.Context,
	req *connect.Request[pb.GetOrgLLMUsageRequest],
) (*connect.Response[pb.GetOrgLLMUsageResponse], error) {
	msg := req.Msg
	orgID := msg.GetOrgId()
	if err := authorize(ctx, orgID); err != nil {
		return nil, err
	}
	start, end, err := parseRange(msg.GetStartDate(), msg.GetEndDate())
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	groupBy := msg.GetGroupBy()
	if groupBy == pb.OrgLLMUsageGroupBy_ORG_LLM_USAGE_GROUP_BY_UNSPECIFIED {
		groupBy = pb.OrgLLMUsageGroupBy_ORG_LLM_USAGE_GROUP_BY_DAY
	}
	keyOf, ok := groupKeys[groupBy]
	if !ok {
		return nil, svcerr.Wrap(svcerr.InvalidArgument(fmt.Sprintf("unknown group_by %d", groupBy)))
	}
	var funding string
	if msg.Funding != nil {
		if funding, ok = fundingNames[msg.GetFunding()]; !ok {
			return nil, svcerr.Wrap(svcerr.InvalidArgument("funding must be org_key, org_seat or org_wallet when set"))
		}
	}

	rows, err := s.deps.Store.ListLLMOrgUsageDaily(ctx, orgID, start, end)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}

	agg := newAggregate()
	for _, row := range rows {
		// The rollup is the org's alone (its key leads with org_id), and it
		// holds only org-funded calls; both are re-asserted here, so a
		// store that ever answered otherwise is ignored rather than shown.
		if row.OrgID != orgID || fundingNames[fundingOf(row.Funding)] == "" {
			continue
		}
		if (msg.UserId != nil && row.UserID != msg.GetUserId()) ||
			(msg.AccountId != nil && row.AccountID != msg.GetAccountId()) ||
			(msg.Model != nil && row.Model != msg.GetModel()) ||
			(msg.Funding != nil && row.Funding != funding) {
			continue
		}
		agg.add(keyOf(row), row)
	}

	estimate := s.estimate(ctx, agg)
	buckets := agg.buckets(groupBy)
	if groupBy == pb.OrgLLMUsageGroupBy_ORG_LLM_USAGE_GROUP_BY_ACCOUNT {
		s.labelAccounts(ctx, buckets)
	}

	return connect.NewResponse(&pb.GetOrgLLMUsageResponse{
		OrgId:   orgID,
		GroupBy: groupBy,
		Buckets: buckets,
		Totals:  agg.total(),
		// Never complete: members' own accounts are out of scope by design,
		// and a list price is not a charge.
		Completeness: pb.LLMUsageCompleteness_LLM_USAGE_COMPLETENESS_PARTIAL,
		AsOf:         s.now().Format(time.RFC3339),
		Note:         Note,
		Estimate:     estimate,
	}), nil
}

// authorize admits a member of orgID who holds billing:read there, read from
// control-plane now. A person's credential is enough; a machine credential
// (an rlat_ access token) must also carry billing:read itself. Any other
// software credential (the CLI login, an OAuth client) is refused.
func authorize(ctx context.Context, orgID string) error {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return svcerr.Wrap(svcerr.Unauthenticated("user ID not found in context"))
	}
	if p, isMachine := auth.MachineTokenFromContext(ctx); isMachine {
		if err := p.Require(fat.ScopeBillingRead); err != nil {
			return svcerr.Wrap(err)
		}
	} else if err := auth.RequirePerson(ctx, "read the organization's AI usage"); err != nil {
		return svcerr.Wrap(err)
	}
	err := authz.RequireOrgScope(ctx, userID, orgID, string(fat.ScopeBillingRead))
	switch {
	case err == nil:
		return nil
	case errors.Is(err, authz.ErrMissingOrgScope):
		return svcerr.Wrap(svcerr.WithReason(svcerr.PermissionDenied(
			"Reading the organization's AI usage needs the billing:read permission in it. Ask an owner of the organization to grant it."),
			ReasonMissingScope))
	case errors.Is(err, authz.ErrMembershipUnavailable):
		return svcerr.Wrap(svcerr.WithCause(svcerr.Unavailable("the organization's membership could not be checked; try again"), err))
	default:
		// A non-member, another org's admin, or a credential bound to
		// another org: nothing about the org is told.
		return svcerr.Wrap(svcerr.WithReason(svcerr.PermissionDenied("you are not a member of this organization"), orgs.ReasonNotOrgMember))
	}
}

func fundingOf(name string) pb.OrgLLMFunding {
	for k, v := range fundingNames {
		if v == name {
			return k
		}
	}
	return pb.OrgLLMFunding_ORG_LLM_FUNDING_UNSPECIFIED
}

func parseRange(startDate, endDate string) (start, end time.Time, err error) {
	if start, err = time.Parse(dateLayout, startDate); err != nil {
		return start, end, svcerr.InvalidArgument("start_date must be a UTC date, YYYY-MM-DD")
	}
	if end, err = time.Parse(dateLayout, endDate); err != nil {
		return start, end, svcerr.InvalidArgument("end_date must be a UTC date, YYYY-MM-DD")
	}
	if end.Before(start) {
		return start, end, svcerr.InvalidArgument("end_date is before start_date")
	}
	if days := int(end.Sub(start)/(24*time.Hour)) + 1; days > MaxRangeDays {
		return start, end, svcerr.InvalidArgument(fmt.Sprintf("range is %d days; the most one request covers is %d", days, MaxRangeDays))
	}
	return start, end, nil
}

// modelKey is one model as a usage row records it.
type modelKey struct{ provider, model string }

// tokens are one model's org-key tokens in one bucket.
type tokens struct{ calls, input, output, cacheRead, cacheWrite int64 }

// aggregate sums rows per bucket key, and per bucket the org-key tokens of
// each model (what is priced).
type aggregate struct {
	byKey   map[string]*pb.OrgLLMUsageBucket
	orgKeys map[string]map[modelKey]*tokens
	totals  *pb.OrgLLMUsageBucket
	// totalKeys is orgKeys over every bucket.
	totalKeys map[modelKey]*tokens
}

func newAggregate() *aggregate {
	return &aggregate{byKey: map[string]*pb.OrgLLMUsageBucket{}, orgKeys: map[string]map[modelKey]*tokens{},
		totals: &pb.OrgLLMUsageBucket{}, totalKeys: map[modelKey]*tokens{}}
}

func (a *aggregate) add(key string, r *db.LLMOrgUsageDay) {
	b := a.byKey[key]
	if b == nil {
		b = &pb.OrgLLMUsageBucket{Key: key}
		a.byKey[key] = b
	}
	addRow(b, r)
	addRow(a.totals, r)
	if r.Funding != fundingOrgKey {
		return
	}
	mk := modelKey{r.Provider, r.Model}
	if a.orgKeys[key] == nil {
		a.orgKeys[key] = map[modelKey]*tokens{}
	}
	for _, m := range []map[modelKey]*tokens{a.orgKeys[key], a.totalKeys} {
		t := m[mk]
		if t == nil {
			t = &tokens{}
			m[mk] = t
		}
		t.calls += r.Calls
		t.input += r.InputTokens
		t.output += r.OutputTokens
		t.cacheRead += r.CacheReadTokens
		t.cacheWrite += r.CacheWriteTokens
	}
}

func addRow(b *pb.OrgLLMUsageBucket, r *db.LLMOrgUsageDay) {
	b.Calls += r.Calls
	b.ErrorCalls += r.ErrorCalls
	b.InputTokens += r.InputTokens
	b.OutputTokens += r.OutputTokens
	b.CacheReadTokens += r.CacheReadTokens
	b.CacheWriteTokens += r.CacheWriteTokens
	b.LatencyMsTotal += r.LatencyMsTotal
}

func finish(b *pb.OrgLLMUsageBucket) {
	if b.Calls > 0 {
		b.AvgLatencyMs = float64(b.LatencyMsTotal) / float64(b.Calls)
	}
}

// buckets are the groups, sorted by day ascending or else by tokens
// descending, then calls descending, then key.
func (a *aggregate) buckets(groupBy pb.OrgLLMUsageGroupBy) []*pb.OrgLLMUsageBucket {
	out := make([]*pb.OrgLLMUsageBucket, 0, len(a.byKey))
	for _, b := range a.byKey {
		finish(b)
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool {
		x, y := out[i], out[j]
		if groupBy != pb.OrgLLMUsageGroupBy_ORG_LLM_USAGE_GROUP_BY_DAY {
			if xt, yt := x.InputTokens+x.OutputTokens, y.InputTokens+y.OutputTokens; xt != yt {
				return xt > yt
			}
			if x.Calls != y.Calls {
				return x.Calls > y.Calls
			}
		}
		return x.Key < y.Key
	})
	return out
}

func (a *aggregate) total() *pb.OrgLLMUsageBucket {
	finish(a.totals)
	return a.totals
}

// estimate prices every bucket's org-key tokens at list price, and describes
// the estimates. A model with no price, or every model when no price can be
// read, is counted as unpriced: its cost is unknown, never zero.
func (s *Service) estimate(ctx context.Context, a *aggregate) *pb.OrgLLMCostEstimate {
	info := &pb.OrgLLMCostEstimate{IsEstimate: true, Label: EstimateLabel, PriceSource: PriceSource}
	if len(a.totalKeys) == 0 {
		info.Status = pb.OrgLLMCostEstimateStatus_ORG_LLM_COST_ESTIMATE_STATUS_NONE
		return info
	}
	refs := make([]controlplane.ModelRef, 0, len(a.totalKeys))
	for mk := range a.totalKeys {
		refs = append(refs, controlplane.ModelRef{Provider: mk.provider, Model: mk.model})
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Provider != refs[j].Provider {
			return refs[i].Provider < refs[j].Provider
		}
		return refs[i].Model < refs[j].Model
	})
	if len(refs) > controlplane.MaxListPriceModels {
		refs = refs[:controlplane.MaxListPriceModels]
	}
	prices := map[modelKey]controlplane.ModelListPrice{}
	info.Status = pb.OrgLLMCostEstimateStatus_ORG_LLM_COST_ESTIMATE_STATUS_UNAVAILABLE
	if s.deps.Prices != nil {
		got, asOf, err := s.deps.Prices.GetModelListPrices(ctx, refs)
		if err != nil {
			s.deps.Logger.WarnContext(ctx, "org usage: list prices could not be read; showing usage without cost estimates", "error", err)
		} else {
			info.Status = pb.OrgLLMCostEstimateStatus_ORG_LLM_COST_ESTIMATE_STATUS_AVAILABLE
			if !asOf.IsZero() {
				info.PricesAsOf = asOf.UTC().Format(time.RFC3339)
			}
			for _, p := range got {
				prices[modelKey{p.Ref.Provider, p.Ref.Model}] = p
			}
		}
	}
	if info.Status == pb.OrgLLMCostEstimateStatus_ORG_LLM_COST_ESTIMATE_STATUS_AVAILABLE {
		for mk := range a.totalKeys {
			if !prices[mk].Priced {
				name := mk.model
				if mk.provider != "" {
					name = mk.provider + "/" + mk.model
				}
				info.UnpricedModels = append(info.UnpricedModels, name)
			}
		}
		sort.Strings(info.UnpricedModels)
	}
	for key, b := range a.byKey {
		b.EstimatedListCost = priceTokens(a.orgKeys[key], prices)
	}
	a.totals.EstimatedListCost = priceTokens(a.totalKeys, prices)
	return info
}

// priceTokens is the list cost of one bucket's org-key tokens.
func priceTokens(byModel map[modelKey]*tokens, prices map[modelKey]controlplane.ModelListPrice) *pb.OrgLLMListCostEstimate {
	out := &pb.OrgLLMListCostEstimate{}
	var nanos int64
	for mk, t := range byModel {
		p, ok := prices[mk]
		cost, valid := listCost(p, t)
		if !ok || !p.Priced || !valid {
			out.UnpricedCalls += t.calls
			continue
		}
		nanos += cost
		out.PricedCalls += t.calls
	}
	if out.PricedCalls > 0 {
		out.UsdNanos = &nanos
	}
	return out
}

// listCost is control-plane's formula (LLMModelListPrice): each token class
// at its rate per million tokens, rounded to the nearest nano. ok is false
// for a negative rate or a cost too large for int64 nanos: the cost is then
// unknown, never zero and never a wrapped figure.
func listCost(p controlplane.ModelListPrice, t *tokens) (nanos int64, ok bool) {
	if p.InputNanosPerMTok < 0 || p.OutputNanosPerMTok < 0 || p.CacheReadNanosPerMTok < 0 || p.CacheWriteNanosPerMTok < 0 {
		return 0, false
	}
	total := float64(t.input)*float64(p.InputNanosPerMTok) +
		float64(t.output)*float64(p.OutputNanosPerMTok) +
		float64(t.cacheRead)*float64(p.CacheReadNanosPerMTok) +
		float64(t.cacheWrite)*float64(p.CacheWriteNanosPerMTok)
	rounded := math.Round(total / 1e6)
	if math.IsNaN(rounded) || rounded < 0 || rounded >= float64(math.MaxInt64) {
		return 0, false
	}
	return int64(rounded), true
}

// labelAccounts names each account bucket by the account's label (or its
// provider when it has none). A deleted account keeps an empty label.
func (s *Service) labelAccounts(ctx context.Context, buckets []*pb.OrgLLMUsageBucket) {
	ids := make([]string, 0, len(buckets))
	for _, b := range buckets {
		if b.Key != "" {
			ids = append(ids, b.Key)
		}
	}
	names, err := s.deps.Store.ProviderAccountNames(ctx, ids)
	if err != nil {
		s.deps.Logger.WarnContext(ctx, "org usage: account names could not be read; showing ids", "error", err)
		return
	}
	for _, b := range buckets {
		if n, ok := names[b.Key]; ok {
			b.Label = n.Label
			if b.Label == "" {
				b.Label = n.Provider
			}
		}
	}
}
