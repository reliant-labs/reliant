package chat_trace

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/viewertest"
	"github.com/reliant-labs/reliant/pkg/config"
)

// now is the fixed clock of every test here; the fixture's chat is moved to
// chatCreated, two hours before it.
var (
	now         = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	chatCreated = now.Add(-2 * time.Hour)
)

// Sentinels a viewer must never read.
const (
	traceSecret  = "trace-id-sentinel"
	costSentinel = "0.98765"
	toolResult   = "TOOL_RESULT_SENTINEL"
)

// fakeSpans is control-plane's span store: it answers pages in turn (then an
// empty page) or err, and records what it was asked.
type fakeSpans struct {
	mu    sync.Mutex
	pages []controlplane.ConversationTrace
	err   error
	asked []controlplane.ConversationTraceQuery
}

func (f *fakeSpans) GetConversationTrace(_ context.Context, q controlplane.ConversationTraceQuery) (controlplane.ConversationTrace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, q)
	if f.err != nil {
		return controlplane.ConversationTrace{}, f.err
	}
	if i := len(f.asked) - 1; i < len(f.pages) {
		return f.pages[i], nil
	}
	return controlplane.ConversationTrace{}, nil
}

func (f *fakeSpans) queries() []controlplane.ConversationTraceQuery {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]controlplane.ConversationTraceQuery(nil), f.asked...)
}

// chatSpan is a span of f's shared chat, in its org, carrying every kind of
// attribute: structure, tokens, cost, ids, reliant.* keys and a tool result
// (which the span store's allowlist never answers; here so the viewer's own
// allowlist is shown to drop what it does not name).
func chatSpan(f *viewertest.Fixture, id, parent, run string, start time.Time) controlplane.ConversationSpan {
	return controlplane.ConversationSpan{
		TraceID: traceSecret, SpanID: id, ParentSpanID: parent, Name: "execute_tool bash", Kind: "Internal",
		ServiceName: "reliant-temporal-worker", Start: start, DurationNs: 1500, StatusCode: "Ok",
		Attributes: map[string]string{
			"org.id":                    viewertest.OrgID,
			"gen_ai.conversation.id":    f.ChatID,
			"user.id":                   f.OwnerID,
			"forge.ai.run.id":           run,
			"gen_ai.operation.name":     "execute_tool",
			"gen_ai.tool.name":          "bash",
			"gen_ai.request.model":      "claude-test",
			"gen_ai.usage.input_tokens": "100",
			"forge.ai.cost.usd":         costSentinel,
			"reliant.cost_usd":          costSentinel,
			"reliant.model_id":          "model-sentinel",
			"gen_ai.tool.call.result":   toolResult,
		},
		Events: []controlplane.ConversationSpanEvent{{Name: "gen_ai.first_token", Time: start.Add(time.Millisecond),
			Attributes: map[string]string{"reliant.secret": toolResult}}},
		Links: []controlplane.ConversationSpanLink{{TraceID: traceSecret, SpanID: "linked"}},
	}
}

// setup is the viewer fixture with its shared chat created at chatCreated,
// unread, and a service over it reading spans from spans (nil: none).
func setup(t *testing.T, spans Spans) (*viewertest.Fixture, *Service) {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	_, err := repo.ORM().Bun().NewRaw(`UPDATE chats SET created_at = ?, last_active = ?, unread = 1 WHERE id = ?`,
		chatCreated, chatCreated.Add(time.Minute), f.ChatID).Exec(context.Background())
	require.NoError(t, err)
	svc, err := New(Deps{
		Logger: slog.New(slog.DiscardHandler), Config: &config.Config{}, Store: repo, Spans: spans,
		Now: func() time.Time { return now },
	})
	require.NoError(t, err)
	return f, svc
}

// call is one llm_call_usage row to seed.
type call struct {
	id, user, run, basis string
	cost                 *int64
	at                   time.Time
	outcome, errorClass  string
}

