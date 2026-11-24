package retry

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Policy is how [Do] retries: the schedule, the limits, which errors retry,
// and the hooks. The zero value retries every error immediately, forever,
// until ctx ends; set at least Backoff.Initial and one limit.
type Policy struct {
	// Backoff is the schedule of waits between attempts.
	Backoff Backoff

	// MaxAttempts is the total number of calls of fn, the first included.
	// Zero means no limit (the loop ends on ctx, MaxElapsed, success or a
	// non-retryable error).
	MaxAttempts int

	// MaxElapsed stops retrying once the next attempt would start more than
	// MaxElapsed after the first one began. Zero means no limit.
	MaxElapsed time.Duration

	// AttemptTimeout bounds each call of fn with its own deadline. Zero
	// means each call has only ctx's. An attempt that hits it fails with
	// context.DeadlineExceeded and is retried like any other error.
	AttemptTimeout time.Duration

	// Retryable reports whether an error is worth another attempt. Nil
	// means every error is, except those wrapped with [Permanent]. A
	// Permanent error is never retried, whatever Retryable says.
	Retryable func(error) bool

	// NextDelay, when set, decides the wait before retry n. computed is the
	// Backoff's delay, already replaced by an [After] wrapper's when err
	// carries one. Return the wait to use, or an error to stop retrying:
	// Do returns that error as it is.
	NextDelay func(err error, n int, computed time.Duration) (time.Duration, error)

	// OnRetry, when set, runs before each wait — the place to log, count
	// or tell a user that a retry is coming. A non-nil return stops the
	// loop, and Do returns that error as it is.
	OnRetry func(ctx context.Context, a Attempt) error

	// Clock tells the time and waits. Nil means the system clock. Tests use
	// retrytest.Clock, which never sleeps.
	Clock Clock

	// Name, when set, labels this loop's span events (retry.name), so two
	// loops under one span can be told apart.
	Name string
}

// Attempt describes a failed attempt that is about to be retried.
type Attempt struct {
	// Number is the attempt that failed, from 1. The retry that follows is
	// retry Number.
	Number int
	// Err is the attempt's error.
	Err error
	// Delay is the wait before the next attempt.
	Delay time.Duration
	// Elapsed is the time since the first attempt began.
	Elapsed time.Duration
}

// Do calls fn until it returns nil, returns an error that is not
// retryable, or a limit is reached, waiting between calls as p says.
//
// It returns nil on success. When it gives up it returns the last
// attempt's error unchanged (a [Permanent] or [After] wrapper around it is
// removed), so errors.Is and errors.As see what fn returned. When ctx ends
// during a wait it returns an error that wraps both ctx's error and the
// last attempt's. An error from NextDelay or OnRetry is returned as it is.
func Do(ctx context.Context, p Policy, fn func(ctx context.Context) error) error {
	_, err := DoValue(ctx, p, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, fn(ctx)
	})
	return err
}

// DoValue is [Do] for an operation that returns a value. On success it
// returns the value of the attempt that succeeded; when fn's own error ends
// the loop it returns that attempt's value with it.
func DoValue[T any](ctx context.Context, p Policy, fn func(ctx context.Context) (T, error)) (T, error) {
	clock := p.Clock
	if clock == nil {
		clock = systemClock{}
	}
	span := trace.SpanFromContext(ctx)
	start := clock.Now()
	var zero T

	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return zero, err
		}
		v, err := callAttempt(ctx, p.AttemptTimeout, attempt, fn)
		if err == nil {
			recordAttempts(span, p.Name, attempt)
			return v, nil
		}
		// Once ctx itself has ended, the attempt's error is the answer:
		// it is usually ctx's error, wrapped by whatever fn called.
		if ctx.Err() != nil || IsPermanent(err) || (p.Retryable != nil && !p.Retryable(err)) ||
			(p.MaxAttempts > 0 && attempt >= p.MaxAttempts) {
			recordAttempts(span, p.Name, attempt)
			return v, unwrapMarkers(err)
		}

		delay := p.Backoff.Delay(attempt)
		if after, ok := RetryAfterOf(err); ok {
			delay = after
		}
		if p.NextDelay != nil {
			d, herr := p.NextDelay(err, attempt, delay)
			if herr != nil {
				recordAttempts(span, p.Name, attempt)
				return zero, herr
			}
			delay = d
		}
		delay = max(delay, 0)

		elapsed := clock.Now().Sub(start)
		if p.MaxElapsed > 0 && elapsed+delay > p.MaxElapsed {
			recordAttempts(span, p.Name, attempt)
			return v, unwrapMarkers(err)
		}

		a := Attempt{Number: attempt, Err: err, Delay: delay, Elapsed: elapsed}
		recordRetry(span, p.Name, a)
		if p.OnRetry != nil {
			if herr := p.OnRetry(ctx, a); herr != nil {
				recordAttempts(span, p.Name, attempt)
				return zero, herr
			}
		}
		if serr := clock.Sleep(ctx, delay); serr != nil {
			recordAttempts(span, p.Name, attempt)
			return zero, fmt.Errorf("%w (last attempt: %w)", serr, unwrapMarkers(err))
		}
	}
}

func callAttempt[T any](ctx context.Context, timeout time.Duration, attempt int, fn func(context.Context) (T, error)) (T, error) {
	actx := context.WithValue(ctx, attemptKey{}, attempt)
	if timeout > 0 {
		var cancel context.CancelFunc
		actx, cancel = context.WithTimeout(actx, timeout)
		defer cancel()
	}
	return fn(actx)
}

type attemptKey struct{}

// AttemptNumber reports which attempt of a [Do] loop ctx belongs to, from 1,
// or 0 when ctx did not come from one. fn uses it to log, or to behave
// differently on a retry (resume a stream rather than restart it).
func AttemptNumber(ctx context.Context) int {
	n, _ := ctx.Value(attemptKey{}).(int)
	return n
}

// Span vocabulary. Attribute names follow OpenTelemetry's dotted style.
const (
	spanEventRetry    = "retry"
	attrRetryAttempt  = "retry.attempt"
	attrRetryDelayMs  = "retry.delay_ms"
	attrRetryName     = "retry.name"
	attrRetryAttempts = "retry.attempts"
	attrErrorMessage  = "error.message"
)

func recordRetry(span trace.Span, name string, a Attempt) {
	if !span.IsRecording() {
		return
	}
	attrs := []attribute.KeyValue{
		attribute.Int(attrRetryAttempt, a.Number),
		attribute.Int64(attrRetryDelayMs, a.Delay.Milliseconds()),
		attribute.String(attrErrorMessage, a.Err.Error()),
	}
	if name != "" {
		attrs = append(attrs, attribute.String(attrRetryName, name))
	}
	span.AddEvent(spanEventRetry, trace.WithAttributes(attrs...))
}

// recordAttempts sets retry.attempts on the span when the loop retried at
// all; a first-try success leaves the span untouched.
func recordAttempts(span trace.Span, name string, attempts int) {
	if attempts < 2 || !span.IsRecording() {
		return
	}
	attrs := []attribute.KeyValue{attribute.Int(attrRetryAttempts, attempts)}
	if name != "" {
		attrs = append(attrs, attribute.String(attrRetryName, name))
	}
	span.SetAttributes(attrs...)
}
