package contractkit

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
)

// fakeSpan records the calls made to it so tests can assert behaviour
// without pulling in the full SDK.
type fakeSpan struct {
	embedded.Span
	recordedErr  error
	statusCode   codes.Code
	statusDesc   string
	endCalled    bool
	tracerStored trace.Tracer
}

func (s *fakeSpan) End(...trace.SpanEndOption)            { s.endCalled = true }
func (s *fakeSpan) AddEvent(string, ...trace.EventOption) {}
func (s *fakeSpan) AddLink(trace.Link)                    {}
func (s *fakeSpan) IsRecording() bool                     { return true }
func (s *fakeSpan) RecordError(err error, _ ...trace.EventOption) {
	s.recordedErr = err
}
func (s *fakeSpan) SpanContext() trace.SpanContext { return trace.SpanContext{} }
func (s *fakeSpan) SetStatus(c codes.Code, d string) {
	s.statusCode = c
	s.statusDesc = d
}
func (s *fakeSpan) SetName(string)                       {}
func (s *fakeSpan) SetAttributes(...attribute.KeyValue)  {}
func (s *fakeSpan) TracerProvider() trace.TracerProvider { return nil }

// fakeTracer captures the operation name and emits a fakeSpan.
type fakeTracer struct {
	embedded.Tracer
	lastName string
	lastSpan *fakeSpan
	lastCtx  context.Context
}

func (t *fakeTracer) Start(ctx context.Context, name string, _ ...trace.SpanStartOption) (context.Context, trace.Span) {
	t.lastName = name
	t.lastCtx = ctx
	s := &fakeSpan{tracerStored: t}
	t.lastSpan = s
	return ctx, s
}

func TestTraceStart_PassesNameAndContext(t *testing.T) {
	tr := &fakeTracer{}
	ctx := context.Background()
	gotCtx, span := TraceStart(ctx, tr, "Service.Send")
	if tr.lastName != "Service.Send" {
		t.Errorf("name = %q, want Service.Send", tr.lastName)
	}
	if gotCtx != ctx {
		t.Errorf("returned ctx differs from input")
	}
	if span == nil {
		t.Fatal("span is nil")
	}
}

func TestTraceStart_NilContextFallsBackToBackground(t *testing.T) {
	tr := &fakeTracer{}
	gotCtx, _ := TraceStart(nil, tr, "M")
	if gotCtx == nil {
		t.Fatal("returned ctx is nil")
	}
	if tr.lastCtx == nil {
		t.Fatal("tracer received nil ctx — should have been substituted")
	}
}

func TestRecordSpanError_Nil(t *testing.T) {
	span := &fakeSpan{}
	RecordSpanError(span, nil)
	if span.recordedErr != nil {
		t.Error("RecordSpanError(nil) should not record on span")
	}
	if span.statusCode != codes.Unset {
		t.Errorf("status code changed for nil err: %v", span.statusCode)
	}
}

func TestRecordSpanError_NonNil(t *testing.T) {
	span := &fakeSpan{}
	want := errors.New("boom")
	RecordSpanError(span, want)
	if span.recordedErr != want {
		t.Errorf("recordedErr = %v, want %v", span.recordedErr, want)
	}
	if span.statusCode != codes.Error {
		t.Errorf("statusCode = %v, want Error", span.statusCode)
	}
	if span.statusDesc != "boom" {
		t.Errorf("statusDesc = %q, want boom", span.statusDesc)
	}
}

func TestRecordSpanError_NilSpanSafe(t *testing.T) {
	RecordSpanError(nil, errors.New("x"))
}