func seedCalls(t *testing.T, f *viewertest.Fixture, calls ...call) {
	t.Helper()
	for _, c := range calls {
		if c.user == "" {
			c.user = f.OwnerID
		}
		if c.outcome == "" {
			c.outcome = "success"
		}
		var basis, run any
		if c.basis != "" {
			basis = c.basis
		}
		if c.run != "" {
			run = c.run
		}
		_, err := f.Repo.ORM().Bun().NewRaw(`INSERT INTO llm_call_usage
			(id, user_id, org_id, chat_id, provider, model, auth_mode, purpose, outcome, error_class,
			 input_tokens, output_tokens, latency_ms, attempt, created_at, ai_run_id, cost_usd_nanos, cost_basis)
			VALUES (?, ?, ?, ?, 'anthropic', 'claude-test', 'managed', 'chat', ?, ?, 100, 20, 900, 1, ?, ?, ?, ?)`,
			c.id, c.user, viewertest.OrgID, f.ChatID, c.outcome, c.errorClass, c.at, run, c.cost, basis).Exec(context.Background())
		require.NoError(t, err, c.id)
	}
}

func get(svc *Service, ctx context.Context, req *pb.GetChatTraceRequest) (*pb.GetChatTraceResponse, error) {
	resp, err := svc.GetChatTrace(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg, nil
}

func nanos(n int64) *int64 { return &n }

// notMember answers every membership question "not a member": a member the
// org removed.
type notMember struct{}

func (notMember) IsMember(context.Context, string, string) (bool, bool, error) {
	return false, true, nil
}

// The viewer matrix (control-plane docs/design/org-sharing.md §4): the owner
// and a viewer of the shared chat are answered, an outsider is not.
func TestChatTraceServiceViewerMatrix(t *testing.T) {
	spans := &fakeSpans{}
	f, svc := setup(t, spans)
	spans.pages = []controlplane.ConversationTrace{{Spans: []controlplane.ConversationSpan{chatSpan(f, "s1", "", "run-1", chatCreated)}}}
	viewertest.RunService(t, f, svc, "reliant.v1.ChatTraceService", nil)
}

// Every caller of the B2 matrix: the owner reads the whole trace, a viewer
// the stubbed one, and an outsider, a removed member, a viewer through a
// machine credential and a credential bound to another org are NotFound,
// before the span store is ever asked.
func TestGetChatTrace_Callers(t *testing.T) {
	spans := &fakeSpans{}
	f, svc := setup(t, spans)
	span := chatSpan(f, "s1", "", "run-1", chatCreated.Add(time.Minute))
	spans.pages = []controlplane.ConversationTrace{{Spans: []controlplane.ConversationSpan{span}}, {Spans: []controlplane.ConversationSpan{span}}}
	seedCalls(t, f, call{id: "c1", basis: "reported", cost: nanos(1234), at: chatCreated.Add(2 * time.Minute)})
	req := &pb.GetChatTraceRequest{ChatId: f.ChatID}

	owner, err := get(svc, f.Owner, req)
	require.NoError(t, err)
	assert.False(t, owner.GetViewer())
	require.Len(t, owner.GetSpans(), 1)
	assert.Equal(t, traceSecret, owner.GetSpans()[0].GetTraceId())
	assert.Equal(t, costSentinel, owner.GetSpans()[0].GetAttributes()["forge.ai.cost.usd"])
	require.Len(t, owner.GetModelCalls(), 1)
	assert.Equal(t, int64(1234), owner.GetModelCalls()[0].GetCostUsdNanos())
	assert.Equal(t, int64(1234), owner.GetTotals().GetReportedCostUsdNanos())

	viewer, err := get(svc, f.Viewer, req)
	require.NoError(t, err)
	assert.True(t, viewer.GetViewer())
	require.Len(t, viewer.GetSpans(), 1)
	require.Len(t, viewer.GetModelCalls(), 1)

	// Every span read was of the chat's own org and id, in a window the
	// span store accepts.
	asked := spans.queries()
	require.Len(t, asked, 2)
	for _, q := range asked {
		assert.Equal(t, viewertest.OrgID, q.OrgID)
		assert.Equal(t, f.ChatID, q.ConversationID)
		assert.True(t, q.End.After(q.Start) && q.End.Sub(q.Start) <= controlplane.MaxConversationTraceWindow)
	}

	for name, ctx := range map[string]context.Context{
		"outsider":                                f.Outsider,
		"removed member":                          f.ContextWith(f.ViewerID, notMember{}),
		"owner removed from the org":              f.ContextWith(f.OwnerID, notMember{}),
		"viewer through a machine credential":     f.ContextAs(f.ViewerID, auth.CredentialAccessToken),
		"owner through another org's credential":  authz.WithCredentialOrg(f.Owner, viewertest.OtherOrgID),
		"viewer through another org's credential": authz.WithCredentialOrg(f.Viewer, viewertest.OtherOrgID),
	} {
		_, err := get(svc, ctx, req)
		assert.Equal(t, connect.CodeNotFound, svcerr.Code(err), name)
	}
	assert.Len(t, spans.queries(), 2, "a refused caller never reaches the span store")

	_, err = get(svc, f.Owner, &pb.GetChatTraceRequest{ChatId: "no-such-chat"})
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err), "a chat that does not exist answers as one the caller may not read")
	_, err = get(svc, f.Owner, &pb.GetChatTraceRequest{})
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
	_, err = get(svc, f.Owner, &pb.GetChatTraceRequest{ChatId: f.ChatID,
		StartTime: timestamppb.New(chatCreated), EndTime: timestamppb.New(chatCreated)})
	assert.Equal(t, connect.CodeInvalidArgument, svcerr.Code(err))
}

