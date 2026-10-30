package usage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"
	fat "github.com/reliant-labs/forge/pkg/accesstoken"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/handlers/usage"
	"github.com/reliant-labs/reliant/internal/store"
)

// These tests run the service over a real database and the real rollup writer:
// scoping, grouping and range bounds are properties of the handler and the
// store together, and a stub store would only restate the handler's assumptions.

var now = time.Date(2026, 10, 30, 15, 0, 0, 0, time.UTC)

type env struct {
	t    *testing.T
	repo *db.Repo
	svc  *usage.Service
	seq  int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	return &env{t: t, repo: repo, svc: usage.NewTestUsage(t, usage.WithUsageDeps(usage.Deps{
		Store: repo,
		Now:   func() time.Time { return now },
	}))}
}

func as(user string) context.Context {
	return auth.WithUser(context.Background(), user, "authenticated", "")
}

type call struct {
	user, provider, model, mode string
	purpose                     string
	at                          time.Time
	in, out, cacheR, cacheW     int64
	latency                     int64
	outcome, errClass           string
}

func (e *env) record(c call) {
	e.t.Helper()
	e.seq++
	if c.provider == "" {
		c.provider = "anthropic"
	}
	if c.model == "" {
		c.model = "claude-opus"
	}
	if c.mode == "" {
		c.mode = store.LLMAuthModeBYOK
	}
	if c.purpose == "" {
		c.purpose = store.LLMPurposeChat
	}
	if c.outcome == "" {
		c.outcome = store.LLMOutcomeSuccess
	}
	_, err := e.repo.RecordLLMCallUsage(context.Background(), db.LLMCallUsage{
		ID: fmt.Sprintf("act-%d", e.seq), UserID: c.user, ChatID: "chat", Provider: c.provider, Model: c.model,
		AuthMode: c.mode, Purpose: c.purpose, InputTokens: c.in, OutputTokens: c.out, CacheReadTokens: c.cacheR, CacheWriteTokens: c.cacheW,
		Outcome: c.outcome, ErrorClass: c.errClass, LatencyMs: c.latency, Attempt: 1, CreatedAt: c.at,
	})
	require.NoError(e.t, err)
}

func (e *env) get(user string, req *pb.GetLLMUsageRequest) (*pb.GetLLMUsageResponse, error) {
	e.t.Helper()
	resp, err := e.svc.GetLLMUsage(as(user), connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func (e *env) mustGet(user string, req *pb.GetLLMUsageRequest) *pb.GetLLMUsageResponse {
	e.t.Helper()
	resp, err := e.get(user, req)
	require.NoError(e.t, err)
	return resp
}

func ptr[T any](v T) *T { return &v }

func keys(bs []*pb.LLMUsageBucket) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.Key
	}
	return out
}

func day(d int) time.Time { return time.Date(2026, 10, d, 10, 0, 0, 0, time.UTC) }

func TestGetLLMUsage_IsScopedToTheCaller(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.record(call{user: "alice", at: day(28), in: 100, out: 10, latency: 100})
	e.record(call{user: "bob", at: day(28), in: 5000, out: 900, latency: 9000, model: "bobs-model"})

	req := &pb.GetLLMUsageRequest{StartDate: "2026-10-01", EndDate: "2026-10-30"}
	alice := e.mustGet("alice", req)
	assert.EqualValues(t, 1, alice.Totals.Calls)
	assert.EqualValues(t, 100, alice.Totals.InputTokens)
	assert.Equal(t, []string{"2026-10-28"}, keys(alice.Buckets))

	byModel := e.mustGet("alice", &pb.GetLLMUsageRequest{StartDate: "2026-10-01", EndDate: "2026-10-30", GroupBy: pb.LLMUsageGroupBy_LLM_USAGE_GROUP_BY_MODEL})
	assert.Equal(t, []string{"claude-opus"}, keys(byModel.Buckets), "bob's model is not visible to alice")

	bob := e.mustGet("bob", req)
	assert.EqualValues(t, 5000, bob.Totals.InputTokens)

	stranger := e.mustGet("carol", req)
	assert.Empty(t, stranger.Buckets, "a user with no usage gets nothing, never someone else's")
	assert.EqualValues(t, 0, stranger.Totals.Calls)
}

