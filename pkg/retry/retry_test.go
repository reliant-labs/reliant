package retry_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/reliant-labs/forge/pkg/retry"
	"github.com/reliant-labs/forge/pkg/retry/retrytest"
)

var (
	errFlaky = errors.New("flaky")
	errFatal = errors.New("fatal")
	epoch    = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
)

// failing returns an fn that fails with errs in order, then succeeds, and
// counts its calls.
func failing(calls *int, errs ...error) func(context.Context) error {
	return func(context.Context) error {
		*calls++
		if *calls <= len(errs) {
			return errs[*calls-1]
		}
		return nil
	}
}

func policy(clock *retrytest.Clock) retry.Policy {
	return retry.Policy{
		Backoff:     retry.Backoff{Initial: time.Second, Max: 4 * time.Second},
		MaxAttempts: 5,
		Clock:       clock,
	}
}

func TestDoRetriesUntilSuccess(t *testing.T) {
	t.Parallel()
	clock := retrytest.NewClock(epoch)
	calls := 0
	err := retry.Do(context.Background(), policy(clock), failing(&calls, errFlaky, errFlaky, errFlaky))
	if err != nil {
		t.Fatalf("Do = %v, want nil", err)
	}
	if calls != 4 {
		t.Errorf("calls = %d, want 4", calls)
	}
	assertSleeps(t, clock, time.Second, 2*time.Second, 4*time.Second)
}

func TestDoStopsAtMaxAttemptsWithTheLastError(t *testing.T) {
	t.Parallel()
	clock := retrytest.NewClock(epoch)
	calls := 0
	last := errors.New("fifth")
	err := retry.Do(context.Background(), policy(clock),
		failing(&calls, errFlaky, errFlaky, errFlaky, errFlaky, last, errFlaky))
	if err != last {
		t.Fatalf("Do = %v, want the fifth attempt's error itself", err)
	}
	if calls != 5 {
		t.Errorf("calls = %d, want MaxAttempts (5)", calls)
	}
	assertSleeps(t, clock, time.Second, 2*time.Second, 4*time.Second, 4*time.Second)
}

func TestMaxAttemptsOneNeverRetries(t *testing.T) {
	t.Parallel()
	clock := retrytest.NewClock(epoch)
	p := policy(clock)
	p.MaxAttempts = 1
	calls := 0
	if err := retry.Do(context.Background(), p, failing(&calls, errFlaky)); err != errFlaky {
		t.Fatalf("Do = %v, want %v", err, errFlaky)
	}
	assertSleeps(t, clock)
}