// A viewer's spans and model calls carry no cost, no reliant.* key, no tool
// result and no id but the span's (D4: viewers see no dollars).
func TestGetChatTrace_ViewerSeesNoCostNoReliantKeysNoResults(t *testing.T) {
	spans := &fakeSpans{}
	f, svc := setup(t, spans)
	spans.pages = []controlplane.ConversationTrace{{Spans: []controlplane.ConversationSpan{
		chatSpan(f, "root", "", "run-1", chatCreated.Add(time.Minute)),
		chatSpan(f, "child", "root", "run-1", chatCreated.Add(2*time.Minute)),
	}}}
	seedCalls(t, f,
		call{id: "reported", basis: "reported", cost: nanos(987650000), at: chatCreated.Add(time.Minute)},
		call{id: "estimated", basis: "estimated", cost: nanos(5), at: chatCreated.Add(2 * time.Minute)})

	resp, err := get(svc, f.Viewer, &pb.GetChatTraceRequest{ChatId: f.ChatID})
	require.NoError(t, err)
	require.Len(t, resp.GetSpans(), 2)
	for _, s := range resp.GetSpans() {
		assert.Empty(t, s.GetTraceId())
		assert.Empty(t, s.GetLinks())
		for k := range s.GetAttributes() {
			assert.True(t, viewerSpanAttributes[k], "a viewer reads only allowlisted attributes, not %s", k)
			assert.False(t, strings.HasPrefix(k, "reliant."), k)
		}
		assert.Equal(t, "bash", s.GetAttributes()["gen_ai.tool.name"])
		assert.Equal(t, "100", s.GetAttributes()["gen_ai.usage.input_tokens"])
		for _, e := range s.GetEvents() {
			assert.Empty(t, e.GetAttributes())
		}
	}
	assert.Equal(t, "root", resp.GetSpans()[1].GetParentSpanId(), "the tree survives the stub")
	for _, c := range resp.GetModelCalls() {
		assert.Nil(t, c.CostUsdNanos)
		assert.Empty(t, c.GetCostBasis())
		assert.Empty(t, c.GetId())
		assert.Empty(t, c.GetAuthMode())
		assert.Empty(t, c.GetRunId())
		assert.Equal(t, int64(100), c.GetInputTokens())
	}
	assert.Equal(t, int64(2), resp.GetTotals().GetCalls())

	wire, err := protojson.Marshal(resp)
	require.NoError(t, err)
	for _, never := range []string{"cost", "Cost", costSentinel, "987650000", "reliant.", toolResult, traceSecret, f.OwnerID, "model-sentinel"} {
		assert.NotContains(t, string(wire), never)
	}

	owner, err := get(svc, f.Owner, &pb.GetChatTraceRequest{ChatId: f.ChatID})
	require.NoError(t, err)
	assert.Equal(t, int64(987650000), owner.GetTotals().GetReportedCostUsdNanos())
	assert.Equal(t, int64(5), owner.GetTotals().GetEstimatedCostUsdNanos(), "estimated cost is never added to reported")
}

