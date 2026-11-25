// Package retry retries an operation with capped exponential backoff and
// jitter, bounded by attempts, elapsed time and the context, with
// Retry-After, error classification and span events built in.
//
// # Two halves
//
// [Backoff] is the schedule alone: a pure function from a retry number to a
// delay. Use it where the loop is not yours to own — a reconnect loop that
// resets after a healthy session ([Backoff.Schedule]), or a scheduler that
// keeps its failure count in a database row and only needs the next wait
// ([Backoff.Delay]).
//
// [Do] and [DoValue] own the loop: call fn, classify its error, wait, call it
// again, until it succeeds, an error is not retryable, or a limit is reached.
//
//	err := retry.Do(ctx, retry.Policy{
//	    Backoff:     retry.Backoff{Initial: 2 * time.Second, Max: time.Minute, Jitter: retry.FullJitter()},
//	    MaxAttempts: 8,
//	    Retryable:   isTransient,
//	}, func(ctx context.Context) error {
//	    resp, err := client.Do(req.WithContext(ctx))
//	    if err != nil {
//	        return err
//	    }
//	    defer resp.Body.Close()
//	    if resp.StatusCode == http.StatusTooManyRequests {
//	        if d, ok := retry.ParseRetryAfter(resp.Header, time.Now()); ok {
//	            return retry.After(errRateLimited, d) // the server names the wait
//	        }
//	        return errRateLimited
//	    }
//	    if resp.StatusCode == http.StatusBadRequest {
//	        return retry.Permanent(errBadRequest) // never retried
//	    }
//	    return nil
//	})
//
// # Counting
//
// Attempts count calls of fn, from 1. Retries count waits, from 1: retry n
// is the wait after attempt n fails, and [Backoff.Delay](1) is
// [Backoff.Initial]. MaxAttempts is the total number of calls, the first one
// included, so MaxAttempts 1 never retries.
//
// # Deciding the next wait
//
// The computed delay is [Backoff.Delay](n). An error wrapped with [After]
// replaces it (a server's Retry-After). [Policy.NextDelay] sees the result and
// may replace it again — to take the larger of the two, to cap it, or to stop
// with an error of its own when the server asks for longer than the caller
// will wait. [Policy.OnRetry] runs after that, just before the wait, and may
// also stop the loop.
//
// # Observability
//
// Each retry adds a "retry" event to the span active in ctx (the caller's
// span; Do starts none of its own) with the attempt number, the wait and the
// error, and a loop that took more than one attempt sets retry.attempts on
// it. A span that is not recording costs nothing.
//
// # Testing
//
// Do never sleeps on its own: it waits through [Policy.Clock]. The
// retrytest package's Clock records every wait and advances its own time
// instead of sleeping, so a test of a retry loop runs in microseconds and
// asserts the exact schedule. [Backoff.Rand] is the seam for jitter.
//
// # Relation to controller.Backoff
//
// forge/pkg/controller's Backoff is the same capped exponential without
// jitter, indexed from 0 and living in a package that links
// controller-runtime. It stays for reconcilers; everything else uses this
// package.
package retry
