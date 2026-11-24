package insights

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/orgs"
	"github.com/reliant-labs/reliant/internal/store"
)

// MaxRangeDays bounds one request's range, inclusive of both ends.
const MaxRangeDays = 400

// MinContributors is the small-group floor (agent-analytics.md §3.6, D5): a
// team bucket fewer members contributed to is merged into "other".
const MinContributors = 5

// TeamNote is shown with every team response.
const TeamNote = "Totals for the organization's agent work. No one is named: a group fewer than 5 members contributed to is merged into Other. " +
	"Cost counts only what the organization pays for; calls on members' own accounts count as calls, with no cost shown. " +
	"Reported cost is what the Reliant gateway charged; estimated cost is a list-price estimate; the two are never added together. " +
	"Today's figures are still accumulating."

// MemberNote is shown with a member's own numbers and an admin's drill-down.
const MemberNote = "One member's agent work in the organization. Cost counts only what the organization pays for; " +
	"calls on the member's own accounts count as calls, with no cost shown. " +
	"Reported cost is what the Reliant gateway charged; estimated cost is a list-price estimate; the two are never added together. " +
	"Today's figures are still accumulating."

const dateLayout = "2006-01-02"

var groupDims = map[pb.InsightsGroupBy]db.InsightsDim{
	pb.InsightsGroupBy_INSIGHTS_GROUP_BY_DAY:       store.InsightsDimDay,
	pb.InsightsGroupBy_INSIGHTS_GROUP_BY_PROJECT:   store.InsightsDimProject,
	pb.InsightsGroupBy_INSIGHTS_GROUP_BY_WORKFLOW:  store.InsightsDimWorkflow,
	pb.InsightsGroupBy_INSIGHTS_GROUP_BY_PRESET:    store.InsightsDimPreset,
	pb.InsightsGroupBy_INSIGHTS_GROUP_BY_MODEL:     store.InsightsDimModel,
	pb.InsightsGroupBy_INSIGHTS_GROUP_BY_PROVIDER:  store.InsightsDimProvider,
	pb.InsightsGroupBy_INSIGHTS_GROUP_BY_PURPOSE:   store.InsightsDimPurpose,
	pb.InsightsGroupBy_INSIGHTS_GROUP_BY_TOOL:      store.InsightsDimTool,
	pb.InsightsGroupBy_INSIGHTS_GROUP_BY_TOOL_KIND: store.InsightsDimToolKind,
}

// query is a validated request: the org, the days, and the dimension.
type query struct {
	orgID      string
	start, end time.Time
	groupBy    pb.InsightsGroupBy
	dim        db.InsightsDim
}

func parseQuery(orgID, startDate, endDate string, groupBy pb.InsightsGroupBy) (query, error) {
	q := query{orgID: orgID, groupBy: groupBy}
	if orgID == "" {
		return q, svcerr.InvalidArgument("org_id is required")
	}
	var err error
	if q.start, err = time.Parse(dateLayout, startDate); err != nil {
		return q, svcerr.InvalidArgument("start_date must be a UTC date, YYYY-MM-DD")
	}
	if q.end, err = time.Parse(dateLayout, endDate); err != nil {
		return q, svcerr.InvalidArgument("end_date must be a UTC date, YYYY-MM-DD")
	}
	if q.end.Before(q.start) {
		return q, svcerr.InvalidArgument("end_date is before start_date")
	}
	if days := int(q.end.Sub(q.start)/(24*time.Hour)) + 1; days > MaxRangeDays {
		return q, svcerr.InvalidArgument(fmt.Sprintf("range is %d days; the most one request covers is %d", days, MaxRangeDays))
	}
	if q.groupBy == pb.InsightsGroupBy_INSIGHTS_GROUP_BY_UNSPECIFIED {
		q.groupBy = pb.InsightsGroupBy_INSIGHTS_GROUP_BY_DAY
	}
	var ok bool
	if q.dim, ok = groupDims[q.groupBy]; !ok {
		return q, svcerr.InvalidArgument(fmt.Sprintf("unknown group_by %d", q.groupBy))
	}
	return q, nil
}

func callerID(ctx context.Context) (string, error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return "", svcerr.Wrap(svcerr.Unauthenticated("user ID not found in context"))
	}
	return userID, nil
}

// requireMember admits a live member of orgID, by their role read from
// control-plane now (authz.RequireOrgMemberFresh). Anyone else is refused,
// the same way whether or not the org exists.
func requireMember(ctx context.Context, userID, orgID string) error {
	switch err := authz.RequireOrgMemberFresh(ctx, userID, orgID); {
	case err == nil:
		return nil
	case errors.Is(err, authz.ErrMembershipUnavailable):
		return svcerr.Wrap(orgs.LookupFailed(ctx, "could not confirm your membership of this organization; try again", err))
	default:
		// A non-member, a role control-plane could not name, or a
		// credential bound to another org.
		return svcerr.Wrap(svcerr.WithCause(orgs.NotMember(orgID), err))
	}
}