// An expired trace, an older control plane, a deployment with no span store
// and a store that fails are each PARTIAL with their reason, and still list
// the model calls: never an empty trace.
func TestGetChatTrace_PartialNeverEmpty(t *testing.T) {
	t.Run("trace_expired", func(t *testing.T) {
		spans := &fakeSpans{}
		f, svc := setup(t, spans)
		old := now.Add(-10 * 24 * time.Hour)
		_, err := f.Repo.ORM().Bun().NewRaw(`UPDATE chats SET created_at = ? WHERE id = ?`, old, f.ChatID).Exec(context.Background())
		require.NoError(t, err)
		seedCalls(t, f, call{id: "old", at: old.Add(time.Hour)})

		resp, err := get(svc, f.Owner, &pb.GetChatTraceRequest{ChatId: f.ChatID})
		require.NoError(t, err)
		assert.Equal(t, pb.ChatTraceCompleteness_CHAT_TRACE_COMPLETENESS_PARTIAL, resp.GetCompleteness())
		assert.Equal(t, []pb.ChatTracePartialReason{pb.ChatTracePartialReason_CHAT_TRACE_PARTIAL_REASON_TRACE_EXPIRED}, resp.GetPartialReasons())
		assert.Len(t, resp.GetModelCalls(), 1)
		require.Len(t, spans.queries(), 1, "the last seven days are still read")
		assert.Equal(t, now.Add(-controlplane.MaxConversationTraceWindow), spans.queries()[0].Start)
		assert.Equal(t, now, spans.queries()[0].End)

		// A window wholly older than the store keeps is not asked for.
		resp, err = get(svc, f.Owner, &pb.GetChatTraceRequest{ChatId: f.ChatID, EndTime: timestamppb.New(old.Add(2 * time.Hour))})
		require.NoError(t, err)
		assert.Equal(t, []pb.ChatTracePartialReason{pb.ChatTracePartialReason_CHAT_TRACE_PARTIAL_REASON_TRACE_EXPIRED}, resp.GetPartialReasons())
		assert.Len(t, resp.GetModelCalls(), 1)
		assert.Nil(t, resp.GetSpansStartTime())
		assert.Len(t, spans.queries(), 1)
	})

	for name, spans := range map[string]Spans{
		"older control plane": &fakeSpans{err: controlplane.ErrConversationTraceUnsupported},
		"rate limited":        &fakeSpans{err: &controlplane.ProjectOrgMoveError{Code: "resource_exhausted"}},
		"no span store":       nil,
	} {
		t.Run(name, func(t *testing.T) {
			f, svc := setup(t, spans)
			seedCalls(t, f, call{id: "c1", at: chatCreated.Add(time.Minute)})
			resp, err := get(svc, f.Owner, &pb.GetChatTraceRequest{ChatId: f.ChatID})
			require.NoError(t, err)
			assert.Equal(t, pb.ChatTraceCompleteness_CHAT_TRACE_COMPLETENESS_PARTIAL, resp.GetCompleteness())
			assert.Equal(t, []pb.ChatTracePartialReason{pb.ChatTracePartialReason_CHAT_TRACE_PARTIAL_REASON_SPANS_UNAVAILABLE}, resp.GetPartialReasons())
			assert.Len(t, resp.GetModelCalls(), 1)
		})
	}

	t.Run("spans missing", func(t *testing.T) {
		f, svc := setup(t, &fakeSpans{})
		seedCalls(t, f, call{id: "c1", at: chatCreated.Add(time.Minute)})
		resp, err := get(svc, f.Owner, &pb.GetChatTraceRequest{ChatId: f.ChatID})
		require.NoError(t, err)
		assert.Equal(t, []pb.ChatTracePartialReason{pb.ChatTracePartialReason_CHAT_TRACE_PARTIAL_REASON_SPANS_MISSING}, resp.GetPartialReasons())
	})

	t.Run("complete", func(t *testing.T) {
		spans := &fakeSpans{}
		f, svc := setup(t, spans)
		spans.pages = []controlplane.ConversationTrace{{Spans: []controlplane.ConversationSpan{chatSpan(f, "s1", "", "run-1", chatCreated.Add(time.Minute))}}}
		seedCalls(t, f, call{id: "c1", at: chatCreated.Add(time.Minute)})
		resp, err := get(svc, f.Owner, &pb.GetChatTraceRequest{ChatId: f.ChatID})
		require.NoError(t, err)
		assert.Equal(t, pb.ChatTraceCompleteness_CHAT_TRACE_COMPLETENESS_COMPLETE, resp.GetCompleteness())
		assert.Empty(t, resp.GetPartialReasons())
	})
}

