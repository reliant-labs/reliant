package usage

import (
	"context"
	"fmt"
	"sort"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
)

// MaxLLMUsageRangeDays bounds one GetLLMUsage range, inclusive of both ends.
const MaxLLMUsageRangeDays = 400

const dateLayout = "2006-01-02"

// GetLLMUsage sums the caller's rollup rows over a UTC date range into buckets.
// The user is always the authenticated one (a machine credential acts as its
// owner); nothing on the request names another.
func (s *Service) GetLLMUsage(
	ctx context.Context,
	req *connect.Request[pb.GetLLMUsageRequest],
) (*connect.Response[pb.GetLLMUsageResponse], error) {
	userID, ok := auth.GetUserIDFromContext(ctx)
	if !ok || userID == "" {
		return nil, svcerr.Wrap(svcerr.Unauthenticated("user ID not found in context"))
	}
	msg := req.Msg

	start, end, err := parseRange(msg.GetStartDate(), msg.GetEndDate())
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	groupBy := msg.GetGroupBy()
	if groupBy == pb.LLMUsageGroupBy_LLM_USAGE_GROUP_BY_UNSPECIFIED {
		groupBy = pb.LLMUsageGroupBy_LLM_USAGE_GROUP_BY_DAY
	}
	keyOf, ok := groupKeys[groupBy]
	if !ok {
		return nil, svcerr.Wrap(svcerr.InvalidArgument(fmt.Sprintf("unknown group_by %d", groupBy)))
	}
	var authMode string
	if msg.AuthMode != nil {
		if authMode, ok = authModeNames[msg.GetAuthMode()]; !ok {
			return nil, svcerr.Wrap(svcerr.InvalidArgument("auth_mode must be managed, byok, subscription or local when set"))
		}
	}

	var purpose string
	if msg.Purpose != nil {
		if purpose, ok = purposeNames[msg.GetPurpose()]; !ok {
			return nil, svcerr.Wrap(svcerr.InvalidArgument("purpose must be chat, compaction, title or draft_automation when set"))
		}
	}

	now := time.Now().UTC()
	if s.deps.Now != nil {
		now = s.deps.Now().UTC()
	}
	detailFrom := detailAvailableFrom(now)
	rows, err := s.usageRows(ctx, userID, start, end, detailFrom, detailGroupBys[groupBy])
	if err != nil {
		return nil, svcerr.Wrap(err)
	}

	byKey := map[string]*pb.LLMUsageBucket{}
	totals := &pb.LLMUsageBucket{}
	for _, row := range rows {
		if (msg.Provider != nil && row.Provider != msg.GetProvider()) ||
			(msg.Model != nil && row.Model != msg.GetModel()) ||
			(msg.AuthMode != nil && row.AuthMode != authMode) ||
			(msg.Purpose != nil && row.Purpose != purpose) ||
			(msg.OrgId != nil && row.OrgID != msg.GetOrgId()) {
			continue
		}
		key := keyOf(row)
		b := byKey[key]
		if b == nil {
			b = &pb.LLMUsageBucket{Key: key}
			byKey[key] = b
		}
		addRow(b, row)
		addRow(totals, row)
	}
	buckets := make([]*pb.LLMUsageBucket, 0, len(byKey))
	for _, b := range byKey {
		finish(b)
		buckets = append(buckets, b)
	}
	finish(totals)
	sortBuckets(buckets, groupBy)
	if groupBy == pb.LLMUsageGroupBy_LLM_USAGE_GROUP_BY_ACCOUNT {
		s.labelAccounts(ctx, buckets)
	}

	completeness := pb.LLMUsageCompleteness_LLM_USAGE_COMPLETENESS_COMPLETE
	note := "Every call in this range is counted. Today's figures are still accumulating."
	if start.Before(detailFrom) {
		completeness = pb.LLMUsageCompleteness_LLM_USAGE_COMPLETENESS_PARTIAL
		retention := int(db.LLMCallUsageRetention / (24 * time.Hour))
		note = fmt.Sprintf(
			"Individual calls are kept for %d days; days before %s come from the daily totals, which are kept. Those totals are complete, but cannot be broken down further.",
			retention, detailFrom.Format(dateLayout))
		if detailGroupBys[groupBy] {
			note = fmt.Sprintf(
				"Individual calls are kept for %d days, and who paid for a call and which account served it are known only from them: calls before %s are counted under \"not recorded\". The totals are complete.",
				retention, detailFrom.Format(dateLayout))
		}
	}

	return connect.NewResponse(&pb.GetLLMUsageResponse{
		GroupBy:             groupBy,
		Buckets:             buckets,
		Totals:              totals,
		Completeness:        completeness,
		AsOf:                now.Format(time.RFC3339),
		DetailAvailableFrom: detailFrom.Format(dateLayout),
		Note:                note,
	}), nil
}

