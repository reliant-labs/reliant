package svcerr

import (
	"connectrpc.com/connect"
)

// Class is fault attribution: WHO has to act on an error. It is a separate
// question from the Connect code, which says what the CLIENT is told.
//
// Observability is what consumes it. A log level, a Sentry event and an
// alert all mean "someone on our side must look at this", and an error the
// caller or user caused — a typo in a field, an expired invitation, a
// disconnected laptop, an empty wallet — is the system working correctly.
// Paging on those buried the real faults: in one measured week a product's
// ERROR stream was dominated by "no daemon connected for user" and "AI
// provider usage limit reached", none of which anyone on the server side
// could do anything about.
//
// Classify derives the class from the error itself; WithClass overrides it
// for the conditions the code cannot express.
type Class uint8

const (
	// ClassNone is the class of a nil error.
	ClassNone Class = iota

	// ClassServer: our system failed, or cannot tell that it did not. Every
	// error nobody classified lands here, deliberately — an unrecognised
	// driver or SDK error must stay loud.
	ClassServer

	// ClassUser: the caller or the user must act, and the system behaved
	// correctly by refusing. Not a page.
	ClassUser

	// ClassCanceled: the call was cut short by one end going away — the
	// caller (context.Canceled, ErrCanceled, CodeCanceled), or this replica
	// draining for a rollout (ErrServerDraining). Nobody has to act.
	ClassCanceled
)

// String is the class's attribute value in logs and metrics: "none",
// "server", "user", "canceled".
func (c Class) String() string {
	switch c {
	case ClassNone:
		return "none"
	case ClassServer:
		return "server"
	case ClassUser:
		return "user"
	case ClassCanceled:
		return "canceled"
	default:
		return "unknown"
	}
}

// Classed is implemented by an error that states its own Class: a typed
// error whose every occurrence means the same thing ("this provider's
// subscription is out of credit") declares it once, on the type, instead of
// each return site wrapping it with WithClass. WithClass's wrapper is itself
// a Classed.
//
// Returning ClassNone means "no opinion": Classify keeps looking down the
// chain, and then falls back to the kind.
type Classed interface {
	ErrorClass() Class
}

// Classify reports who has to act on err. In order:
//
//  1. nil is ClassNone.
//  2. The nearest explicit class on the chain wins — a WithClass marker, or
//     an error type implementing [Classed]. It is how an application states
//     what the code alone cannot — "the user's machine is offline" travels
//     as Unavailable, which by kind is a server fault.
//  3. A cancellation — context.Canceled, ErrCanceled, or a *connect.Error
//     with CodeCanceled — is ClassCanceled. So is a drain
//     (ErrServerDraining): the client reads Unavailable, which by kind is a
//     server fault, but a replica ending its calls because a rollout is
//     replacing it is the platform working, and one ERROR per connected
//     client per deploy buried the faults.
//  4. Otherwise the kind decides, through the same code the client receives
//     ([Code]). The client-fault kinds are ClassUser: InvalidArgument,
//     NotFound, AlreadyExists, PermissionDenied, Unauthenticated,
//     FailedPrecondition (and so InsufficientBalance and Expired),
//     OutOfRange, and ResourceExhausted (and so PlanLimit). Everything else
//     — Internal, Unknown, DataLoss, Unavailable, DeadlineExceeded, Aborted,
//     Unimplemented, and any error nothing recognised — is ClassServer.
//
// ResourceExhausted is a user class because forge's own sentinel means a
// caller's quota, rate limit or plan. When it is YOUR capacity that ran out
// — a connection pool, a disk — return Unavailable or Internal instead.
//
// Kind is read from the code because the code is what the client was told:
// a 4xx is the caller's to fix, so a log that disagreed with the response
// about whose fault it was would be its own bug. A 4xx that is really ours
// (a row that must exist and does not) is a wrong code, or a place for
// WithClass(err, ClassServer).
//
// # Behind a WithCause
//
// Steps 2 and 4 read the OUTER error of a [WithCause], exactly as the client
// does: a WithClass marker, a [Classed] type or a kind in the cause does not
// count. WithCause(Internal("charge failed"), notFoundFromBilling) is our
// fault — a dependency said not_found, and we answered internal — so it is
// ClassServer; an inner ClassUser marker must not silence it.
//
// Step 3 is the one exception, deliberately: it sees a cancellation anywhere,
// cause included. A context.Canceled is not a verdict someone chose; it is
// the fact that the caller went away, and it reaches handlers labelled
// however they label every repository failure —
// WithCause(Internal("list failed"), err) receives one each time a browser
// navigates away mid-query. Paging on those is the noise this exists to
// remove. The wire still reads the outer error.
func Classify(err error) Class {
	if err == nil {
		return ClassNone
	}
	if class := explicitClass(err); class != ClassNone {
		return class
	}
	// errors.Is, not the client view: a canceled cause counts (see above),
	// and so does a drain behind a handler's own label.
	if IsCanceled(err) || IsServerDraining(err) {
		return ClassCanceled
	}
	switch Code(err) {
	case connect.CodeCanceled:
		return ClassCanceled
	case connect.CodeInvalidArgument,
		connect.CodeNotFound,
		connect.CodeAlreadyExists,
		connect.CodePermissionDenied,
		connect.CodeUnauthenticated,
		connect.CodeFailedPrecondition,
		connect.CodeOutOfRange,
		connect.CodeResourceExhausted:
		return ClassUser
	default:
		return ClassServer
	}
}

