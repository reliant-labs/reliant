package observe

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestLogCall_SuccessRecord(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	LogCall(context.Background(), logger, "userstore.Get", time.Now(), nil)

	out := buf.String()
	if !strings.Contains(out, "msg=userstore.Get") {
		t.Errorf("missing msg=userstore.Get in %q", out)
	}
	if !strings.Contains(out, "duration=") {
		t.Errorf("missing duration in %q", out)
	}
	if strings.Contains(out, "error=") {
		t.Errorf("success record should not include error attr: %q", out)
	}
}

func TestLogCall_ErrorRecord(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))

	LogCall(context.Background(), logger, "userstore.Get", time.Now(), errors.New("not found"))

	out := buf.String()
	if !strings.Contains(out, "error=") {
		t.Errorf("error record missing error attr: %q", out)
	}
}

func TestLogCall_NilLoggerSafe(t *testing.T) {
	LogCall(context.Background(), nil, "x", time.Now(), nil)
	LogCall(context.Background(), nil, "x", time.Now(), errors.New("x"))
	// no panic = pass
}

func TestTraceCall_StartsSpanAndReturnsResult(t *testing.T) {
	tr := &fakeTracer{}
	got, err := TraceCall(context.Background(), tr, "op.Name", func(_ context.Context) (int, error) {
		return 42, nil
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != 42 {
		t.Errorf("got = %d, want 42", got)
	}
	if tr.lastName != "op.Name" {
		t.Errorf("span name = %q, want op.Name", tr.lastName)
	}
	if !tr.lastSpan.endCalled {
		t.Error("span.End not called")
	}
}

func TestTraceCall_RecordsErrorOnFailure(t *testing.T) {
	tr := &fakeTracer{}
	wantErr := errors.New("boom")
	_, err := TraceCall(context.Background(), tr, "op.Name", func(_ context.Context) (int, error) {
		return 0, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if tr.lastSpan.recordedErr != wantErr {
		t.Errorf("span.RecordError(%v) not called; recordedErr = %v", wantErr, tr.lastSpan.recordedErr)
	}
}

func TestTraceCall_NilTracerExecutesFn(t *testing.T) {
	called := false
	got, err := TraceCall(context.Background(), nil, "x", func(_ context.Context) (string, error) {
		called = true
		return "ok", nil
	})
	if err != nil || got != "ok" || !called {
		t.Errorf("nil tracer should pass through; got=%q err=%v called=%v", got, err, called)
	}
}

func TestTraceVoidCall_RecordsErrorOnFailure(t *testing.T) {
	tr := &fakeTracer{}
	wantErr := errors.New("nope")
	err := TraceVoidCall(context.Background(), tr, "v", func(_ context.Context) error {
		return wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("err mismatch")
	}
	if tr.lastSpan.recordedErr != wantErr {
		t.Errorf("span error not recorded")
	}
}

func TestNewCallMetrics_NilMeterIsNoop(t *testing.T) {
	m := NewCallMetrics(nil, "test")
	// All instruments should be nil; RecordCall must not panic.
	m.RecordCall(context.Background(), "M", time.Now(), nil)
	m.RecordCall(context.Background(), "M", time.Now(), errors.New("x"))
}

func TestNewCallMetrics_CreatesCanonicalNames(t *testing.T) {
	meter := newFakeMeter()
	NewCallMetrics(meter, "userstore")
	if got := meter.int64CounterNames; len(got) != 2 || got[0] != "userstore.calls" || got[1] != "userstore.errors" {
		t.Errorf("counter names = %v, want [userstore.calls userstore.errors]", got)
	}
	if got := meter.float64HistogramNames; len(got) != 1 || got[0] != "userstore.duration" {
		t.Errorf("histogram names = %v, want [userstore.duration]", got)
	}
}

func TestCallMetrics_RecordsCallAndDuration(t *testing.T) {
	meter := newFakeMeter()
	cm := NewCallMetrics(meter, "ns")
	cm.RecordCall(context.Background(), "Method", time.Now(), nil)
	if got := meter.counters["ns.calls"].adds; len(got) != 1 {
		t.Errorf("calls adds = %v, want [1]", got)
	}
	if got := len(meter.histograms["ns.duration"].records); got != 1 {
		t.Errorf("duration records = %d, want 1", got)
	}
}

func TestCallMetrics_RecordsError(t *testing.T) {
	meter := newFakeMeter()
	cm := NewCallMetrics(meter, "ns")
	cm.RecordCall(context.Background(), "Method", time.Now(), errors.New("boom"))
	if got := meter.counters["ns.errors"].adds; len(got) != 1 {
		t.Errorf("errors adds = %v, want [1]", got)
	}
}

func TestCallMetrics_NilReceiverSafe(t *testing.T) {
	var cm *CallMetrics
	cm.RecordCall(context.Background(), "M", time.Now(), nil) // must not panic
}
