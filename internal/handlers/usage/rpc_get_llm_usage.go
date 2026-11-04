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
	rows, err := s.deps.Store.ListLLMUsageDaily(ctx, userID, start, end)
	if err != nil {
		return nil, svcerr.Wrap(err)
	}

	byKey := map[string]*pb.LLMUsageBucket{}
	totals := &pb.LLMUsageBucket{}
	for _, row := range rows {
		if (msg.Provider != nil && row.Provider != msg.GetProvider()) ||
			(msg.Model != nil && row.Model != msg.GetModel()) ||
			(msg.AuthMode != nil && row.AuthMode != authMode) ||
			(msg.Purpose != nil && row.Purpose != purpose) {
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

	detailFrom := detailAvailableFrom(now)
	completeness := pb.LLMUsageCompleteness_LLM_USAGE_COMPLETENESS_COMPLETE
	note := "Every call in this range is counted. Today's figures are still accumulating."
	if start.Before(detailFrom) {
		completeness = pb.LLMUsageCompleteness_LLM_USAGE_COMPLETENESS_PARTIAL
		note = fmt.Sprintf(
			"Individual calls are kept for %d days; days before %s come from the daily totals, which are kept. Those totals are complete, but cannot be broken down further.",
			int(db.LLMCallUsageRetention/(24*time.Hour)), detailFrom.Format(dateLayout))
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

var groupKeys = map[pb.LLMUsageGroupBy]func(*db.LLMUsageDay) string{
	pb.LLMUsageGroupBy_LLM_USAGE_GROUP_BY_DAY:       func(r *db.LLMUsageDay) string { return r.Day.Format(dateLayout) },
	pb.LLMUsageGroupBy_LLM_USAGE_GROUP_BY_MODEL:     func(r *db.LLMUsageDay) string { return r.Model },
	pb.LLMUsageGroupBy_LLM_USAGE_GROUP_BY_PROVIDER:  func(r *db.LLMUsageDay) string { return r.Provider },
	pb.LLMUsageGroupBy_LLM_USAGE_GROUP_BY_AUTH_MODE: func(r *db.LLMUsageDay) string { return r.AuthMode },
	pb.LLMUsageGroupBy_LLM_USAGE_GROUP_BY_PURPOSE:   func(r *db.LLMUsageDay) string { return r.Purpose },
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

func addRow(b *pb.LLMUsageBucket, r *db.LLMUsageDay) {
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
