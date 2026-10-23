// Copyright (c) 2025 Reliant Labs
package telemetry

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestBindUserReachesOuterLayersAndSpan(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))

	outer, span := tp.Tracer("t").Start(context.Background(), "rpc")
	outer = WithRequestScope(outer)
	assert.Empty(t, UserIDFromContext(outer))

	inner := BindUser(outer, "user-1")
	span.End()

	assert.Equal(t, "user-1", UserIDFromContext(inner))
	assert.Equal(t, "user-1", UserIDFromContext(outer), "a layer outside the authenticator sees the user it bound")
	ended := recorder.Ended()
	if assert.Len(t, ended, 1) {
		assert.Contains(t, ended[0].Attributes(), attribute.String(AttrUserID, "user-1"))
	}
}

func TestRequestsDoNotShareAUser(t *testing.T) {
	a := WithRequestScope(context.Background())
	b := WithRequestScope(context.Background())
	BindUser(a, "user-a")
	BindUser(b, "user-b")
	assert.Equal(t, "user-a", UserIDFromContext(a))
	assert.Equal(t, "user-b", UserIDFromContext(b))
}

func TestBindUserWithoutScopeOrIDIsSafe(t *testing.T) {
	ctx := context.Background()
	assert.Equal(t, ctx, BindUser(ctx, ""))
	assert.Equal(t, "u", UserIDFromContext(BindUser(ctx, "u")))
}

func TestLogEventCarriesItsUser(t *testing.T) {
	event := logEventToSentry(LogEvent{Message: "boom", UserID: "user-7"})
	assert.Equal(t, "user-7", event.User.ID)
	assert.Empty(t, logEventToSentry(LogEvent{Message: "boom"}).User.ID)
}
