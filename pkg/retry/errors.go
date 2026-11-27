package retry

import (
	"errors"
	"time"
)

// Permanent marks err as not worth retrying: [Do] returns it at once,
// whatever [Policy.Retryable] says. Permanent(nil) is nil. The wrapper is
// transparent — its message is err's, and errors.Is / errors.As see through
// it.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &permanentError{err: err}
}

// IsPermanent reports whether err, anywhere in its chain, was wrapped with
// [Permanent].
func IsPermanent(err error) bool {
	var p *permanentError
	return errors.As(err, &p)
}

type permanentError struct{ err error }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

// After marks err as retryable after exactly d — typically a server's
// Retry-After ([ParseRetryAfter]) — instead of the Backoff's computed wait.
// [Policy.NextDelay], when set, still has the last word. After(nil, d) is
// nil. Like [Permanent], the wrapper is transparent.
func After(err error, d time.Duration) error {
	if err == nil {
		return nil
	}
	return &afterError{err: err, delay: d}
}

// RetryAfterOf returns the wait an [After] wrapper anywhere in err's chain
// carries.
func RetryAfterOf(err error) (time.Duration, bool) {
	var a *afterError
	if errors.As(err, &a) {
		return a.delay, true
	}
	return 0, false
}

type afterError struct {
	err   error
	delay time.Duration
}

func (e *afterError) Error() string { return e.err.Error() }
func (e *afterError) Unwrap() error { return e.err }

// unwrapMarkers strips Permanent / After wrappers from the top of err, so a
// caller comparing Do's result to a sentinel with == still matches.
func unwrapMarkers(err error) error {
	for {
		switch e := err.(type) {
		case *permanentError:
			err = e.err
		case *afterError:
			err = e.err
		default:
			return err
		}
	}
}
