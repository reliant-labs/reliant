// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/machinepool"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/telemetry"
)

// FireSpan is the span of one firing attempt: "trigger.fire <event kind>".
// Every attempt that writes or settles a trigger_events row opens one, so a
// trigger that stopped producing runs can be read on a timeline as well as in
// the event history: launched, skipped, failed, or still pending, and why.
// That covers every fire path: a schedule's fire (Firer), an inbound event's
// fire (EventFirer), an event settled at intake (Intake.Accept), a workflow
// event's dispatch (workflowevent), and the redriver's and capacity queue's
// settles of events nobody is firing (traceSettled).
//
// The span is the root of a trace of its own, linked to the span that caused
// the attempt (the fire activity, a webhook request, a run event's dispatch).
// One cause fans out to triggers of other orgs, so a firing never joins its
// cause's trace. The run it launches starts a trace of its own as well, its
// workflow span linking back here (launch.Spec.FiringTraceParent).
//
// What the span learns is collected as the attempt goes and set when it
// ends, so a trigger re-projected onto its declaration names the workflow
// that ran. Every method is a no-op on nil.
type FireSpan struct {
	span    trace.Span
	kind    core.TriggerEventKind
	trigger *core.Trigger
	dedupe  string
	origin  string
	resumed bool
	verdict FireVerdict
}

// FireVerdict is what an attempt left on its event. Empty fields keep what an
// earlier Record set.
type FireVerdict struct {
	Outcome core.TriggerEventOutcome
	// Detail is the event's outcome_detail. It never reaches the span: only
	// its class does (AttrTriggerReason, fireReason).
	Detail  string
	EventID string
	ChatID  string
	RunID   string
}

type fireSpanKey struct{}

// Resolved per span, not cached: a package-level tracer taken before the
// process installs its provider would stay a no-op.
func fireTracer() trace.Tracer {
	return otel.Tracer("github.com/reliant-labs/reliant/internal/triggers")
}

// StartFireSpan opens the span of one firing attempt of an event of kind.
// The returned ctx carries it (FireSpanFrom) as the current span, so a launch
// made from it links its run here; end it exactly once with End.
func StartFireSpan(ctx context.Context, kind core.TriggerEventKind) (context.Context, *FireSpan) {
	return startFireSpan(ctx, kind)
}

func startFireSpan(ctx context.Context, kind core.TriggerEventKind, opts ...trace.SpanStartOption) (context.Context, *FireSpan) {
	opts = append(opts, trace.WithNewRoot(), trace.WithSpanKind(trace.SpanKindInternal))
	if cause := trace.SpanContextFromContext(ctx); cause.IsValid() {
		opts = append(opts, trace.WithLinks(trace.Link{SpanContext: cause}))
	}
	ctx, span := fireTracer().Start(ctx, telemetry.SpanNameTriggerFire+" "+string(kind), opts...)
	s := &FireSpan{span: span, kind: kind}
	return context.WithValue(ctx, fireSpanKey{}, s), s
}

// FireSpanFrom is the firing span ctx carries, nil when none.
func FireSpanFrom(ctx context.Context) *FireSpan {
	s, _ := ctx.Value(fireSpanKey{}).(*FireSpan)
	return s
}

// traceSettled records, as a span of its own, an event settled outside any
// firing attempt: by the redriver, or in bulk by the capacity queue (expired,
// superseded), or at intake (Intake.Accept). trigger may be nil (deleted, or
// not loaded). ctx is the cause the span links to; opts may set its start.
func traceSettled(ctx context.Context, trigger *core.Trigger, ev *core.TriggerEvent, outcome core.TriggerEventOutcome, detail string, opts ...trace.SpanStartOption) {
	if ev == nil {
		return
	}
	_, span := startFireSpan(ctx, ev.Kind, opts...)
	span.Trigger(trigger)
	span.Event(ev)
	span.Record(FireVerdict{Outcome: outcome, Detail: detail})
	span.End(nil)
}

// triggerReader reads the trigger a settled event names, for its org.
type triggerReader interface {
	GetTrigger(ctx context.Context, id string) (*core.Trigger, error)
}

