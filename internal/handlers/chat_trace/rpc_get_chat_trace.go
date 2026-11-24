package chat_trace

import (
	"context"
	"errors"
	"sort"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/authz"
	"github.com/reliant-labs/reliant/internal/controlplane"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/reliant-labs/reliant/internal/store"
)

const (
	// spanStoreTTL is how long the span store keeps a trace, and the longest
	// window one span read may ask for.
	spanStoreTTL = controlplane.MaxConversationTraceWindow
	// spanStartSlack reads spans from a little before the chat row was
	// written: the worker's clock and the database's are not the same.
	spanStartSlack = time.Minute
	// maxSpanPages bounds the span reads one trace makes (control-plane rate
	// limits each org to 60 a minute); maxSpans bounds the spans it answers.
	maxSpanPages = 5
	maxSpans     = 10000
	// maxModelCalls bounds the model calls one trace answers.
	maxModelCalls = 5000
)

// Span attributes the span store answers that a run's scope stamps; a span
// is the chat's only when both are the chat's own.
const (
	attrOrgID          = "org.id"
	attrConversationID = "gen_ai.conversation.id"
	attrRunID          = "forge.ai.run.id"
	attrRootRunID      = "forge.ai.root_run_id"
)

// viewerSpanAttributes are the only span attributes a viewer of a shared chat
// reads: what the transcript already shows them (the model, the tool's name,
// the node) and the token counts (agent-analytics.md §2.2, D4). Never a cost,
// an id, or a reliant.* key. An allowlist, so a key the span store learns
// later reaches the owner and never a viewer.
var viewerSpanAttributes = map[string]bool{
	"gen_ai.operation.name":                    true,
	"gen_ai.provider.name":                     true,
	"gen_ai.request.model":                     true,
	"gen_ai.response.model":                    true,
	"gen_ai.usage.input_tokens":                true,
	"gen_ai.usage.output_tokens":               true,
	"gen_ai.usage.cache_read.input_tokens":     true,
	"gen_ai.usage.cache_creation.input_tokens": true,
	"gen_ai.usage.reasoning.output_tokens":     true,
	"gen_ai.tool.name":                         true,
	"gen_ai.tool.type":                         true,
	"gen_ai.agent.name":                        true,
	"forge.ai.workflow.name":                   true,
	"forge.ai.node.id":                         true,
	"forge.ai.node.type":                       true,
}

// GetChatTrace answers one chat's spans and model calls, shaped for how the
// caller reads the chat.
func (s *Service) GetChatTrace(
	ctx context.Context,
	req *connect.Request[pb.GetChatTraceRequest],
) (*connect.Response[pb.GetChatTraceResponse], error) {
	userID := auth.MustGetUserID(ctx)
	// Serves viewers of a shared chat: no cost, no ids, no reliant.*
	// attribute (viewerSpan, viewerCall). Reads only: the chat's unread
	// flag and last_active are untouched.
	ctx = authz.WithViewerReads(ctx)

	msg := req.Msg
	if msg.GetChatId() == "" {
		return nil, svcerr.InvalidArgument("chat_id is required")
	}
	if msg.StartTime != nil && msg.EndTime != nil && !msg.EndTime.AsTime().After(msg.StartTime.AsTime()) {
		return nil, svcerr.InvalidArgument("end_time must be after start_time")
	}

	chat, err := s.deps.Store.GetChatRow(ctx, msg.GetChatId())
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to get chat"))
	}
	access, denied := authz.ReadChatAccess(ctx, userID, chat)
	if denied != nil {
		return nil, svcerr.NotFound("chat")
	}
	viewer := access.Viewer()

	now := s.now()
	end := now
	if msg.EndTime != nil && msg.EndTime.AsTime().Before(now) {
		end = msg.EndTime.AsTime().UTC()
	}
	// from is the window's start as asked (zero: unbounded); oldest is where
	// the chat's own history can begin.
	var from time.Time
	oldest := chat.CreatedAt.UTC()
	if msg.StartTime != nil {
		from = msg.StartTime.AsTime().UTC()
		if from.After(oldest) {
			oldest = from
		}
	}
	reasons := map[pb.ChatTracePartialReason]bool{}
	resp := &pb.GetChatTraceResponse{ChatId: chat.ID, Viewer: viewer, AsOf: timestamppb.New(now)}

	// The cost half: llm_call_usage, ninety days.
	if oldest.Before(now.Add(-db.LLMCallUsageRetention)) {
		reasons[pb.ChatTracePartialReason_CHAT_TRACE_PARTIAL_REASON_CALLS_EXPIRED] = true
	}
	rows, err := s.deps.Store.ListChatLLMCalls(ctx, db.ChatLLMCallQuery{
		ChatID: chat.ID, From: from, To: end, RunID: msg.GetRunId(), Limit: maxModelCalls + 1,
	})
	if err != nil {
		return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list model calls"))
	}
	if len(rows) > maxModelCalls {
		rows = rows[:maxModelCalls]
		reasons[pb.ChatTracePartialReason_CHAT_TRACE_PARTIAL_REASON_CALLS_TRUNCATED] = true
	}
	resp.ModelCalls, resp.Totals = modelCalls(rows, chat.UserID, viewer)

	// The trace half: the span store, seven days. Clock slack applies only
	// to the inferred chat boundary, never to the caller's explicit window.
	spanFrom := chat.CreatedAt.UTC().Add(-spanStartSlack)
	if from.After(spanFrom) {
		spanFrom = from
	}
	if expiry := now.Add(-spanStoreTTL); spanFrom.Before(expiry) {
		spanFrom = expiry
		if oldest.Before(expiry) {
			reasons[pb.ChatTracePartialReason_CHAT_TRACE_PARTIAL_REASON_TRACE_EXPIRED] = true
		}
	}
	if end.After(spanFrom) {
		resp.SpansStartTime, resp.SpansEndTime = timestamppb.New(spanFrom), timestamppb.New(end)
		spans, truncated, err := s.readSpans(ctx, chat, spanFrom, end)
		if err != nil {
			if ctx.Err() != nil {
				return nil, svcerr.Wrap(ctx.Err())
			}
			s.deps.Logger.WarnContext(ctx, "chat trace: the span store could not be read; answering without spans",
				"chat_id", chat.ID, "error", err)
			reasons[pb.ChatTracePartialReason_CHAT_TRACE_PARTIAL_REASON_SPANS_UNAVAILABLE] = true
		}
		if truncated {
			reasons[pb.ChatTracePartialReason_CHAT_TRACE_PARTIAL_REASON_SPANS_TRUNCATED] = true
		}
		resp.Spans = chatSpans(spans, msg.GetRunId(), viewer)
		if err == nil && len(resp.Spans) == 0 && callsBetween(rows, spanFrom, end) {
			reasons[pb.ChatTracePartialReason_CHAT_TRACE_PARTIAL_REASON_SPANS_MISSING] = true
		}
	}

	resp.Completeness = pb.ChatTraceCompleteness_CHAT_TRACE_COMPLETENESS_COMPLETE
	for reason := range reasons {
		resp.PartialReasons = append(resp.PartialReasons, reason)
	}
	if len(resp.PartialReasons) > 0 {
		sort.Slice(resp.PartialReasons, func(i, j int) bool { return resp.PartialReasons[i] < resp.PartialReasons[j] })
		resp.Completeness = pb.ChatTraceCompleteness_CHAT_TRACE_COMPLETENESS_PARTIAL
	}
	return connect.NewResponse(resp), nil
}