// --- Model calls ---

func modelStats(r db.InsightsModelRow) *pb.InsightsModelStats {
	return &pb.InsightsModelStats{
		Calls:            r.Calls,
		RetriedCalls:     r.RetriedCalls,
		ErrorCalls:       r.ErrorCalls,
		CancelledCalls:   r.CancelledCalls,
		InputTokens:      r.InputTokens,
		OutputTokens:     r.OutputTokens,
		CacheReadTokens:  r.CacheReadTokens,
		CacheWriteTokens: r.CacheWriteTokens,
		Latency:          latency(r.LatencyMsTotal, r.LatencyMsHist),
		TimeToFirstToken: latency(r.TTFTMsTotal, r.TTFTMsHist),
		Cost:             cost(r),
	}
}

// cost is a row's org-funded cost. A figure is absent, never zero, when no
// call stands behind it: a bucket of personally funded calls has a blank
// cost, which the page labels "personally funded".
func cost(r db.InsightsModelRow) *pb.InsightsCost {
	c := &pb.InsightsCost{
		OrgFundedCalls:         r.OrgFundedCalls,
		PersonallyFundedCalls:  r.PersonallyFundedCalls,
		UnrecordedFundingCalls: r.UnrecordedFundingCalls,
		ReportedCalls:          r.ReportedCalls,
		EstimatedCalls:         r.EstimatedCalls,
		UnpricedCalls:          r.UnpricedCalls,
	}
	if r.ReportedCalls > 0 {
		v := r.ReportedCostUSDNanos
		c.ReportedUsdNanos = &v
	}
	if r.EstimatedCalls > 0 {
		v := r.EstimatedCostUSDNanos
		c.EstimatedUsdNanos = &v
	}
	return c
}

// addModel adds b into a, histograms included.
func addModel(a *db.InsightsModelRow, b db.InsightsModelRow) {
	a.Calls += b.Calls
	a.RetriedCalls += b.RetriedCalls
	a.ErrorCalls += b.ErrorCalls
	a.CancelledCalls += b.CancelledCalls
	a.InputTokens += b.InputTokens
	a.OutputTokens += b.OutputTokens
	a.CacheReadTokens += b.CacheReadTokens
	a.CacheWriteTokens += b.CacheWriteTokens
	a.LatencyMsTotal += b.LatencyMsTotal
	a.LatencyMsHist = addHist(a.LatencyMsHist, b.LatencyMsHist)
	a.TTFTMsTotal += b.TTFTMsTotal
	a.TTFTMsHist = addHist(a.TTFTMsHist, b.TTFTMsHist)
	a.OrgFundedCalls += b.OrgFundedCalls
	a.PersonallyFundedCalls += b.PersonallyFundedCalls
	a.UnrecordedFundingCalls += b.UnrecordedFundingCalls
	a.ReportedCalls += b.ReportedCalls
	a.ReportedCostUSDNanos += b.ReportedCostUSDNanos
	a.EstimatedCalls += b.EstimatedCalls
	a.EstimatedCostUSDNanos += b.EstimatedCostUSDNanos
	a.UnpricedCalls += b.UnpricedCalls
}

// modelSection builds a section from rows; shown, when not nil, names the
// keys shown on their own, and every other row is merged into other.
func modelSection(rows []db.InsightsModelRow, shown map[string]bool, byDay bool) *pb.InsightsModelSection {
	sec := &pb.InsightsModelSection{}
	var total, other db.InsightsModelRow
	weights := map[string]int64{}
	for _, r := range rows {
		addModel(&total, r)
		if shown != nil && !shown[r.Key] {
			addModel(&other, r)
			sec.OtherGroups++
			continue
		}
		sec.Buckets = append(sec.Buckets, &pb.InsightsModelBucket{Key: r.Key, Stats: modelStats(r)})
		weights[r.Key] = r.Calls
	}
	sort.SliceStable(sec.Buckets, func(i, j int) bool {
		return bucketLess(byDay, sec.Buckets[i].Key, sec.Buckets[j].Key, weights)
	})
	if sec.OtherGroups > 0 {
		sec.Other = modelStats(other)
	}
	sec.Totals = modelStats(total)
	return sec
}

// --- Tool calls ---

func toolStats(r db.InsightsToolRow) *pb.InsightsToolStats {
	return &pb.InsightsToolStats{
		Calls:          r.Calls,
		RetriedCalls:   r.RetriedCalls,
		ErrorCalls:     r.ErrorCalls,
		CancelledCalls: r.CancelledCalls,
		Duration:       latency(r.DurationMsTotal, r.DurationMsHist),
	}
}

func addTool(a *db.InsightsToolRow, b db.InsightsToolRow) {
	a.Calls += b.Calls
	a.RetriedCalls += b.RetriedCalls
	a.ErrorCalls += b.ErrorCalls
	a.CancelledCalls += b.CancelledCalls
	a.DurationMsTotal += b.DurationMsTotal
	a.DurationMsHist = addHist(a.DurationMsHist, b.DurationMsHist)
}

