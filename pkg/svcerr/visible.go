package svcerr

import "reflect"

// The client view of an error chain.
//
// errors.Is and errors.As reach a WithCause cause — that is what keeps
// `errors.As(err, &pgErr)` working for the server (see redactedError's Is and
// As). Nothing that decides what the CLIENT is told may reach it: the code,
// the message, the reason header, IsClassified and Classify are all read
// from the outer error, and the cause is for the log.
//
// They used to share one chain, and the cause won whenever it was the nearer
// or more specific match. A downstream service's *connect.Error was found by
// errors.As and published verbatim — its code, its message (an internal
// hostname), its reason — in place of the "charge failed" the handler wrote;
// an svcerr kind in the cause changed the code; a WithClass on the cause
// silenced a real server fault.
//
// visibleIs and visibleAs are errors.Is and errors.As with one difference:
// they never call redactedError's Is / As, the two doors to the cause. They
// follow Unwrap, which at a WithCause boundary leads to the outer error only.
// Every other node's Is / As method is honoured, so an error type that
// answers Is(context.Canceled) keeps doing so.

// visibleIs is errors.Is over the client view.
func visibleIs(err, target error) bool {
	if err == nil || target == nil {
		return err == target
	}
	comparable := reflect.TypeOf(target).Comparable()
	return walkVisible(err, func(e error) bool {
		if comparable && e == target {
			return true
		}
		if _, redacted := e.(*redactedError); redacted {
			return false
		}
		x, ok := e.(interface{ Is(error) bool })
		return ok && x.Is(target)
	})
}

// visibleAs is errors.As over the client view, returning the match.
func visibleAs[T any](err error) (T, bool) {
	var found T
	ok := walkVisible(err, func(e error) bool {
		if t, ok := e.(T); ok {
			found = t
			return true
		}
		if _, redacted := e.(*redactedError); redacted {
			return false
		}
		x, ok := e.(interface{ As(any) bool })
		return ok && x.As(&found)
	})
	return found, ok
}

// walkVisible calls visit on err and everything it unwraps to, in errors.As
// order (Unwrap() error, then each of Unwrap() []error depth first), until
// visit returns true. It reports whether one did.
func walkVisible(err error, visit func(error) bool) bool {
	for err != nil {
		if visit(err) {
			return true
		}
		switch u := err.(type) {
		case interface{ Unwrap() error }:
			err = u.Unwrap()
		case interface{ Unwrap() []error }:
			for _, inner := range u.Unwrap() {
				if walkVisible(inner, visit) {
					return true
				}
			}
			return false
		default:
			return false
		}
	}
	return false
}