func TestPermanentStopsAndIsUnwrapped(t *testing.T) {
	t.Parallel()
	clock := retrytest.NewClock(epoch)
	calls := 0
	err := retry.Do(context.Background(), policy(clock), failing(&calls, errFlaky, retry.Permanent(errFatal)))
	if err != errFatal {
		t.Fatalf("Do = %v, want the unwrapped %v", err, errFatal)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
	assertSleeps(t, clock, time.Second)
}

func TestPermanentDeepInTheChainStillStops(t *testing.T) {
	t.Parallel()
	wrapped := errors.Join(errors.New("context"), retry.Permanent(errFatal))
	if !retry.IsPermanent(wrapped) {
		t.Fatal("IsPermanent = false through errors.Join")
	}
	if !errors.Is(wrapped, errFatal) {
		t.Fatal("Permanent hides its error from errors.Is")
	}
	if retry.Permanent(nil) != nil || retry.After(nil, time.Second) != nil {
		t.Fatal("Permanent(nil) / After(nil) must be nil")
	}
	if retry.Permanent(errFatal).Error() != errFatal.Error() {
		t.Fatal("Permanent changed the message")
	}
}

func TestRetryableClassifies(t *testing.T) {
	t.Parallel()
	clock := retrytest.NewClock(epoch)
	p := policy(clock)
	p.Retryable = func(err error) bool { return errors.Is(err, errFlaky) }
	calls := 0
	if err := retry.Do(context.Background(), p, failing(&calls, errFlaky, errFatal)); err != errFatal {
		t.Fatalf("Do = %v, want %v", err, errFatal)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestAfterReplacesTheComputedWait(t *testing.T) {
	t.Parallel()
	clock := retrytest.NewClock(epoch)
	calls := 0
	err := retry.Do(context.Background(), policy(clock),
		failing(&calls, retry.After(errFlaky, 7*time.Second), errFlaky))
	if err != nil {
		t.Fatalf("Do = %v", err)
	}
	assertSleeps(t, clock, 7*time.Second, 2*time.Second)
}

func TestAfterIsUnwrappedFromTheResult(t *testing.T) {
	t.Parallel()
	clock := retrytest.NewClock(epoch)
	p := policy(clock)
	p.MaxAttempts = 1
	err := retry.Do(context.Background(), p, func(context.Context) error {
		return retry.After(errFlaky, time.Second)
	})
	if err != errFlaky {
		t.Fatalf("Do = %v, want the unwrapped %v", err, errFlaky)
	}
}

func TestNextDelayOverridesFloorsAndStops(t *testing.T) {
	t.Parallel()
	errTooLong := errors.New("server asked for too long")
	clock := retrytest.NewClock(epoch)
	p := policy(clock)
	var seen []time.Duration
	p.NextDelay = func(err error, n int, computed time.Duration) (time.Duration, error) {
		seen = append(seen, computed)
		switch n {
		case 1:
			return max(computed, 3*time.Second), nil // a floor
		case 2:
			return computed, nil // the After wrapper's 30s, passed through
		default:
			return 0, errTooLong
		}
	}
	calls := 0
	err := retry.Do(context.Background(), p,
		failing(&calls, errFlaky, retry.After(errFlaky, 30*time.Second), errFlaky, errFlaky))
	if err != errTooLong {
		t.Fatalf("Do = %v, want NextDelay's own error", err)
	}
	assertDurationList(t, "NextDelay saw", seen, []time.Duration{time.Second, 30 * time.Second, 4 * time.Second})
	assertSleeps(t, clock, 3*time.Second, 30*time.Second)
}

func TestOnRetryRunsBeforeTheWaitAndCanAbort(t *testing.T) {
	t.Parallel()
	errAbort := errors.New("abort")
	clock := retrytest.NewClock(epoch)
	p := policy(clock)
	var attempts []retry.Attempt
	p.OnRetry = func(_ context.Context, a retry.Attempt) error {
		if len(clock.Sleeps()) != len(attempts) {
			t.Errorf("OnRetry ran after the wait")
		}
		attempts = append(attempts, a)
		if a.Number == 2 {
			return errAbort
		}
		return nil
	}
	calls := 0
	err := retry.Do(context.Background(), p, failing(&calls, errFlaky, errFatal, errFlaky))
	if err != errAbort {
		t.Fatalf("Do = %v, want %v", err, errAbort)
	}
	if len(attempts) != 2 {
		t.Fatalf("OnRetry ran %d times, want 2", len(attempts))
	}
	a := attempts[1]
	if a.Number != 2 || a.Err != errFatal || a.Delay != 2*time.Second || a.Elapsed != time.Second {
		t.Errorf("second Attempt = %+v, want Number 2, errFatal, Delay 2s, Elapsed 1s", a)
	}
	assertSleeps(t, clock, time.Second)
}

func TestMaxElapsedStopsBeforeAWaitThatWouldPassIt(t *testing.T) {
	t.Parallel()
	clock := retrytest.NewClock(epoch)
	p := retry.Policy{
		Backoff:    retry.Backoff{Initial: time.Second, Max: 5 * time.Second},
		MaxElapsed: 10 * time.Second,
		Clock:      clock,
	}
	calls := 0
	err := retry.Do(context.Background(), p, func(context.Context) error {
		calls++
		return errFlaky
	})
	if err != errFlaky {
		t.Fatalf("Do = %v, want the last attempt's error", err)
	}
	// 1+2+4 = 7s; the next wait (5s) would end at 12s > 10s.
	assertSleeps(t, clock, time.Second, 2*time.Second, 4*time.Second)
	if calls != 4 {
		t.Errorf("calls = %d, want 4", calls)
	}
}

func TestMaxElapsedCountsTimeSpentInAttempts(t *testing.T) {
	t.Parallel()
	clock := retrytest.NewClock(epoch)
	p := retry.Policy{
		Backoff:    retry.Backoff{Initial: time.Second, Multiplier: 1},
		MaxElapsed: 5 * time.Second,
		Clock:      clock,
	}
	calls := 0
	_ = retry.Do(context.Background(), p, func(context.Context) error {
		calls++
		clock.Advance(2 * time.Second)
		return errFlaky
	})
	// 2s in the attempt + a 1s wait = 3s; the second attempt ends at 5s, and
	// its 1s wait would end past MaxElapsed.
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestCancelDuringTheWaitWrapsBothErrors(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	clock := retrytest.NewClock(epoch)
	p := policy(clock)
	p.OnRetry = func(context.Context, retry.Attempt) error {
		cancel()
		return nil
	}
	calls := 0
	err := retry.Do(ctx, p, failing(&calls, errFlaky, errFlaky))
	if !errors.Is(err, context.Canceled) || !errors.Is(err, errFlaky) {
		t.Fatalf("Do = %v, want it to wrap context.Canceled and the last error", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestCancelledContextEndsTheLoopWithTheAttemptsError(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	clock := retrytest.NewClock(epoch)
	calls := 0
	err := retry.Do(ctx, policy(clock), func(ctx context.Context) error {
		calls++
		cancel()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("Do = %v after %d calls, want context.Canceled after 1", err, calls)
	}
	assertSleeps(t, clock)

	if err := retry.Do(ctx, policy(clock), func(context.Context) error {
		t.Fatal("fn ran on a context that had already ended")
		return nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Do on an ended context = %v, want context.Canceled", err)
	}
}

func TestAttemptTimeoutBoundsEachAttemptAndRetries(t *testing.T) {
	t.Parallel()
	clock := retrytest.NewClock(epoch)
	p := policy(clock)
	p.AttemptTimeout = time.Hour
	var numbers []int
	err := retry.Do(context.Background(), p, func(ctx context.Context) error {
		numbers = append(numbers, retry.AttemptNumber(ctx))
		if _, ok := ctx.Deadline(); !ok {
			t.Error("attempt ctx has no deadline")
		}
		if len(numbers) == 1 {
			return context.DeadlineExceeded // as if this attempt timed out
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Do = %v, want an attempt timeout to be retried", err)
	}
	if len(numbers) != 2 || numbers[0] != 1 || numbers[1] != 2 {
		t.Errorf("AttemptNumber = %v, want [1 2]", numbers)
	}
	if retry.AttemptNumber(context.Background()) != 0 {
		t.Error("AttemptNumber outside Do != 0")
	}
}

func TestDoValueReturnsTheValue(t *testing.T) {
	t.Parallel()
	clock := retrytest.NewClock(epoch)
	calls := 0
	v, err := retry.DoValue(context.Background(), policy(clock), func(context.Context) (int, error) {
		calls++
		if calls < 3 {
			return calls, errFlaky
		}
		return 42, nil
	})
	if err != nil || v != 42 {
		t.Fatalf("DoValue = (%d, %v), want (42, nil)", v, err)
	}
	p := policy(clock)
	p.MaxAttempts = 1
	v, err = retry.DoValue(context.Background(), p, func(context.Context) (int, error) { return 7, errFatal })
	if err != errFatal || v != 7 {
		t.Fatalf("DoValue on give-up = (%d, %v), want the last attempt's (7, errFatal)", v, err)
	}
}

func TestZeroPolicyUsesTheSystemClockWithoutWaiting(t *testing.T) {
	t.Parallel()
	calls := 0
	// Backoff zero value: retries at once; MaxAttempts bounds it.
	if err := retry.Do(context.Background(), retry.Policy{MaxAttempts: 3}, failing(&calls, errFlaky, errFlaky)); err != nil {
		t.Fatalf("Do = %v", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

func TestSystemClockSleepEndsWithTheContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := retry.SystemClock().Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("Sleep on an ended context = %v, want context.Canceled", err)
	}
	if err := retry.SystemClock().Sleep(context.Background(), 0); err != nil {
		t.Fatalf("Sleep(0) = %v", err)
	}
}

func TestSpanEventsAndAttempts(t *testing.T) {
	t.Parallel()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	ctx, span := tp.Tracer("test").Start(context.Background(), "op")
	clock := retrytest.NewClock(epoch)
	p := policy(clock)
	p.Name = "fetch"
	calls := 0
	if err := retry.Do(ctx, p, failing(&calls, errFlaky, errFlaky)); err != nil {
		t.Fatal(err)
	}
	span.End()

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("recorded %d spans, want only the caller's", len(spans))
	}
	got := spans[0]
	events := got.Events()
	if len(events) != 2 {
		t.Fatalf("events = %d, want one per retry (2)", len(events))
	}
	for i, ev := range events {
		attrs := attrMap(ev.Attributes)
		if ev.Name != "retry" || attrs["retry.attempt"].AsInt64() != int64(i+1) ||
			attrs["retry.delay_ms"].AsInt64() != int64(1000<<i) ||
			attrs["error.message"].AsString() != "flaky" || attrs["retry.name"].AsString() != "fetch" {
			t.Errorf("event %d = %s %v", i, ev.Name, ev.Attributes)
		}
	}
	if attrMap(got.Attributes())["retry.attempts"].AsInt64() != 3 {
		t.Errorf("span attributes = %v, want retry.attempts=3", got.Attributes())
	}
}

func TestFirstTrySuccessLeavesTheSpanAlone(t *testing.T) {
	t.Parallel()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	ctx, span := tp.Tracer("test").Start(context.Background(), "op")
	if err := retry.Do(ctx, policy(retrytest.NewClock(epoch)), func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	span.End()
	s := rec.Ended()[0]
	if len(s.Events()) != 0 || len(s.Attributes()) != 0 {
		t.Errorf("span = %v %v, want no retry telemetry", s.Events(), s.Attributes())
	}
}

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		header http.Header
		want   time.Duration
		ok     bool
	}{
		{"seconds", http.Header{"Retry-After": {"30"}}, 30 * time.Second, true},
		{"fractional seconds", http.Header{"Retry-After": {"1.5"}}, 1500 * time.Millisecond, true},
		{"milliseconds win", http.Header{"Retry-After": {"30"}, "Retry-After-Ms": {"250"}}, 250 * time.Millisecond, true},
		{"bad ms falls back", http.Header{"Retry-After": {"2"}, "Retry-After-Ms": {"soon"}}, 2 * time.Second, true},
		{"http date", http.Header{"Retry-After": {epoch.Add(90 * time.Second).Format(http.TimeFormat)}}, 90 * time.Second, true},
		{"past date is zero", http.Header{"Retry-After": {epoch.Add(-time.Hour).Format(http.TimeFormat)}}, 0, true},
		{"negative is zero", http.Header{"Retry-After": {"-5"}}, 0, true},
		{"garbage", http.Header{"Retry-After": {"later"}}, 0, false},
		{"NaN", http.Header{"Retry-After": {"NaN"}}, 0, false},
		{"absent", http.Header{}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := retry.ParseRetryAfter(tc.header, epoch)
			if got != tc.want || ok != tc.ok {
				t.Errorf("ParseRetryAfter = (%v, %v), want (%v, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestRetrytestClockRefusesAnEndedContext(t *testing.T) {
	t.Parallel()
	clock := retrytest.NewClock(epoch)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := clock.Sleep(ctx, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("Sleep = %v, want context.Canceled", err)
	}
	if len(clock.Sleeps()) != 0 || !clock.Now().Equal(epoch) {
		t.Fatal("a refused Sleep recorded a wait or moved the clock")
	}
}

func attrMap(kvs []attribute.KeyValue) map[string]attribute.Value {
	m := make(map[string]attribute.Value, len(kvs))
	for _, kv := range kvs {
		m[string(kv.Key)] = kv.Value
	}
	return m
}

func assertSleeps(t *testing.T, clock *retrytest.Clock, want ...time.Duration) {
	t.Helper()
	assertDurationList(t, "waits", clock.Sleeps(), want)
}

func assertDurationList(t *testing.T, what string, got, want []time.Duration) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("%s = %v, want %v", what, got, want)
		}
	}
}