// Opening a trace changes nothing of the chat: not its unread flag, not its
// last_active, not its updated_at.
func TestGetChatTrace_DoesNotTouchUnreadOrLastActive(t *testing.T) {
	spans := &fakeSpans{}
	f, svc := setup(t, spans)
	ctx := context.Background()
	before, err := f.Repo.GetChatRow(ctx, f.ChatID)
	require.NoError(t, err)
	require.True(t, before.Unread)

	for _, caller := range []context.Context{f.Owner, f.Viewer} {
		_, err := get(svc, caller, &pb.GetChatTraceRequest{ChatId: f.ChatID})
		require.NoError(t, err)
	}
	after, err := f.Repo.GetChatRow(ctx, f.ChatID)
	require.NoError(t, err)
	assert.True(t, after.Unread)
	assert.True(t, before.LastActive.Equal(after.LastActive), "last_active %v → %v", before.LastActive, after.LastActive)
	assert.True(t, before.UpdatedAt.Equal(after.UpdatedAt), "updated_at %v → %v", before.UpdatedAt, after.UpdatedAt)
}

// A truncated page is read on from its last span's start, skipping spans
// already read; a span of another org or chat the store answered is dropped;
// a store that keeps answering truncated is cut off and says so.
func TestGetChatTrace_PagesAndDropsForeignSpans(t *testing.T) {
	spans := &fakeSpans{}
	f, svc := setup(t, spans)
	t1, t2 := chatCreated.Add(time.Minute), chatCreated.Add(2*time.Minute)
	foreign := chatSpan(f, "foreign", "", "run-1", t1)
	foreign.Attributes["org.id"] = viewertest.OtherOrgID
	otherChat := chatSpan(f, "other-chat", "", "run-1", t1)
	otherChat.Attributes["gen_ai.conversation.id"] = "another-chat"
	spans.pages = []controlplane.ConversationTrace{
		{Spans: []controlplane.ConversationSpan{chatSpan(f, "a", "", "run-1", t1), foreign, otherChat, chatSpan(f, "b", "a", "run-1", t2)}, Truncated: true},
		{Spans: []controlplane.ConversationSpan{chatSpan(f, "b", "a", "run-1", t2), chatSpan(f, "c", "a", "run-2", t2)}},
	}
	resp, err := get(svc, f.Owner, &pb.GetChatTraceRequest{ChatId: f.ChatID})
	require.NoError(t, err)
	var ids []string
	for _, s := range resp.GetSpans() {
		ids = append(ids, s.GetSpanId())
	}
	assert.Equal(t, []string{"a", "b", "c"}, ids)
	asked := spans.queries()
	require.Len(t, asked, 2)
	assert.Equal(t, t2, asked[1].Start, "the next page starts at the last span read")
	assert.Equal(t, pb.ChatTraceCompleteness_CHAT_TRACE_COMPLETENESS_COMPLETE, resp.GetCompleteness())

	// run_id keeps one run's spans and calls.
	seedCalls(t, f, call{id: "run1", run: "run-1", at: t1}, call{id: "run2", run: "run-2", at: t2})
	spans.mu.Lock()
	spans.asked, spans.pages = nil, spans.pages[1:]
	spans.mu.Unlock()
	run2 := "run-2"
	resp, err = get(svc, f.Owner, &pb.GetChatTraceRequest{ChatId: f.ChatID, RunId: &run2})
	require.NoError(t, err)
	require.Len(t, resp.GetSpans(), 1)
	assert.Equal(t, "c", resp.GetSpans()[0].GetSpanId())
	require.Len(t, resp.GetModelCalls(), 1)
	assert.Equal(t, "run2", resp.GetModelCalls()[0].GetId())

	// Always truncated, never moving: cut off, SPANS_TRUNCATED.
	stuck := &fakeSpans{}
	f2, svc2 := setup(t, stuck)
	page := controlplane.ConversationTrace{Spans: []controlplane.ConversationSpan{chatSpan(f2, "x", "", "run-1", t1)}, Truncated: true}
	stuck.pages = []controlplane.ConversationTrace{page, page, page}
	resp, err = get(svc2, f2.Owner, &pb.GetChatTraceRequest{ChatId: f2.ChatID})
	require.NoError(t, err)
	assert.Len(t, resp.GetSpans(), 1)
	assert.Contains(t, resp.GetPartialReasons(), pb.ChatTracePartialReason_CHAT_TRACE_PARTIAL_REASON_SPANS_TRUNCATED)
	assert.Len(t, stuck.queries(), 2, "a page that adds nothing ends the paging")
}