// detailAvailableFrom is the first UTC day whose per-call rows are all still
// stored: the retention job deletes rows older than now minus the retention,
// so the day holding that cutoff is already partly gone.
func detailAvailableFrom(now time.Time) time.Time {
	cutoff := now.Add(-db.LLMCallUsageRetention).UTC()
	day := time.Date(cutoff.Year(), cutoff.Month(), cutoff.Day(), 0, 0, 0, 0, time.UTC)
	if cutoff.Equal(day) {
		return day
	}
	return day.AddDate(0, 0, 1)
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
	if days := int(end.Sub(start)/(24*time.Hour)) + 1; days > MaxLLMUsageRangeDays {
		return start, end, svcerr.InvalidArgument(fmt.Sprintf("range is %d days; the most one request covers is %d", days, MaxLLMUsageRangeDays))
	}
	return start, end, nil
}

// usageRow is one summed row of either source: the daily rollup, which has
// no funding or account, or the per-call breakdown, which has both.
type usageRow struct {
	Day                                               time.Time
	OrgID, Provider, Model, AuthMode, Purpose         string
	Funding, AccountID                                string
	Calls, ErrorCalls, InputTokens, OutputTokens      int64
	CacheReadTokens, CacheWriteTokens, LatencyMsTotal int64
}

// detailGroupBys are the dimensions only the per-call rows record.
var detailGroupBys = map[pb.LLMUsageGroupBy]bool{
	pb.LLMUsageGroupBy_LLM_USAGE_GROUP_BY_FUNDING: true,
	pb.LLMUsageGroupBy_LLM_USAGE_GROUP_BY_ACCOUNT: true,
}

// usageRows reads the range. Without a detail dimension it is the rollup, as
// it always was. With one, the days whose calls are still kept (from
// detailFrom) come from the per-call rows, and older days from the rollup
// with no funding or account (the "" bucket), so the totals never change
// with the grouping.
func (s *Service) usageRows(ctx context.Context, userID string, start, end, detailFrom time.Time, detail bool) ([]usageRow, error) {
	if !detail {
		daily, err := s.deps.Store.ListLLMUsageDaily(ctx, userID, start, end)
		if err != nil {
			return nil, err
		}
		return fromDaily(daily), nil
	}
	var rows []usageRow
	if start.Before(detailFrom) {
		last := detailFrom.AddDate(0, 0, -1)
		if end.Before(last) {
			last = end
		}
		daily, err := s.deps.Store.ListLLMUsageDaily(ctx, userID, start, last)
		if err != nil {
			return nil, err
		}
		rows = fromDaily(daily)
	}
	if first := maxTime(start, detailFrom); !end.Before(first) {
		calls, err := s.deps.Store.ListLLMUsageDetail(ctx, userID, first, end)
		if err != nil {
			return nil, err
		}
		for _, r := range calls {
			rows = append(rows, usageRow{Day: r.Day, OrgID: r.OrgID, Provider: r.Provider, Model: r.Model, AuthMode: r.AuthMode,
				Purpose: r.Purpose, Funding: r.Funding, AccountID: r.AccountID, Calls: r.Calls, ErrorCalls: r.ErrorCalls,
				InputTokens: r.InputTokens, OutputTokens: r.OutputTokens, CacheReadTokens: r.CacheReadTokens,
				CacheWriteTokens: r.CacheWriteTokens, LatencyMsTotal: r.LatencyMsTotal})
		}
	}
	return rows, nil
}

