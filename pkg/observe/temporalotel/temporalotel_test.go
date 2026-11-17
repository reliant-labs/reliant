package temporalotel

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

func pingActivity(context.Context) (string, error) { return "pong", nil }

func pingWorkflow(ctx workflow.Context) (string, error) {
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
	var out string
	err := workflow.ExecuteActivity(ctx, pingActivity).Get(ctx, &out)
	return out, err
}

// The workflow and its activity are one trace, with the activity nested under
// the workflow — the gap reliant's hand-rolled interceptor left (every
// activity span was a root).
func TestWorkflowAndActivityShareOneTrace(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prevTP, prevProp := otel.GetTracerProvider(), otel.GetTextMapPropagator()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	})

	ti, err := NewInterceptor(Options{})
	if err != nil {
		t.Fatal(err)
	}
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	env.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{ti}})
	env.RegisterWorkflow(pingWorkflow)
	env.RegisterActivity(pingActivity)
	env.ExecuteWorkflow(pingWorkflow)
	if !env.IsWorkflowCompleted() || env.GetWorkflowError() != nil {
		t.Fatalf("workflow did not complete: %v", env.GetWorkflowError())
	}

	var wf, act sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		switch {
		case strings.HasPrefix(s.Name(), "RunWorkflow:"):
			wf = s
		case strings.HasPrefix(s.Name(), "RunActivity:"):
			act = s
		}
	}
	if wf == nil || act == nil {
		names := []string{}
		for _, s := range rec.Ended() {
			names = append(names, s.Name())
		}
		t.Fatalf("missing workflow or activity span: %v", names)
	}
	if act.SpanContext().TraceID() != wf.SpanContext().TraceID() {
		t.Error("activity span is in a different trace from its workflow")
	}
	if got := act.InstrumentationScope().Name; got != TracerName {
		t.Errorf("scope = %q, want %q", got, TracerName)
	}
}

func TestTracerOptionsUseTheGlobalPropagator(t *testing.T) {
	o := tracerOptions(Options{DisableQueryTracing: true})
	if o.TextMapPropagator == nil || !o.AllowInvalidParentSpans || !o.DisableQueryTracing {
		t.Errorf("unexpected options: %+v", o)
	}
}
