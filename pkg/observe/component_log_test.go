package observe

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"
)

// component_log_test.go — the in-process twin of log_sampling_test.go.
//
// LogMiddleware writes one record per component method call by default.
// Under a DEBUG handler — every dev stack — one method on a 10s work loop
// (domainregistry.ListConverging) was 68% of control-plane's dev log, so a
// deployment can turn success sampling on. Same policy as the RPC edge:
// every failure, every slow call, and — when sampling is on — sampled
// successes. These tests set the window in code; the default and the
// environment are log_sampling_config_test.go.

const loopMethod = "domainregistry.ListConverging"

func runComponent(t *testing.T, mw ComponentMiddleware, method string, n int, op ComponentOp) {
	t.Helper()
	chain := NewComponentChain(mw)
	for range n {
		_ = chain.Run(context.Background(), method, op)
	}
}

func ok(context.Context) error { return nil }

// TestLogMiddleware_RepeatedSuccessesAreSampled is the incident, built as the
// scaffolded observe_chain.go seam builds it, with sampling on.
func TestLogMiddleware_RepeatedSuccessesAreSampled(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	runComponent(t, LogMiddleware(jsonLogger(&buf, slog.LevelDebug), slog.LevelDebug, WithSuccessSampling(testWindow)),
		loopMethod, 100, ok)

	got := records(t, &buf, loopMethod)
	if len(got) != 1 {
		t.Fatalf("100 successful calls wrote %d records with sampling on, want 1 — the generated "+
			"decorator wraps every method, so sampling is what bounds a polled component", len(got))
	}
	if got[0]["level"] != "DEBUG" {
		t.Errorf("the sampled record keeps the seam's level, got %v", got[0]["level"])
	}
}

// TestLogMiddleware_SuppressedCountAndWindow: the record after a window
// accounts for the calls the window swallowed.
func TestLogMiddleware_SuppressedCountAndWindow(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	clock := newFakeClock()
	mw := LogMiddleware(jsonLogger(&buf, slog.LevelDebug), slog.LevelDebug, withClock(clock.Now), WithSuccessSampling(testWindow))

	runComponent(t, mw, loopMethod, 6, ok)
	clock.Advance(testWindow)
	runComponent(t, mw, loopMethod, 1, ok)

	got := records(t, &buf, loopMethod)
	if len(got) != 2 {
		t.Fatalf("want 2 records, got %d", len(got))
	}
	if n, _ := got[1]["suppressed"].(float64); n != 5 {
		t.Errorf("suppressed = %v, want 5", got[1]["suppressed"])
	}
}

// TestLogMiddleware_FailuresAreNeverSampled: each failure is an ERROR record
// with its error.
func TestLogMiddleware_FailuresAreNeverSampled(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	boom := errors.New("relation does not exist")
	runComponent(t, LogMiddleware(jsonLogger(&buf, slog.LevelDebug), slog.LevelDebug, WithSuccessSampling(testWindow)),
		loopMethod, 10, func(context.Context) error { return boom })

	got := records(t, &buf, loopMethod)
	if len(got) != 10 {
		t.Fatalf("10 failed calls wrote %d records, want 10", len(got))
	}
	for _, rec := range got {
		if rec["level"] != "ERROR" || rec["error"] != boom.Error() {
			t.Fatalf("failure record lost its level or error: %v", rec)
		}
	}
}

// TestLogMiddleware_SlowSuccessIsAlwaysLogged: a slow call is written inside
// an open window, at the seam's level.
func TestLogMiddleware_SlowSuccessIsAlwaysLogged(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	clock := newFakeClock()
	mw := LogMiddleware(jsonLogger(&buf, slog.LevelDebug), slog.LevelDebug, withClock(clock.Now), WithSuccessSampling(testWindow))

	runComponent(t, mw, loopMethod, 2, ok)
	runComponent(t, mw, loopMethod, 1, func(context.Context) error {
		clock.Advance(DefaultSlowThreshold)
		return nil
	})

	got := records(t, &buf, loopMethod)
	if len(got) != 2 || got[1]["slow"] != true {
		t.Fatalf("want the sampled record then a slow=true record, got %v", got)
	}
}

// TestLogMiddleware_Overrides: the seam's options reach the middleware —
// sampling off in code keeps one record per call even after an earlier
// option turned it on, and one method can be raised above the seam's level.
func TestLogMiddleware_Overrides(t *testing.T) {
	t.Parallel()
	t.Run("the last sampling option wins", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		runComponent(t, LogMiddleware(jsonLogger(&buf, slog.LevelDebug), slog.LevelDebug, WithSuccessSampling(time.Hour), WithSuccessSampling(0)),
			loopMethod, 4, ok)
		if got := records(t, &buf, loopMethod); len(got) != 4 {
			t.Fatalf("want 4 records with sampling off, got %d", len(got))
		}
	})
	t.Run("per-method level", func(t *testing.T) {
		t.Parallel()
		var buf bytes.Buffer
		mw := LogMiddleware(jsonLogger(&buf, slog.LevelInfo), slog.LevelDebug,
			WithSuccessLevel("billing.Charge", slog.LevelInfo))
		runComponent(t, mw, loopMethod, 1, ok)
		runComponent(t, mw, "billing.Charge", 1, ok)
		if got := records(t, &buf, loopMethod); len(got) != 0 {
			t.Errorf("a DEBUG success must stay quiet under an INFO handler, got %v", got)
		}
		if got := records(t, &buf, "billing.Charge"); len(got) != 1 || got[0]["level"] != "INFO" {
			t.Errorf("the raised method must log at INFO, got %v", got)
		}
	})
}

// TestLogMiddleware_DisabledLevelSkipsSampling: under a handler that drops
// the seam's level, a success never touches the sampler — so it cannot
// consume a window and swallow the first record once the level is enabled.
func TestLogMiddleware_DisabledLevelSkipsSampling(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	levelVar := new(slog.LevelVar)
	levelVar.Set(slog.LevelInfo)
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: levelVar}))
	mw := LogMiddleware(logger, slog.LevelDebug, WithSuccessSampling(testWindow))

	runComponent(t, mw, loopMethod, 5, ok)
	levelVar.Set(slog.LevelDebug)
	runComponent(t, mw, loopMethod, 1, ok)

	got := records(t, &buf, loopMethod)
	if len(got) != 1 {
		t.Fatalf("first success after enabling DEBUG must be written, got %d records", len(got))
	}
	if n, _ := got[0]["suppressed"].(float64); n != 0 {
		t.Errorf("calls the handler dropped are not sampling suppressions, got suppressed=%v", got[0]["suppressed"])
	}
}