func TestGetLLMUsage_NeedsAnAuthenticatedUser(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	for name, ctx := range map[string]context.Context{
		"no user":    context.Background(),
		"empty user": auth.WithUser(context.Background(), "", "authenticated", ""),
	} {
		_, err := e.svc.GetLLMUsage(ctx, connect.NewRequest(&pb.GetLLMUsageRequest{StartDate: "2026-10-01", EndDate: "2026-10-02"}))
		require.Error(t, err, name)
		assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err), name)
	}
}

// A machine credential authenticates as the user it acts for: the middleware
// puts that user in the context, and the principal changes nothing about scope.
func TestGetLLMUsage_MachineCredentialActsForItsOwner(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.record(call{user: "alice", at: day(28), in: 100, out: 10})
	e.record(call{user: "bob", at: day(28), in: 7000, out: 10})

	ctx := auth.WithMachineToken(as("alice"), &fat.Principal{ActingUserID: "alice"})
	resp, err := e.svc.GetLLMUsage(ctx, connect.NewRequest(&pb.GetLLMUsageRequest{StartDate: "2026-10-01", EndDate: "2026-10-30"}))
	require.NoError(t, err)
	assert.EqualValues(t, 100, resp.Msg.Totals.InputTokens, "the owner's usage only")
}

func TestGetLLMUsage_GroupsAndAverages(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.record(call{user: "u", at: day(27), in: 100, out: 20, cacheR: 10, cacheW: 5, latency: 100})
	e.record(call{user: "u", at: day(27), in: 300, out: 40, latency: 300})
	e.record(call{user: "u", at: day(28), in: 50, out: 5, latency: 200, outcome: store.LLMOutcomeError, errClass: "rate_limit"})
	e.record(call{user: "u", at: day(28), in: 1000, out: 500, model: "sonnet", latency: 400})
	e.record(call{user: "u", at: day(28), in: 10, out: 10, provider: "claude", mode: store.LLMAuthModeSubscription, latency: 50})
	e.record(call{user: "u", at: day(28), in: 1, out: 1, provider: "reliant", mode: store.LLMAuthModeManaged, model: "managed-model"})
	rng := func(g pb.LLMUsageGroupBy) *pb.GetLLMUsageRequest {
		return &pb.GetLLMUsageRequest{StartDate: "2026-10-27", EndDate: "2026-10-28", GroupBy: g}
	}

	t.Run("day is the default, oldest first", func(t *testing.T) {
		r := e.mustGet("u", &pb.GetLLMUsageRequest{StartDate: "2026-10-27", EndDate: "2026-10-28"})
		assert.Equal(t, pb.LLMUsageGroupBy_LLM_USAGE_GROUP_BY_DAY, r.GroupBy)
		require.Equal(t, []string{"2026-10-27", "2026-10-28"}, keys(r.Buckets))
		d27 := r.Buckets[0]
		assert.EqualValues(t, 2, d27.Calls)
		assert.EqualValues(t, 0, d27.ErrorCalls)
		assert.EqualValues(t, 400, d27.InputTokens)
		assert.EqualValues(t, 60, d27.OutputTokens)
		assert.EqualValues(t, 10, d27.CacheReadTokens)
		assert.EqualValues(t, 5, d27.CacheWriteTokens)
		assert.EqualValues(t, 400, d27.LatencyMsTotal)
		assert.InDelta(t, 200.0, d27.AvgLatencyMs, 1e-9)
		assert.EqualValues(t, 4, r.Buckets[1].Calls)
		assert.EqualValues(t, 1, r.Buckets[1].ErrorCalls)
		assert.EqualValues(t, 6, r.Totals.Calls, "totals cover every bucket")
		assert.Empty(t, r.Totals.Key)
		assert.InDelta(t, float64(100+300+200+400+50+0)/6, r.Totals.AvgLatencyMs, 1e-9)
	})
	t.Run("model, most tokens first", func(t *testing.T) {
		r := e.mustGet("u", rng(pb.LLMUsageGroupBy_LLM_USAGE_GROUP_BY_MODEL))
		assert.Equal(t, []string{"sonnet", "claude-opus", "managed-model"}, keys(r.Buckets))
		assert.EqualValues(t, 1500, r.Buckets[0].InputTokens+r.Buckets[0].OutputTokens)
	})
	t.Run("provider", func(t *testing.T) {
		r := e.mustGet("u", rng(pb.LLMUsageGroupBy_LLM_USAGE_GROUP_BY_PROVIDER))
		assert.Equal(t, []string{"anthropic", "claude", "reliant"}, keys(r.Buckets))
	})
	t.Run("auth mode", func(t *testing.T) {
		r := e.mustGet("u", rng(pb.LLMUsageGroupBy_LLM_USAGE_GROUP_BY_AUTH_MODE))
		assert.Equal(t, []string{"byok", "subscription", "managed"}, keys(r.Buckets))
	})
}