// settledTrigger is the trigger ev names, read through reader once per id
// (known caches the reads); nil when ev names none or it cannot be read. A
// span without its trigger is still the record of the settle, so a failed
// read is not an error.
func settledTrigger(ctx context.Context, reader triggerReader, known map[string]*core.Trigger, ev *core.TriggerEvent) *core.Trigger {
	if ev.TriggerID == nil || *ev.TriggerID == "" {
		return nil
	}
	id := *ev.TriggerID
	if t, ok := known[id]; ok {
		return t
	}
	var t *core.Trigger
	if reader != nil {
		if loaded, err := reader.GetTrigger(ctx, id); err == nil {
			t = loaded
		}
	}
	known[id] = t
	return t
}

// Trigger names the trigger being fired; a later call replaces it.
func (s *FireSpan) Trigger(t *core.Trigger) {
	if s != nil && t != nil {
		s.trigger = t
	}
}

// Event names the trigger_events row being fired: its id, dedupe key and,
// when Reliant caused it and it is the owner's to see, the run that did.
func (s *FireSpan) Event(ev *core.TriggerEvent) {
	if s == nil || ev == nil {
		return
	}
	s.DedupeKey(ev.DedupeKey)
	s.Record(FireVerdict{EventID: ev.ID})
	if o := ev.Origin; o != nil && o.CausedByReliant && !o.Foreign {
		s.OriginRunID(o.RunID)
	}
}

// DedupeKey records the event's dedupe key, hashed.
func (s *FireSpan) DedupeKey(key string) {
	if s != nil && key != "" {
		s.dedupe = key
	}
}

// OriginRunID records the run that caused the event.
func (s *FireSpan) OriginRunID(id string) {
	if s != nil && id != "" {
		s.origin = id
	}
}

// Resumed marks an attempt that continues an event already launched.
func (s *FireSpan) Resumed() {
	if s != nil {
		s.resumed = true
	}
}

// Record merges v into what the attempt left on its event.
func (s *FireSpan) Record(v FireVerdict) {
	if s == nil {
		return
	}
	if v.Outcome != "" {
		s.verdict.Outcome, s.verdict.Detail = v.Outcome, v.Detail
	}
	if v.EventID != "" {
		s.verdict.EventID = v.EventID
	}
	if v.ChatID != "" {
		s.verdict.ChatID = v.ChatID
	}
	if v.RunID != "" {
		s.verdict.RunID = v.RunID
	}
}

// RecordOutput records a fire activity's result.
func (s *FireSpan) RecordOutput(out *FireOutput) {
	if out == nil {
		return
	}
	s.Record(FireVerdict{Outcome: core.TriggerEventOutcome(out.Outcome), Detail: out.Reason, EventID: out.EventID, ChatID: out.ChatID})
}

// TraceParent is the span's context as a W3C traceparent, for the run it
// launches to link to; "" when there is no span to link.
func (s *FireSpan) TraceParent() string {
	if s == nil {
		return ""
	}
	return telemetry.TraceParentOf(s.span.SpanContext())
}

// End sets what the attempt learned and ends the span. Its status is Error
// when the event was settled failed, or when the attempt ended in err before
// recording any outcome (it is retried); a skip or a wait is not an error.
// The status description is a code, never err's text: an error can carry a
// response body or a name.
func (s *FireSpan) End(err error) {
	if s == nil {
		return
	}
	s.span.SetAttributes(s.attributes()...)
	switch {
	case s.verdict.Outcome == core.TriggerEventFailed:
		s.span.SetStatus(codes.Error, string(core.TriggerEventFailed))
	case s.verdict.Outcome == "" && err != nil:
		switch {
		case errors.Is(err, context.Canceled):
			s.span.SetAttributes(attribute.String(telemetry.AttrErrorType, telemetry.ErrorTypeCanceled))
		case errors.Is(err, context.DeadlineExceeded):
			s.span.SetAttributes(attribute.String(telemetry.AttrErrorType, "deadline_exceeded"))
			s.span.SetStatus(codes.Error, "deadline_exceeded")
		default:
			s.span.SetAttributes(attribute.String(telemetry.AttrErrorType, "error"))
			s.span.SetStatus(codes.Error, "error")
		}
	}
	s.span.End()
}