func fromDaily(daily []*db.LLMUsageDay) []usageRow {
	rows := make([]usageRow, 0, len(daily))
	for _, r := range daily {
		rows = append(rows, usageRow{Day: r.Day, OrgID: r.OrgID, Provider: r.Provider, Model: r.Model, AuthMode: r.AuthMode,
			Purpose: r.Purpose, Calls: r.Calls, ErrorCalls: r.ErrorCalls, InputTokens: r.InputTokens,
			OutputTokens: r.OutputTokens, CacheReadTokens: r.CacheReadTokens, CacheWriteTokens: r.CacheWriteTokens,
			LatencyMsTotal: r.LatencyMsTotal})
	}
	return rows
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// labelAccounts names each account bucket by the account's label (or its
// provider when it has none). A deleted account keeps an empty label.
func (s *Service) labelAccounts(ctx context.Context, buckets []*pb.LLMUsageBucket) {
	ids := make([]string, 0, len(buckets))
	for _, b := range buckets {
		if b.Key != "" {
			ids = append(ids, b.Key)
		}
	}
	names, err := s.deps.Store.ProviderAccountNames(ctx, ids)
	if err != nil {
		s.deps.Logger.WarnContext(ctx, "usage: account names could not be read; showing ids", "error", err)
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

var groupKeys = map[pb.LLMUsageGroupBy]func(usageRow) string{
	pb.LLMUsageGroupBy_LLM_USAGE_GROUP_BY_DAY:       func(r usageRow) string { return r.Day.UTC().Format(dateLayout) },
	pb.LLMUsageGroupBy_LLM_USAGE_GROUP_BY_MODEL:     func(r usageRow) string { return r.Model },
	pb.LLMUsageGroupBy_LLM_USAGE_GROUP_BY_PROVIDER:  func(r usageRow) string { return r.Provider },
	pb.LLMUsageGroupBy_LLM_USAGE_GROUP_BY_AUTH_MODE: func(r usageRow) string { return r.AuthMode },
	pb.LLMUsageGroupBy_LLM_USAGE_GROUP_BY_PURPOSE:   func(r usageRow) string { return r.Purpose },
	pb.LLMUsageGroupBy_LLM_USAGE_GROUP_BY_FUNDING:   func(r usageRow) string { return r.Funding },
	pb.LLMUsageGroupBy_LLM_USAGE_GROUP_BY_ACCOUNT:   func(r usageRow) string { return r.AccountID },
}

var authModeNames = map[pb.LLMAuthMode]string{
	pb.LLMAuthMode_LLM_AUTH_MODE_MANAGED:      store.LLMAuthModeManaged,
	pb.LLMAuthMode_LLM_AUTH_MODE_BYOK:         store.LLMAuthModeBYOK,
	pb.LLMAuthMode_LLM_AUTH_MODE_SUBSCRIPTION: store.LLMAuthModeSubscription,
	pb.LLMAuthMode_LLM_AUTH_MODE_LOCAL:        store.LLMAuthModeLocal,
}

var purposeNames = map[pb.LLMPurpose]string{
	pb.LLMPurpose_LLM_PURPOSE_CHAT:             store.LLMPurposeChat,
	pb.LLMPurpose_LLM_PURPOSE_COMPACTION:       store.LLMPurposeCompaction,
	pb.LLMPurpose_LLM_PURPOSE_TITLE:            store.LLMPurposeTitle,
	pb.LLMPurpose_LLM_PURPOSE_DRAFT_AUTOMATION: store.LLMPurposeDraftAutomation,
}

func addRow(b *pb.LLMUsageBucket, r usageRow) {
	b.Calls += r.Calls
	b.ErrorCalls += r.ErrorCalls
	b.InputTokens += r.InputTokens
	b.OutputTokens += r.OutputTokens
	b.CacheReadTokens += r.CacheReadTokens
	b.CacheWriteTokens += r.CacheWriteTokens
	b.LatencyMsTotal += r.LatencyMsTotal
}

func finish(b *pb.LLMUsageBucket) {
	if b.Calls > 0 {
		b.AvgLatencyMs = float64(b.LatencyMsTotal) / float64(b.Calls)
	}
}

// sortBuckets orders by day ascending, or otherwise by input+output tokens
// descending, then calls descending, then key, so equal buckets are stable.
func sortBuckets(buckets []*pb.LLMUsageBucket, groupBy pb.LLMUsageGroupBy) {
	sort.Slice(buckets, func(i, j int) bool {
		a, b := buckets[i], buckets[j]
		if groupBy != pb.LLMUsageGroupBy_LLM_USAGE_GROUP_BY_DAY {
			if at, bt := a.InputTokens+a.OutputTokens, b.InputTokens+b.OutputTokens; at != bt {
				return at > bt
			}
			if a.Calls != b.Calls {
				return a.Calls > b.Calls
			}
		}
		return a.Key < b.Key
	})
}
