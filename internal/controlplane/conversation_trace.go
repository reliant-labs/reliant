package controlplane

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ConversationTraceInternalService (control-plane), withheld from the
// exported contract like LLMPriceInternalService: Connect unary over plain
// HTTP+JSON, signed with this process's internal-service identity.
const getConversationTracePath = "/controlplane.v1.ConversationTraceInternalService/GetConversationTrace"

// MaxConversationTraceWindow is the longest window one GetConversationTrace
// may ask for: the span store's trace TTL (control-plane's
// conversation_trace_internal.MaxWindow).
const MaxConversationTraceWindow = 7 * 24 * time.Hour

// maxConversationTraceBytes bounds one answer: at most 2,000 spans, each a
// few allowlisted attributes, comes to a few MiB.
const maxConversationTraceBytes = 32 << 20

// ErrConversationTraceUnsupported is a control plane without
// ConversationTraceInternalService (an older release), or an adapter with no
// internal-service identity to call it with.
var ErrConversationTraceUnsupported = errors.New("controlplane: conversation traces are not available")

// ConversationTraceQuery names one chat's spans: the chat's org and id, and a
// window [Start, End) of at most MaxConversationTraceWindow.
type ConversationTraceQuery struct {
	OrgID, ConversationID string
	Start, End            time.Time
}

// ConversationTrace is one page of a conversation's spans, oldest first.
// Truncated: more matched than were returned.
type ConversationTrace struct {
	Spans     []ConversationSpan
	Truncated bool
}

// ConversationSpan is one span as control-plane returns it: only its
// allowlisted attributes and events.
type ConversationSpan struct {
	TraceID, SpanID, ParentSpanID string
	Name, Kind, ServiceName       string
	Start                         time.Time
	DurationNs                    int64
	StatusCode                    string
	Attributes                    map[string]string
	Events                        []ConversationSpanEvent
	Links                         []ConversationSpanLink
}

// ConversationSpanEvent is one allowlisted span event.
type ConversationSpanEvent struct {
	Name       string
	Time       time.Time
	Attributes map[string]string
}

// ConversationSpanLink is another span's ids.
type ConversationSpanLink struct {
	TraceID, SpanID string
}

// GetConversationTrace is Service.GetConversationTrace.
func (c *client) GetConversationTrace(ctx context.Context, q ConversationTraceQuery) (ConversationTrace, error) {
	if q.OrgID == "" || q.ConversationID == "" {
		return ConversationTrace{}, errors.New("controlplane: a conversation trace needs an org and a conversation")
	}
	if !q.End.After(q.Start) || q.End.Sub(q.Start) > MaxConversationTraceWindow {
		return ConversationTrace{}, fmt.Errorf("controlplane: a conversation trace window must end after it starts and span at most %s", MaxConversationTraceWindow)
	}
	in := map[string]any{
		"orgId":          q.OrgID,
		"conversationId": q.ConversationID,
		"startTime":      q.Start.UTC().Format(time.RFC3339Nano),
		"endTime":        q.End.UTC().Format(time.RFC3339Nano),
	}
	// int64 fields travel as JSON strings (proto3 JSON); empty fields are
	// omitted.
	var out struct {
		Spans []struct {
			TraceID      string            `json:"traceId"`
			SpanID       string            `json:"spanId"`
			ParentSpanID string            `json:"parentSpanId"`
			Name         string            `json:"name"`
			Kind         string            `json:"kind"`
			ServiceName  string            `json:"serviceName"`
			StartTime    *time.Time        `json:"startTime"`
			DurationNs   json64            `json:"durationNs"`
			StatusCode   string            `json:"statusCode"`
			Attributes   map[string]string `json:"attributes"`
			Events       []struct {
				Name       string            `json:"name"`
				Time       *time.Time        `json:"time"`
				Attributes map[string]string `json:"attributes"`
			} `json:"events"`
			Links []struct {
				TraceID string `json:"traceId"`
				SpanID  string `json:"spanId"`
			} `json:"links"`
		} `json:"spans"`
		Truncated bool `json:"truncated"`
	}
	err := c.internalCall(ctx, getConversationTracePath, in, &out, maxConversationTraceBytes)
	if errors.Is(err, ErrProjectOrgMoveUnsupported) {
		return ConversationTrace{}, ErrConversationTraceUnsupported
	}
	if err != nil {
		return ConversationTrace{}, err
	}
	trace := ConversationTrace{Spans: make([]ConversationSpan, 0, len(out.Spans)), Truncated: out.Truncated}
	for _, s := range out.Spans {
		span := ConversationSpan{
			TraceID: s.TraceID, SpanID: s.SpanID, ParentSpanID: s.ParentSpanID,
			Name: s.Name, Kind: s.Kind, ServiceName: s.ServiceName,
			DurationNs: int64(s.DurationNs), StatusCode: s.StatusCode, Attributes: s.Attributes,
		}
		if s.StartTime != nil {
			span.Start = s.StartTime.UTC()
		}
		for _, e := range s.Events {
			ev := ConversationSpanEvent{Name: e.Name, Attributes: e.Attributes}
			if e.Time != nil {
				ev.Time = e.Time.UTC()
			}
			span.Events = append(span.Events, ev)
		}
		for _, l := range s.Links {
			span.Links = append(span.Links, ConversationSpanLink{TraceID: l.TraceID, SpanID: l.SpanID})
		}
		trace.Spans = append(trace.Spans, span)
	}
	return trace, nil
}
