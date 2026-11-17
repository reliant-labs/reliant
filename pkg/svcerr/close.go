package svcerr

// close.go — an ending the server chose on purpose.
//
// Some calls end with an error code only because the wire has no better word
// for "this is over, redial". A gateway cuts a stream whose peer has sent
// nothing for 90s so the peer reconnects; Unavailable is the code that peer
// needs, and changing it would strand every older client that redials on it.
// But nothing failed: the server applied its own policy. Classed canceled, the
// ending already logs below ERROR; to the trace and the error-rate metric it
// was still an Unavailable, so a stream that only ever ends this way read as a
// mostly-failing rpc.
//
// ExpectedClose is the handler's explicit statement of that, so observability
// never has to guess it from a code: a real Unavailable — a dependency that
// refused — looks the same on the wire and must stay an error.

// CloseReasonExpected is the reason ExpectedClose records when it is given
// none.
const CloseReasonExpected = "expected"

// closeError marks an expected close. Like classError it is invisible
// everywhere but observability: Error() is the wrapped error's text and
// Unwrap returns it, so the code, the message, the reason header and
// errors.Is/As see straight through it.
type closeError struct {
	reason string
	err    error
}

func (e *closeError) Error() string { return e.err.Error() }
func (e *closeError) Unwrap() error { return e.err }

// ErrorClass makes an expected close ClassCanceled: nobody has to act.
func (e *closeError) ErrorClass() Class { return ClassCanceled }

// ExpectedClose marks err as an ending the server chose on purpose — its own
// idle policy, a handover, a session cap — rather than a failure. The client
// receives err exactly as before (code, message, reason header); only
// observability changes. Forge's observe layers then:
//
//   - leave the request's server span status OK, even when a tracing layer
//     that knows only the wire code (otelconnect) would mark it Error, and
//     record the reason as rpc.close_reason;
//   - do not count the call in rpc.server.errors;
//   - log it as an expected outcome (expected=true, close_reason=<reason>) at
//     the success level, not as "stream failed".
//
// reason names the policy in a short, closed vocabulary — "peer_idle",
// "handover", "session_limit" — because it is an attribute a dashboard groups
// by; an empty reason records CloseReasonExpected. Put the detail in err's
// message instead.
//
//	// The sweeper cut a stream whose daemon went quiet: Unavailable sends the
//	// daemon down its redial path, and nothing here failed.
//	var errStreamSilent = svcerr.ExpectedClose(
//	    svcerr.Unavailable("read nothing for 90s; closing so the daemon redials"),
//	    "peer_idle")
//
// Use it only for an ending the SERVER decided. A dependency that refused, a
// replica that cannot serve, or anything a person may need to look at is a
// failure and stays one. A client that went away needs no marker: it is
// already canceled-class (see [Classify]), and a drain has its own sentinel
// ([ErrServerDraining]).
//
// An expected close is ClassCanceled. The marker obeys the same rules as
// [WithClass]: it survives wrapping in process and does not cross the network;
// the nearest class on the chain wins, so an outer WithClass(err, ClassServer)
// turns it back into a failure; and a marker on a [WithCause] cause is not the
// error's. ExpectedClose(nil, _) returns nil.
func ExpectedClose(err error, reason string) error {
	if err == nil {
		return nil
	}
	if reason == "" {
		reason = CloseReasonExpected
	}
	return &closeError{reason: reason, err: err}
}

// CloseReason reports whether err is an expected close ([ExpectedClose]) and
// the reason it was given. It is one only while it is still ClassCanceled: an
// outer WithClass that reclassified it as a failure wins, as it does for
// [Classify].
func CloseReason(err error) (reason string, ok bool) {
	if err == nil || Classify(err) != ClassCanceled {
		return "", false
	}
	ce, found := visibleAs[*closeError](err)
	if !found {
		return "", false
	}
	return ce.reason, true
}

// IsExpectedClose reports whether err is an expected close; see [CloseReason].
func IsExpectedClose(err error) bool {
	_, ok := CloseReason(err)
	return ok
}