// Cost totals keep each basis apart, and a call another person paid for is
// unknown to the owner, never shown.
func TestGetChatTrace_CostTotals(t *testing.T) {
	f, svc := setup(t, &fakeSpans{err: errors.New("down")})
	at := chatCreated.Add(time.Minute)
	seedCalls(t, f,
		call{id: "reported", basis: "reported", cost: nanos(100), at: at},
		call{id: "estimated", basis: "estimated", cost: nanos(40), at: at},
		call{id: "unpriced", basis: "unpriced", at: at},
		call{id: "unrecorded", at: at, outcome: "error", errorClass: "timeout"},
		call{id: "someone-else", user: f.ViewerID, basis: "reported", cost: nanos(7), at: at},
	)
	resp, err := get(svc, f.Owner, &pb.GetChatTraceRequest{ChatId: f.ChatID})
	require.NoError(t, err)
	tot := resp.GetTotals()
	assert.Equal(t, int64(5), tot.GetCalls())
	assert.Equal(t, int64(1), tot.GetErrorCalls())
	assert.Equal(t, int64(500), tot.GetInputTokens())
	assert.Equal(t, int64(100), tot.GetReportedCostUsdNanos())
	assert.Equal(t, int64(1), tot.GetReportedCalls())
	assert.Equal(t, int64(40), tot.GetEstimatedCostUsdNanos())
	assert.Equal(t, int64(1), tot.GetEstimatedCalls())
	assert.Equal(t, int64(1), tot.GetUnpricedCalls())
	assert.Equal(t, int64(2), tot.GetUnknownCostCalls())
	for _, c := range resp.GetModelCalls() {
		if c.GetId() == "someone-else" {
			assert.Nil(t, c.CostUsdNanos, "another person's spend")
			assert.Empty(t, c.GetCostBasis())
		}
		if c.GetId() == "unrecorded" {
			assert.Nil(t, c.CostUsdNanos, "NULL cost is unknown, never zero")
		}
	}
}