// errNoSpanStore is a deployment with no span store to ask, or a chat with no
// org (the span store keys every span by its org).
var errNoSpanStore = errors.New("chat trace: no span store for this chat")

// readSpans pages through the chat's spans in [from, end): every span the
// store answers for the chat's org and id, oldest first, at most maxSpans of
// them over at most maxSpanPages reads. truncated: more matched. An error
// keeps what was read before it.
func (s *Service) readSpans(ctx context.Context, chat *db.ChatRow, from, end time.Time) (spans []controlplane.ConversationSpan, truncated bool, err error) {
	orgID := ptr.From(chat.OrgID)
	if s.deps.Spans == nil || orgID == "" {
		return nil, false, errNoSpanStore
	}
	type spanKey struct{ trace, span string }
	seen := map[spanKey]bool{}
	for page := 0; page < maxSpanPages; page++ {
		got, err := s.deps.Spans.GetConversationTrace(ctx, controlplane.ConversationTraceQuery{
			OrgID: orgID, ConversationID: chat.ID, Start: from, End: end,
		})
		if err != nil {
			return spans, false, err
		}
		added := 0
		for _, span := range got.Spans {
			key := spanKey{span.TraceID, span.SpanID}
			if seen[key] {
				continue
			}
			seen[key] = true
			// The store matched both; asserted again, so a store that ever
			// answered another org's or chat's span is ignored, not shown.
			if span.Attributes[attrOrgID] != orgID || span.Attributes[attrConversationID] != chat.ID {
				s.deps.Logger.WarnContext(ctx, "chat trace: the span store answered a span of another org or chat; dropped",
					"chat_id", chat.ID, "span_id", span.SpanID)
				continue
			}
			spans = append(spans, span)
			added++
		}
		if !got.Truncated {
			return spans, false, nil
		}
		// Ask again from the last span's start; a page that added nothing
		// (every span of it started at once) cannot move forward.
		if added == 0 || len(spans) >= maxSpans || len(got.Spans) == 0 {
			break
		}
		from = got.Spans[len(got.Spans)-1].Start
	}
	if len(spans) > maxSpans {
		spans = spans[:maxSpans]
	}
	return spans, true, nil
}

// chatSpans is spans as the caller reads them, of run runID when set, oldest
// first.
func chatSpans(spans []controlplane.ConversationSpan, runID string, viewer bool) []*pb.ChatTraceSpan {
	out := make([]*pb.ChatTraceSpan, 0, len(spans))
	for _, span := range spans {
		if runID != "" && span.Attributes[attrRunID] != runID && span.Attributes[attrRootRunID] != runID {
			continue
		}
		if viewer {
			out = append(out, viewerSpan(span))
		} else {
			out = append(out, ownerSpan(span))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].GetStartTime().AsTime(), out[j].GetStartTime().AsTime()
		if !a.Equal(b) {
			return a.Before(b)
		}
		return out[i].GetSpanId() < out[j].GetSpanId()
	})
	return out
}

