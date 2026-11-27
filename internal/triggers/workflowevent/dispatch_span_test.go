// Copyright (c) 2025 Reliant Labs
package workflowevent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/telemetry"
	"github.com/reliant-labs/reliant/internal/triggers/runevents"
)

// Each trigger a run event matches is a trigger.fire span of its own: a root
// linked to the dispatch, naming the source run, whether it launched or was
// skipped. A launched run links to its trigger's span, not to the dispatch.
func TestDispatch_EachMatchedTriggerIsAFireSpan(t *testing.T) {
	f := newFixture(t)
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	src := Source{Workflows: []string{"code-review"}, Outcomes: []string{"finished"}}
	launches := f.addTrigger(f.userID, f.projectID, "on-review-done", "builtin://agent", src)
	skips := f.addFilteredTrigger(f.userID, f.projectID, "never", "builtin://agent", src, `trigger.payload.outcome == "failed"`)
	source := f.humanRun("code-review")
	ev := f.finish(source, core.RunEventFinished)

	ctx, dispatch := otel.Tracer("test").Start(context.Background(), "dispatch")
	results, err := f.dispatcher.Dispatch(ctx, ev)
	dispatch.End()
	require.NoError(t, err)
	launched := launchedChat(t, results, launches.ID)

	byTrigger := map[string]sdktrace.ReadOnlySpan{}
	for _, s := range rec.Ended() {
		if s.Name() != telemetry.SpanNameTriggerFire+" "+string(runevents.TriggerEventKind) {
			continue
		}
		byTrigger[spanAttrs(s)[telemetry.AttrTriggerID]] = s
	}
	require.Len(t, byTrigger, 2)
	for id, s := range byTrigger {
		assert.False(t, s.Parent().IsValid(), "%s: a root", id)
		assert.NotEqual(t, dispatch.SpanContext().TraceID(), s.SpanContext().TraceID(), "%s: not in the dispatch's trace", id)
		require.Len(t, s.Links(), 1, id)
		assert.Equal(t, dispatch.SpanContext().SpanID(), s.Links()[0].SpanContext.SpanID(), "%s: linked to the dispatch", id)
	}
	assert.NotEqual(t, byTrigger[launches.ID].SpanContext().TraceID(), byTrigger[skips.ID].SpanContext().TraceID())

	l := spanAttrs(byTrigger[launches.ID])
	assert.Equal(t, string(core.TriggerEventLaunched), l[telemetry.AttrTriggerOutcome])
	assert.Equal(t, launched, l[telemetry.AttrGenAIConversationID])
	assert.Equal(t, source, l[telemetry.AttrTriggerOriginRunID])
	assert.Equal(t, f.userID, l[telemetry.AttrUserID])
	launchEv, err := store.GetTriggerEventByChatID(ctx, f.repo.ORM(), launched)
	require.NoError(t, err)
	assert.Equal(t, launchEv.ID, l[telemetry.AttrTriggerEventID])
	root := f.starter.lastRoot()
	require.NotNil(t, root.Trigger)
	assert.Equal(t, telemetry.TraceParentOf(byTrigger[launches.ID].SpanContext()), root.Trigger.TraceParent,
		"the run links to its trigger's firing")

	sk := spanAttrs(byTrigger[skips.ID])
	assert.Equal(t, string(core.TriggerEventSkipped), sk[telemetry.AttrTriggerOutcome])
	assert.Equal(t, telemetry.TriggerReasonFilterNoMatch, sk[telemetry.AttrTriggerReason])
	assert.NotEmpty(t, sk[telemetry.AttrTriggerEventID])
	for key, value := range sk {
		assert.NotContains(t, value, "payload.outcome", "attribute %s carries no filter text", key)
	}
}

func spanAttrs(s sdktrace.ReadOnlySpan) map[attribute.Key]string {
	out := map[attribute.Key]string{}
	for _, kv := range s.Attributes() {
		out[kv.Key] = kv.Value.Emit()
	}
	return out
}