func (s *FireSpan) attributes() []attribute.KeyValue {
	attrs := []attribute.KeyValue{attribute.String(telemetry.AttrTriggerEventKind, string(s.kind))}
	str := func(key, value string) {
		if value != "" {
			attrs = append(attrs, attribute.String(key, value))
		}
	}
	if t := s.trigger; t != nil {
		str(telemetry.AttrTriggerID, t.ID)
		str(telemetry.AttrTriggerKind, string(t.Kind))
		str(telemetry.AttrTriggerWorkflowRef, t.Workflow)
		str(telemetry.AttrUserID, t.UserID)
		str(telemetry.AttrOrgID, ptr.From(t.OrgID))
		if name := ptr.From(t.WorkflowTrigger); name != "" {
			str(telemetry.AttrTriggerActivationID, t.ID)
			str(telemetry.AttrTriggerActivationName, name)
		}
	}
	if s.dedupe != "" {
		str(telemetry.AttrTriggerDedupeKeyHash, dedupeKeyHash(s.dedupe))
	}
	str(telemetry.AttrTriggerOriginRunID, s.origin)
	str(telemetry.AttrTriggerEventID, s.verdict.EventID)
	str(telemetry.AttrTriggerOutcome, string(s.verdict.Outcome))
	str(telemetry.AttrTriggerReason, fireReason(s.verdict.Detail))
	if s.verdict.Outcome == core.TriggerEventLaunched {
		str(telemetry.AttrGenAIConversationID, s.verdict.ChatID)
		str(telemetry.AttrAIRunID, s.verdict.RunID)
	}
	if s.resumed {
		attrs = append(attrs, attribute.Bool(telemetry.AttrTriggerResumed, true))
	}
	return attrs
}

// dedupeKeyHash is AttrTriggerDedupeKeyHash's value for key.
func dedupeKeyHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}

// fireReason classifies an outcome_detail into AttrTriggerReason's closed
// vocabulary: "" for none, TriggerReasonOther for a detail it does not know.
// It matches the constants and fixed prefixes the details are written from,
// so a new detail reads as "other" until it is added here, never as its text.
func fireReason(detail string) string {
	if detail == "" {
		return ""
	}
	switch detail {
	case "trigger deleted":
		return telemetry.TriggerReasonTriggerDeleted
	case "trigger is disabled", creatorLeftOrgPausedDetail, orgPolicyPausedDetail, noOrgAccountPausedDetail, projectDeletedPausedDetail:
		return telemetry.TriggerReasonTriggerPaused
	case creatorLeftOrgNoPause:
		return telemetry.TriggerReasonCreatorLeftOrg
	case capacityWaiting:
		return telemetry.TriggerReasonCapacityWaiting
	case store.TriggerCapacitySuperseded:
		return telemetry.TriggerReasonCapacitySuperseded
	case capacityExpiredDetail:
		return telemetry.TriggerReasonExpired
	case "queued workflow capacity policy was removed":
		return telemetry.TriggerReasonCapacityPolicyRemoved
	case "already launched":
		return telemetry.TriggerReasonAlreadyLaunched
	case daemonMissingDetail:
		return telemetry.TriggerReasonDaemonMissing
	}
	for _, p := range fireReasonPrefixes {
		if strings.HasPrefix(detail, p.prefix) {
			return p.reason
		}
	}
	return telemetry.TriggerReasonOther
}

var fireReasonPrefixes = []struct{ prefix, reason string }{
	{noOrgAccountPrefix, telemetry.TriggerReasonNoOrgAccount},
	{"filter did not match", telemetry.TriggerReasonFilterNoMatch},
	{"the trigger's filter ", telemetry.TriggerReasonFilterError},
	{"filter error:", telemetry.TriggerReasonFilterError},
	{"origin_filter rejected", telemetry.TriggerReasonOriginFilterRejected},
	{"the trigger's origin_filter ", telemetry.TriggerReasonOriginFilterError},
	{"loop guard:", telemetry.TriggerReasonLoopGuard},
	{"needs setup: ", telemetry.TriggerReasonNeedsSetup},
	{"refused: the newest definition", telemetry.TriggerReasonDefinitionBroken},
	{"trigger config is invalid", telemetry.TriggerReasonConfigInvalid},
	{machinepool.WaitingPrefix, telemetry.TriggerReasonPoolWaiting},
}