func TestGetLLMUsage_Filters(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.record(call{user: "u", at: day(28), in: 100})
	e.record(call{user: "u", at: day(28), in: 200, model: "sonnet"})
	e.record(call{user: "u", at: day(28), in: 400, provider: "claude", mode: store.LLMAuthModeSubscription})
	e.record(call{user: "u", at: day(28), in: 800, provider: "reliant", mode: store.LLMAuthModeManaged, model: "managed-model"})
	base := func() *pb.GetLLMUsageRequest {
		return &pb.GetLLMUsageRequest{StartDate: "2026-10-28", EndDate: "2026-10-28"}
	}

	r := base()
	r.Provider = ptr("anthropic")
	assert.EqualValues(t, 300, e.mustGet("u", r).Totals.InputTokens)

	r = base()
	r.Model = ptr("sonnet")
	assert.EqualValues(t, 200, e.mustGet("u", r).Totals.InputTokens)

	r = base()
	r.AuthMode = ptr(pb.LLMAuthMode_LLM_AUTH_MODE_SUBSCRIPTION)
	assert.EqualValues(t, 400, e.mustGet("u", r).Totals.InputTokens)

	r = base()
	r.Provider, r.AuthMode = ptr("anthropic"), ptr(pb.LLMAuthMode_LLM_AUTH_MODE_MANAGED)
	got := e.mustGet("u", r)
	assert.Empty(t, got.Buckets, "filters combine with AND")
	assert.EqualValues(t, 0, got.Totals.Calls)

	r = base()
	r.Model = ptr("")
	assert.Empty(t, e.mustGet("u", r).Buckets, "a set-but-empty filter matches nothing; only an unset one is absent")

	r = base()
	r.AuthMode = ptr(pb.LLMAuthMode_LLM_AUTH_MODE_UNSPECIFIED)
	_, err := e.get("u", r)
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "an explicit unspecified auth mode is not a filter")
}

func TestGetLLMUsage_RangeBounds(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	// 23:59 UTC on the 27th and 00:01 UTC on the 28th are different days.
	e.record(call{user: "u", at: time.Date(2026, 10, 27, 23, 59, 0, 0, time.UTC), in: 1})
	e.record(call{user: "u", at: time.Date(2026, 10, 28, 0, 1, 0, 0, time.UTC), in: 10})
	e.record(call{user: "u", at: time.Date(2026, 10, 29, 23, 59, 0, 0, time.UTC), in: 100})
	e.record(call{user: "u", at: time.Date(2026, 10, 30, 0, 1, 0, 0, time.UTC), in: 1000})

	r := e.mustGet("u", &pb.GetLLMUsageRequest{StartDate: "2026-10-28", EndDate: "2026-10-29"})
	assert.Equal(t, []string{"2026-10-28", "2026-10-29"}, keys(r.Buckets), "both ends are inclusive")
	assert.EqualValues(t, 110, r.Totals.InputTokens)

	single := e.mustGet("u", &pb.GetLLMUsageRequest{StartDate: "2026-10-30", EndDate: "2026-10-30"})
	assert.EqualValues(t, 1000, single.Totals.InputTokens, "one day is a valid range")
}

func TestGetLLMUsage_RejectsBadRanges(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	cases := []struct {
		name       string
		start, end string
		ok         bool
	}{
		{"400 days is the most", "2025-09-26", "2026-10-30", true},
		{"401 days is too many", "2025-09-25", "2026-10-30", false},
		{"end before start", "2026-10-30", "2026-10-29", false},
		{"not a date", "yesterday", "2026-10-29", false},
		{"not a real date", "2026-02-30", "2026-03-01", false},
		{"empty", "", "", false},
		{"timestamp rather than date", "2026-10-01T00:00:00Z", "2026-10-02", false},
	}
	// Guard the arithmetic the first two rows rely on.
	s, _ := time.Parse("2006-01-02", "2025-09-26")
	require.Equal(t, 400, int(time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC).Sub(s).Hours()/24)+1)

	for _, c := range cases {
		_, err := e.get("u", &pb.GetLLMUsageRequest{StartDate: c.start, EndDate: c.end})
		if c.ok {
			assert.NoError(t, err, c.name)
			continue
		}
		require.Error(t, err, c.name)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), c.name)
	}

	_, err := e.get("u", &pb.GetLLMUsageRequest{StartDate: "2026-10-01", EndDate: "2026-10-02", GroupBy: pb.LLMUsageGroupBy(99)})
	assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "unknown group_by")
}