func toolSection(rows []db.InsightsToolRow, shown map[string]bool, byDay bool) *pb.InsightsToolSection {
	sec := &pb.InsightsToolSection{}
	var total, other db.InsightsToolRow
	weights := map[string]int64{}
	for _, r := range rows {
		addTool(&total, r)
		if shown != nil && !shown[r.Key] {
			addTool(&other, r)
			sec.OtherGroups++
			continue
		}
		sec.Buckets = append(sec.Buckets, &pb.InsightsToolBucket{Key: r.Key, Stats: toolStats(r)})
		weights[r.Key] = r.Calls
	}
	sort.SliceStable(sec.Buckets, func(i, j int) bool {
		return bucketLess(byDay, sec.Buckets[i].Key, sec.Buckets[j].Key, weights)
	})
	if sec.OtherGroups > 0 {
		sec.Other = toolStats(other)
	}
	sec.Totals = toolStats(total)
	return sec
}

// bucketLess orders buckets: by day oldest first, otherwise most calls first,
// then key.
func bucketLess(byDay bool, a, b string, calls map[string]int64) bool {
	if !byDay && calls[a] != calls[b] {
		return calls[a] > calls[b]
	}
	return a < b
}

// --- Latency ---

func addHist(a, b []int64) []int64 {
	if len(a) < len(b) {
		a = append(a, make([]int64, len(b)-len(a))...)
	}
	for i, v := range b {
		a[i] += v
	}
	return a
}

// latency reads a duration distribution from a total and its histogram. The
// histogram counts the calls with a measured duration.
func latency(totalMs int64, hist []int64) *pb.InsightsLatency {
	var n int64
	for _, v := range hist {
		n += v
	}
	l := &pb.InsightsLatency{Count: n}
	if n == 0 {
		return l
	}
	l.AvgMs = float64(totalMs) / float64(n)
	p50 := percentile(hist, n, 0.5)
	p95 := percentile(hist, n, 0.95)
	l.P50Ms, l.P95Ms = &p50, &p95
	return l
}

// percentile is the q-quantile of a histogram of n counts, interpolated
// linearly inside the bucket it falls in. The last bucket has no upper bound,
// so a quantile there is its lower bound.
func percentile(hist []int64, n int64, q float64) float64 {
	rank := q * float64(n)
	var cum int64
	for k, c := range hist {
		if c == 0 {
			continue
		}
		if float64(cum+c) >= rank {
			if k == 0 {
				return 0
			}
			lower, upper := store.InsightsLatencyBucketBounds(k)
			if k >= store.InsightsLatencyBuckets-1 {
				return lower
			}
			return lower + (rank-float64(cum))/float64(c)*(upper-lower)
		}
		cum += c
	}
	lower, _ := store.InsightsLatencyBucketBounds(store.InsightsLatencyBuckets - 1)
	return lower
}

// --- The small-group floor ---

// contributorSets is, per group key, the opaque ordinals of its contributors.
func contributorSets(rows []db.InsightsContributor) map[string]map[int64]struct{} {
	out := map[string]map[int64]struct{}{}
	for _, r := range rows {
		if out[r.Key] == nil {
			out[r.Key] = map[int64]struct{}{}
		}
		out[r.Key][r.Member] = struct{}{}
	}
	return out
}

// floorKeys decides which of keys a team view shows on its own: those at
// least min members contributed to. Every other key is merged into "other".
// "Other" must not single anyone out either, so while it holds fewer than min
// distinct contributors, the shown key with the fewest contributors (then the
// fewest calls, then the greatest key) joins it, until other reaches min or
// holds everything. A key with no counted contributor (its rows outlived
// their members' rows) counts as none: it is merged, and adds no one to
// other.
func floorKeys(keys []string, calls map[string]int64, contributors map[string]map[int64]struct{}, min int) map[string]bool {
	shown := map[string]bool{}
	for _, k := range keys {
		if len(contributors[k]) >= min {
			shown[k] = true
		}
	}
	for {
		other := map[int64]struct{}{}
		merged := 0
		for _, k := range keys {
			if shown[k] {
				continue
			}
			merged++
			for m := range contributors[k] {
				other[m] = struct{}{}
			}
		}
		if merged == 0 || len(other) >= min || len(shown) == 0 {
			return shown
		}
		var pick string
		for k := range shown {
			if pick == "" || fewer(k, pick, calls, contributors) {
				pick = k
			}
		}
		delete(shown, pick)
	}
}

// fewer orders candidates to merge: fewest contributors, then fewest calls,
// then the greatest key.
func fewer(a, b string, calls map[string]int64, contributors map[string]map[int64]struct{}) bool {
	if ca, cb := len(contributors[a]), len(contributors[b]); ca != cb {
		return ca < cb
	}
	if calls[a] != calls[b] {
		return calls[a] < calls[b]
	}
	return a > b
}