// ownerSpan is the whole span, as the span store's allowlist answers it.
func ownerSpan(span controlplane.ConversationSpan) *pb.ChatTraceSpan {
	out := spanShape(span)
	out.TraceId = span.TraceID
	out.Attributes = span.Attributes
	for _, e := range span.Events {
		out.Events = append(out.Events, &pb.ChatTraceSpanEvent{Name: e.Name, Time: timestamppb.New(e.Time), Attributes: e.Attributes})
	}
	for _, l := range span.Links {
		out.Links = append(out.Links, &pb.ChatTraceSpanLink{TraceId: l.TraceID, SpanId: l.SpanID})
	}
	return out
}

// viewerSpan is a stubbed span: its structure, timing and status, and only
// viewerSpanAttributes; no trace id, no link, no event attribute.
func viewerSpan(span controlplane.ConversationSpan) *pb.ChatTraceSpan {
	out := spanShape(span)
	for k, v := range span.Attributes {
		if viewerSpanAttributes[k] {
			if out.Attributes == nil {
				out.Attributes = map[string]string{}
			}
			out.Attributes[k] = v
		}
	}
	for _, e := range span.Events {
		out.Events = append(out.Events, &pb.ChatTraceSpanEvent{Name: e.Name, Time: timestamppb.New(e.Time)})
	}
	return out
}

// spanShape is what every reader sees of a span: its place in the tree, its
// name, kind and service, its timing and status.
func spanShape(span controlplane.ConversationSpan) *pb.ChatTraceSpan {
	return &pb.ChatTraceSpan{
		SpanId:       span.SpanID,
		ParentSpanId: span.ParentSpanID,
		Name:         span.Name,
		Kind:         span.Kind,
		ServiceName:  span.ServiceName,
		StartTime:    timestamppb.New(span.Start),
		DurationNs:   span.DurationNs,
		StatusCode:   span.StatusCode,
	}
}

// Cost bases (llm_call_usage.cost_basis).
const (
	costReported  = "reported"
	costEstimated = "estimated"
	costUnpriced  = "unpriced"
)

// modelCalls is rows as the caller reads them and their totals. Cost is the
// owner's only, and only for the calls the owner made: a call another person
// paid for is unknown, never shown. Reported and estimated cost are summed
// apart.
func modelCalls(rows []*db.ChatLLMCall, ownerID string, viewer bool) ([]*pb.ChatModelCall, *pb.ChatTraceTotals) {
	calls := make([]*pb.ChatModelCall, 0, len(rows))
	totals := &pb.ChatTraceTotals{}
	for _, row := range rows {
		call := &pb.ChatModelCall{
			Attempt:          row.Attempt,
			Time:             timestamppb.New(row.CreatedAt),
			Purpose:          row.Purpose,
			Provider:         row.Provider,
			Model:            row.Model,
			Outcome:          row.Outcome,
			ErrorClass:       row.ErrorClass,
			InputTokens:      row.InputTokens,
			OutputTokens:     row.OutputTokens,
			CacheReadTokens:  row.CacheReadTokens,
			CacheWriteTokens: row.CacheWriteTokens,
			LatencyMs:        row.LatencyMs,
			TtftMs:           row.TtftMs,
			NodeId:           ptr.From(row.NodeID),
			WorkflowName:     ptr.From(row.WorkflowName),
		}
		totals.Calls++
		if row.Outcome == store.LLMOutcomeError {
			totals.ErrorCalls++
		}
		totals.InputTokens += row.InputTokens
		totals.OutputTokens += row.OutputTokens
		totals.CacheReadTokens += row.CacheReadTokens
		totals.CacheWriteTokens += row.CacheWriteTokens
		totals.LatencyMsTotal += row.LatencyMs
		if !viewer {
			call.Id = row.ID
			call.RunId = ptr.From(row.AiRunID)
			call.AuthMode = row.AuthMode
			if row.UserID == ownerID {
				call.CostBasis = ptr.From(row.CostBasis)
				call.CostUsdNanos = row.CostUsdNanos
			}
			switch {
			case call.CostBasis == costReported && call.CostUsdNanos != nil:
				totals.ReportedCalls++
				totals.ReportedCostUsdNanos += *call.CostUsdNanos
			case call.CostBasis == costEstimated && call.CostUsdNanos != nil:
				totals.EstimatedCalls++
				totals.EstimatedCostUsdNanos += *call.CostUsdNanos
			case call.CostBasis == costUnpriced:
				totals.UnpricedCalls++
			default:
				totals.UnknownCostCalls++
			}
		}
		calls = append(calls, call)
	}
	return calls, totals
}

// callsBetween reports whether any of rows was made in [from, end).
func callsBetween(rows []*db.ChatLLMCall, from, end time.Time) bool {
	for _, row := range rows {
		if !row.CreatedAt.Before(from) && row.CreatedAt.Before(end) {
			return true
		}
	}
	return false
}