// Per-call rows live 90 days; the rollup is kept. A range reaching past the
// per-call window is answered from the rollup and says so.
func TestGetLLMUsage_CompletenessFollowsPerCallRetention(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	// now is 2026-10-30 15:00 UTC: calls older than 2026-08-01 15:00 are pruned,
	// so 2026-08-02 is the first day whose calls are all still stored.
	require.Equal(t, 90*24*time.Hour, db.LLMCallUsageRetention)
	e.record(call{user: "u", at: time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC), in: 7})
	e.record(call{user: "u", at: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC), in: 3})

	t.Run("inside the window is complete", func(t *testing.T) {
		r := e.mustGet("u", &pb.GetLLMUsageRequest{StartDate: "2026-08-02", EndDate: "2026-10-30"})
		assert.Equal(t, pb.LLMUsageCompleteness_LLM_USAGE_COMPLETENESS_COMPLETE, r.Completeness)
		assert.Equal(t, "2026-08-02", r.DetailAvailableFrom)
		assert.Equal(t, "2026-10-30T15:00:00Z", r.AsOf)
		assert.EqualValues(t, 3, r.Totals.InputTokens)
	})
	t.Run("one day before the window is partial", func(t *testing.T) {
		r := e.mustGet("u", &pb.GetLLMUsageRequest{StartDate: "2026-08-01", EndDate: "2026-10-30"})
		assert.Equal(t, pb.LLMUsageCompleteness_LLM_USAGE_COMPLETENESS_PARTIAL, r.Completeness)
		assert.Contains(t, r.Note, "2026-08-02")
	})
	t.Run("beyond the window the rollup still answers", func(t *testing.T) {
		r := e.mustGet("u", &pb.GetLLMUsageRequest{StartDate: "2026-06-01", EndDate: "2026-10-30"})
		assert.Equal(t, pb.LLMUsageCompleteness_LLM_USAGE_COMPLETENESS_PARTIAL, r.Completeness)
		assert.EqualValues(t, 10, r.Totals.InputTokens, "the July call is in the totals")
		assert.Equal(t, []string{"2026-07-01", "2026-10-01"}, keys(r.Buckets))
	})
	t.Run("a range entirely in the past is partial", func(t *testing.T) {
		r := e.mustGet("u", &pb.GetLLMUsageRequest{StartDate: "2026-07-01", EndDate: "2026-07-01"})
		assert.Equal(t, pb.LLMUsageCompleteness_LLM_USAGE_COMPLETENESS_PARTIAL, r.Completeness)
		assert.EqualValues(t, 7, r.Totals.InputTokens)
	})
}

// The detail boundary is a day, not an instant: at exactly midnight the cutoff
// day is whole.
func TestGetLLMUsage_DetailBoundaryAtMidnight(t *testing.T) {
	t.Parallel()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	at := time.Date(2026, 10, 30, 0, 0, 0, 0, time.UTC)
	svc := usage.NewTestUsage(t, usage.WithUsageDeps(usage.Deps{Store: repo, Now: func() time.Time { return at }}))
	resp, err := svc.GetLLMUsage(as("u"), connect.NewRequest(&pb.GetLLMUsageRequest{StartDate: "2026-08-01", EndDate: "2026-08-01"}))
	require.NoError(t, err)
	assert.Equal(t, "2026-08-01", resp.Msg.DetailAvailableFrom)
	assert.Equal(t, pb.LLMUsageCompleteness_LLM_USAGE_COMPLETENESS_COMPLETE, resp.Msg.Completeness)
}

