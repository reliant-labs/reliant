package controller

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

// Every reconcile is a root span named for the kind, and the user's reconcile
// runs under it — so what it calls nests instead of each being its own trace.
func TestRun_ReconcileIsARootSpan(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cm1", Namespace: "default"}}
	r := &Reconciler[*corev1.ConfigMap]{Client: fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(cm).Build()}

	// Even when the caller's ctx carries a span, the reconcile is a new trace.
	callerCtx, caller := tp.Tracer("t").Start(context.Background(), "caller")
	var inner trace.SpanContext
	_, err := r.Run(callerCtx,
		ctrl.Request{NamespacedName: types.NamespacedName{Name: "cm1", Namespace: "default"}},
		&corev1.ConfigMap{},
		func(ctx context.Context, _ *corev1.ConfigMap) (Result, error) {
			inner = trace.SpanContextFromContext(ctx)
			return Done(), errors.New("boom")
		}, nil)
	caller.End()
	if err == nil {
		t.Fatal("reconcile error was not returned")
	}

	var span sdktrace.ReadOnlySpan
	for _, s := range rec.Ended() {
		if s.Name() == "reconcile ConfigMap" {
			span = s
		}
	}
	if span == nil {
		t.Fatalf("no \"reconcile ConfigMap\" span")
	}
	if span.Parent().IsValid() || span.SpanContext().TraceID() == caller.SpanContext().TraceID() {
		t.Error("reconcile span is not a root")
	}
	if inner.SpanID() != span.SpanContext().SpanID() {
		t.Error("the user's reconcile does not run under the reconcile span")
	}
	if span.Status().Code != codes.Error {
		t.Error("reconcile error not recorded on the span")
	}
}
