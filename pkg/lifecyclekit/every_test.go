package lifecyclekit_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/reliant-labs/forge/pkg/lifecyclekit"
	"github.com/reliant-labs/forge/pkg/serverkit"
)

// Each run is its own root span, and a failing or panicking run does not stop
// the cadence.
func TestEveryRunsEachIterationAsARootSpan(t *testing.T) {
	rec := tracetest.NewSpanRecorder()
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec)))
	t.Cleanup(func() { otel.SetTracerProvider(prev) })

	var runs atomic.Int32
	w := lifecyclekit.Every("sync", lifecyclekit.EveryOptions{Interval: time.Millisecond, RunImmediately: true},
		func(context.Context) error {
			switch runs.Add(1) {
			case 1:
				return errors.New("transient")
			case 2:
				panic("bad run")
			}
			return nil
		})
	cw, ok := w.(serverkit.ContextWorker)
	if !ok {
		t.Fatal("Every's worker is not ctx-aware")
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- cw.RunContext(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for runs.Load() < 4 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("RunContext: %v", err)
	}
	if runs.Load() < 4 {
		t.Fatalf("cadence stopped after %d runs", runs.Load())
	}
	roots := 0
	for _, s := range rec.Ended() {
		if s.Name() == "worker.sync.run" && !s.Parent().IsValid() {
			roots++
		}
	}
	if roots < 3 {
		t.Errorf("got %d root run spans, want one per run", roots)
	}
}