func TestGetLLMUsage_PurposeSplitsChatFromBackgroundRequests(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.record(call{user: "u", at: day(28), in: 1000, out: 200, purpose: store.LLMPurposeChat})
	e.record(call{user: "u", at: day(28), in: 2000, out: 100, purpose: store.LLMPurposeChat})
	e.record(call{user: "u", at: day(28), in: 50000, out: 800, purpose: store.LLMPurposeCompaction})
	e.record(call{user: "u", at: day(28), in: 80, out: 10, purpose: store.LLMPurposeTitle})
	e.record(call{user: "u", at: day(28), in: 300, out: 60, purpose: store.LLMPurposeDraftAutomation})
	e.record(call{user: "other", at: day(28), in: 999999, purpose: store.LLMPurposeCompaction})
	base := func() *pb.GetLLMUsageRequest {
		return &pb.GetLLMUsageRequest{StartDate: "2026-10-28", EndDate: "2026-10-28"}
	}

	t.Run("group by purpose, most tokens first", func(t *testing.T) {
		r := base()
		r.GroupBy = pb.LLMUsageGroupBy_LLM_USAGE_GROUP_BY_PURPOSE
		got := e.mustGet("u", r)
		assert.Equal(t, []string{"compaction", "chat", "draft_automation", "title"}, keys(got.Buckets))
		assert.EqualValues(t, 2, got.Buckets[1].Calls, "two chat turns")
		assert.EqualValues(t, 3000, got.Buckets[1].InputTokens)
		assert.EqualValues(t, 5, got.Totals.Calls, "totals cover every purpose and only the caller's")
	})
	t.Run("filter by purpose", func(t *testing.T) {
		r := base()
		r.Purpose = ptr(pb.LLMPurpose_LLM_PURPOSE_COMPACTION)
		got := e.mustGet("u", r)
		assert.EqualValues(t, 1, got.Totals.Calls)
		assert.EqualValues(t, 50000, got.Totals.InputTokens, "another user's compaction is not included")
	})
	t.Run("purpose combines with the other filters", func(t *testing.T) {
		r := base()
		r.Purpose = ptr(pb.LLMPurpose_LLM_PURPOSE_CHAT)
		r.Provider = ptr("nobody")
		assert.Empty(t, e.mustGet("u", r).Buckets)
	})
	t.Run("an explicit unspecified purpose is not a filter", func(t *testing.T) {
		r := base()
		r.Purpose = ptr(pb.LLMPurpose_LLM_PURPOSE_UNSPECIFIED)
		_, err := e.get("u", r)
		assert.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err))
	})
	t.Run("no purpose filter counts them all", func(t *testing.T) {
		assert.EqualValues(t, 5, e.mustGet("u", base()).Totals.Calls)
	})
}

// A day's rollup keeps the purpose after the per-call rows are gone.
func TestGetLLMUsage_PurposeSurvivesPerCallRetention(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.record(call{user: "u", at: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC), in: 40, purpose: store.LLMPurposeChat})
	e.record(call{user: "u", at: time.Date(2026, 6, 1, 12, 5, 0, 0, time.UTC), in: 9000, purpose: store.LLMPurposeCompaction})
	pruned, err := e.repo.PruneLLMCallUsage(context.Background(), now)
	require.NoError(t, err)
	require.EqualValues(t, 2, pruned, "the per-call rows are gone")

	r := e.mustGet("u", &pb.GetLLMUsageRequest{StartDate: "2026-06-01", EndDate: "2026-06-01", GroupBy: pb.LLMUsageGroupBy_LLM_USAGE_GROUP_BY_PURPOSE})
	assert.Equal(t, pb.LLMUsageCompleteness_LLM_USAGE_COMPLETENESS_PARTIAL, r.Completeness)
	assert.Equal(t, []string{"compaction", "chat"}, keys(r.Buckets))
	assert.EqualValues(t, 9000, r.Buckets[0].InputTokens)
}

// Each Temporal attempt is its own provider request, so a failed attempt that
// used tokens is in the totals next to the retry that succeeded.
func TestGetLLMUsage_CountsEveryAttemptOfARetriedRequest(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	rec := func(attempt int32, outcome, class string, in int64) {
		_, err := e.repo.RecordLLMCallUsage(context.Background(), db.LLMCallUsage{
			ID: "wf/run/act/chat/1", UserID: "u", ChatID: "c", Provider: "anthropic", Model: "m", AuthMode: store.LLMAuthModeBYOK,
			Purpose: store.LLMPurposeChat, InputTokens: in, Outcome: outcome, ErrorClass: class, LatencyMs: 100,
			Attempt: attempt, CreatedAt: day(28),
		})
		require.NoError(t, err)
	}
	rec(1, store.LLMOutcomeError, "network", 500)
	rec(1, store.LLMOutcomeError, "network", 500)
	rec(2, store.LLMOutcomeSuccess, "", 700)

	r := e.mustGet("u", &pb.GetLLMUsageRequest{StartDate: "2026-10-28", EndDate: "2026-10-28"})
	assert.EqualValues(t, 2, r.Totals.Calls)
	assert.EqualValues(t, 1, r.Totals.ErrorCalls)
	assert.EqualValues(t, 1200, r.Totals.InputTokens, "the failed attempt's tokens are not lost")
}