// IsUserError reports whether err is ClassUser: the caller or user must act,
// and the system behaved correctly.
func IsUserError(err error) bool { return Classify(err) == ClassUser }

// explicitClass returns the nearest non-None class an error on err's chain
// declares, walking the same tree errors.As does (Unwrap() error and
// Unwrap() []error, depth first) — the client view, so a class declared
// behind a WithCause cause is not err's class.
func explicitClass(err error) Class {
	class := ClassNone
	walkVisible(err, func(e error) bool {
		if c, ok := e.(Classed); ok {
			class = c.ErrorClass()
		}
		return class != ClassNone
	})
	return class
}

// classError carries an explicit Class. It is invisible everywhere else:
// Error() is the wrapped error's text, so the client message is unchanged,
// and Unwrap returns the original, so errors.Is/As, Code, the reason header
// and the redaction rules all see straight through it.
type classError struct {
	class Class
	err   error
}

func (e *classError) Error() string     { return e.err.Error() }
func (e *classError) Unwrap() error     { return e.err }
func (e *classError) ErrorClass() Class { return e.class }

// WithClass states err's Class explicitly, overriding what [Classify] would
// derive from its kind. It changes nothing on the wire — not the code, not
// the message — only who observability says must act.
//
//	// The user's machine is offline: Unavailable is the honest code (retry
//	// later), but nobody on the server side can fix it.
//	return svcerr.WithClass(svcerr.Unavailable("your machine is not connected"), svcerr.ClassUser)
//
//	// An internal error with no Connect boundary at all, logged by a worker:
//	return svcerr.WithClass(fmt.Errorf("provider rejected the credential: %w", err), svcerr.ClassUser)
//
// A condition the client should see as a 4xx usually needs no marker: build
// it with that kind's constructor (FailedPrecondition, PlanLimit, ...) and
// Classify already says ClassUser. Reach for WithClass when the code and the
// fault disagree — or when there is no code because the error never crosses
// an RPC boundary.
//
// The marker lives in the error chain, so it survives wrapping
// (fmt.Errorf("...: %w", err)) and Wrap/ToConnect in process. It does not
// cross a network boundary; a remote caller classifies by the code it
// receives. The nearest marker wins, so an outer WithClass overrides an
// inner one. A marker on a [WithCause] cause is not the error's class: the
// outer error decides.
//
// Behavior:
//   - WithClass(nil, _) returns nil.
//   - WithClass(err, ClassNone) returns err unchanged — "no class" is not a
//     statement worth recording.
func WithClass(err error, class Class) error {
	if err == nil {
		return nil
	}
	if class == ClassNone {
		return err
	}
	return &classError{class: class, err: err}
}
