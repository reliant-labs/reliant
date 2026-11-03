// Package svcerr provides the canonical service-error → Connect-error
// mapping used by every forge handler package.
//
// # Why this lives here
//
// Earlier forge versions left handler packages to write their own
// per-package mapServiceError / toConnectError helper. The dogfood pass
// of control-plane-next ended up with four byte-identical copies of the
// same switch statement (handlers/{billing,daemon,llm_gateway,org}/
// handlers.go). Worse, the api/handlers skill prescribed the
// hand-rolled pattern, so the duplication was earned: the LLM was
// faithfully following the documented convention.
//
// svcerr ships the mapping once, as a library. Sentinels in this
// package are the canonical service-layer error categories; Wrap
// converts any error wrapping one of them into the right
// *connect.Error. Already-Connect errors pass through unchanged so
// handlers can compose freely.
//
// # Usage
//
//	// In internal/<svc>/contract.go (the service layer):
//	import "github.com/reliant-labs/forge/pkg/svcerr"
//
//	func (s *svc) GetThing(ctx context.Context, id string) (*Thing, error) {
//	    row, err := s.db.GetThing(ctx, id)
//	    if errors.Is(err, sql.ErrNoRows) {
//	        return nil, svcerr.NotFound("thing")
//	    }
//	    if err != nil {
//	        return nil, fmt.Errorf("get thing: %w", err)
//	    }
//	    return row, nil
//	}
//
//	// In handlers/<svc>/handlers.go (the wire layer):
//	import "github.com/reliant-labs/forge/pkg/svcerr"
//
//	func (s *Service) GetThing(ctx context.Context, req *connect.Request[pb.GetThingRequest]) (*connect.Response[pb.GetThingResponse], error) {
//	    thing, err := s.deps.Things.GetThing(ctx, req.Msg.GetId())
//	    if err != nil {
//	        return nil, svcerr.Wrap(err)
//	    }
//	    return connect.NewResponse(thingToProto(thing)), nil
//	}
//
// # Sentinel set
//
// One sentinel per Connect code we map. Service code constructs them
// either by `errors.Is`-able comparison (return svcerr.ErrNotFound) or
// via the matching constructor that carries a human-readable cause:
//
//	return svcerr.NotFound("user")               // → CodeNotFound, "user not found"
//	return svcerr.PermissionDenied("admin only") // → CodePermissionDenied, "admin only"
//
// Both forms preserve the sentinel for downstream errors.Is checks and
// for Code() lookups.
//
// # What the client reads
//
// The message on the wire is the DETAIL the constructor was given, and
// nothing else — see detailError and clientMessage. A sentinel is an
// identity, not display copy, and the category it names is already on the
// wire twice (the Connect code, and ReasonHeader for the machine-readable
// reason).
//
// Wrapping does not widen that. `fmt.Errorf("get thing %s from %s: %w",
// id, shard, err)` is context for the OPERATOR: it survives as a cause
// (see [Cause]) and reaches the log, and it never reaches the client. That
// is not a courtesy — a recognised sentinel used to make the whole
// accumulated string client-visible, which published request ids, internal
// hostnames and, measured, a DSN with a password to an unauthenticated
// caller. Prose meant for a client goes in the constructor's argument.
//
// # What a cause decides: nothing the client sees
//
// [WithCause] attaches a server-only cause to an error. The OUTER error
// decides everything the client reads — the code, the message, the
// [ReasonHeader] reason — and everything [Classify] derives from those. The
// cause decides none of it, whatever it is: a driver error, another svcerr
// kind, or a *connect.Error a downstream service returned. It reaches the
// log through [Cause] and stays reachable server-side through errors.Is and
// errors.As (and so the Is* predicates, which ask what an error CARRIES),
// with one exception: errors.As never finds a *connect.Error behind a cause,
// because that is how connect-go itself finds the verdict it publishes.
//
// The question "what will the client be told?" is [Code], not an Is*
// predicate: IsNotFound(WithCause(Internal("x"), NotFound("row"))) is true,
// and the client reads internal "x".
package svcerr

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
)

// ReasonHeader is the Connect error-metadata key under which a stable,
// machine-readable domain reason code (see WithReason) is delivered.
// Clients route on this code instead of matching server MESSAGE TEXT
// with brittle regexes. The value is an app-defined snake_case code
// (e.g. "no_active_subscription"); forge only carries it, it never
// invents codes. HTTP header keys are case-insensitive, so the frontend
// error mapper may read it as "x-forge-error-reason" regardless of
// canonicalization.
const ReasonHeader = "x-forge-error-reason"

// Sentinel errors mapped to Connect codes. Each sentinel corresponds to
// exactly one connect.Code (see codeFor). Service code returns either
// the sentinel directly (`return svcerr.ErrNotFound`) or one of the
// matching constructors (`return svcerr.NotFound("user")`), which carry
// the sentinel for identity while the client reads only the detail (see
// detailError).
//
// Their identity is the VALUE, not the text — errors.New returns a
// distinct pointer per call — so the strings here are free to be plain
// readable prose, and they must be: a service that returns a bare
// sentinel puts exactly this text in front of a user.
//
// Add new sentinels parsimoniously — only when an existing Connect code
// has no representative sentinel here AND the service layer needs to
// signal it from places the handler can't otherwise distinguish.
var (
	ErrCanceled           = errors.New("canceled")
	ErrUnknown            = errors.New("unknown")
	ErrInvalidArgument    = errors.New("invalid argument")
	ErrDeadlineExceeded   = errors.New("deadline exceeded")
	ErrNotFound           = errors.New("not found")
	ErrAlreadyExists      = errors.New("already exists")
	ErrPermissionDenied   = errors.New("permission denied")
	ErrResourceExhausted  = errors.New("resource exhausted")
	ErrFailedPrecondition = errors.New("failed precondition")
	ErrAborted            = errors.New("aborted")
	ErrOutOfRange         = errors.New("out of range")
	ErrUnimplemented      = errors.New("unimplemented")
	ErrInternal           = errors.New("internal")
	ErrUnavailable        = errors.New("unavailable")
	ErrDataLoss           = errors.New("data loss")
	ErrUnauthenticated    = errors.New("unauthenticated")

	// Domain-distinct sentinels with codes shared by the canonical set
	// above. Added because real services routinely need to distinguish
	// these failure modes at errors.Is time even though they map to the
	// same wire-level Connect code. Without them, billing/quota/expiry
	// callers either lose fidelity (collapse into the canonical sentinel)
	// or carry private sentinels, breaking svcerr.Wrap composition.

	// ErrPlanLimit indicates the operation was rejected because the
	// caller's plan/tier doesn't allow it (seat cap, feature gate, model
	// allowlist, etc.). Distinct from a generic rate-limit hit so
	// upstream code can errors.Is(err, ErrPlanLimit) for upsell-prompt /
	// per-cause metric purposes. Wire code: ResourceExhausted.
	ErrPlanLimit = errors.New("plan limit reached")

	// ErrInsufficientBalance indicates a billing wallet / credit
	// balance is insufficient for the operation. Distinct from generic
	// "system not in required state" cases so callers can distinguish
	// "your account is out of money" from "this resource is currently
	// suspended/locked." Wire code: FailedPrecondition.
	ErrInsufficientBalance = errors.New("insufficient balance")

	// ErrExpired indicates a time-limited resource (invitation, session,
	// password-reset token, license key) is no longer valid. Distinct
	// from NotFound (the resource never existed) and from generic
	// FailedPrecondition. Wire code: FailedPrecondition.
	ErrExpired = errors.New("expired")

	// ErrServerDraining indicates the call was ended because THIS replica
	// is shutting down — a rollout, a scale-down, a node drain — and not
	// because anything failed. The client reconnects and lands on a replica
	// that is still serving. Wire code: Unavailable, the code a client
	// already treats as "retry".
	//
	// It is the cause serverkit.Streams gives every long-lived stream it
	// ends at shutdown, and what a transport returns when it tells its own
	// peers about the drain in its own words ([Draining]). Distinct from
	// ErrUnavailable so logging and metrics can treat a drain as the
	// expected outcome of a rollout instead of a dependency outage: one
	// ERROR per connected client per deploy is noise that buries the faults.
	ErrServerDraining = errors.New("server draining: this replica is shutting down; reconnect to reach a live replica")

	// ErrScaffoldStub marks a handler forge scaffolded and NOBODY HAS
	// IMPLEMENTED YET. It is not a failure mode an application ever
	// chooses: application code returns ErrUnimplemented for a
	// deliberately-unimplemented RPC (a forwarder, a feature-flagged
	// path, an unwired optional dep). This sentinel means only "forge
	// wrote this and it was never replaced." Wire code: Unimplemented.
	//
	// WHY IT IS SEPARATE FROM ErrUnimplemented. The scaffold test row
	// forge emits alongside each stub asserts the RPC is not implemented,
	// so it goes red the moment you implement it — that failure IS the
	// prompt to write a real assertion. Keying that row on the bare
	// CodeUnimplemented made it unfalsifiable, because a finished handler
	// can return that code for its own reasons. The observed case: a
	// handler opening `if s.deps.X == nil { return unimplemented(rpc) }`
	// answers CodeUnimplemented on a test harness that leaves optional
	// deps nil, so the row kept passing against fully-implemented RPCs —
	// 78 of them in one project, none of them ever verified by the suite
	// that reported them green.
	//
	// The fix is that only forge's own untouched stub can produce this
	// value. Replace the stub and the row fails, whatever the replacement
	// returns. Do not return it from hand-written code — reach for
	// [Unimplemented] instead, which is what a real unimplemented RPC
	// means and what the scaffold row deliberately does NOT match.
	ErrScaffoldStub = errors.New("handler not implemented")
)

// ReasonScaffoldStub is the reason code [ScaffoldStub] annotates, carried
// to clients as [ReasonHeader] metadata.
//
// The reason header, not errors.Is, is what makes the scaffold row work
// over a real Connect client: unwrapping stops at the network boundary,
// so a client-side errors.Is(err, ErrScaffoldStub) is false even when the
// server returned exactly that. Metadata survives the roundtrip, which is
// why the sentinel is identified by this string on the wire and by
// errors.Is in process. [tdd.AssertScaffoldStub] accepts either.
const ReasonScaffoldStub = "forge_scaffold_stub"

// ScaffoldStub builds the error forge's generated handler stubs return —
// [ErrScaffoldStub] annotated with [ReasonScaffoldStub] so it is
// recognisable both in process and across the wire.
//
// It is called by generated code. Hand-written handlers should not use
// it; see [ErrScaffoldStub] for why, and prefer [Unimplemented].
func ScaffoldStub(rpc string) error {
	return WithReason(wrapped(ErrScaffoldStub, "handler for "+rpc+" not yet implemented"), ReasonScaffoldStub)
}

// Constructors wrap each sentinel with a human-readable cause. Use
// these when the service layer wants to communicate WHY the failure
// occurred ("user not found", "AI access is billed via wallet, not
// subscription"). The wrapped error still satisfies errors.Is against
// the sentinel and against the constructor's category, so handlers and
// tests can match on either.

// Canceled wraps ErrCanceled with the supplied detail.
func Canceled(detail string) error { return wrapped(ErrCanceled, detail) }

// Unknown wraps ErrUnknown with the supplied detail. Prefer a more
// specific category when one applies.
func Unknown(detail string) error { return faulted(ErrUnknown, detail) }

// InvalidArgument wraps ErrInvalidArgument with the supplied detail.
// Use for domain invariants violated post-validation; pure wire-format
// validation should fail earlier in the handler's validator.
func InvalidArgument(detail string) error { return wrapped(ErrInvalidArgument, detail) }

// DeadlineExceeded wraps ErrDeadlineExceeded with the supplied detail.
func DeadlineExceeded(detail string) error { return wrapped(ErrDeadlineExceeded, detail) }

// NotFound wraps ErrNotFound. Its parameter is the ENTITY name rather
// than a detail — the one constructor of which that is true — so it
// composes the sentence itself: NotFound("user") reads "user not found".
func NotFound(entity string) error {
	if entity == "" {
		return ErrNotFound
	}
	return wrapped(ErrNotFound, entity+" not found")
}

// AlreadyExists wraps ErrAlreadyExists with the supplied detail.
func AlreadyExists(detail string) error { return wrapped(ErrAlreadyExists, detail) }

// PermissionDenied wraps ErrPermissionDenied with the supplied reason.
func PermissionDenied(reason string) error { return wrapped(ErrPermissionDenied, reason) }

// ResourceExhausted wraps ErrResourceExhausted with the supplied detail.
// Use for rate limits, quota overruns, plan-limit-reached cases.
func ResourceExhausted(detail string) error { return wrapped(ErrResourceExhausted, detail) }

// FailedPrecondition wraps ErrFailedPrecondition with the supplied
// detail. Use when the operation was rejected because the system is
// not in the required state (e.g., cannot rotate a revoked key, cannot
// remove the last owner).
func FailedPrecondition(detail string) error { return wrapped(ErrFailedPrecondition, detail) }

// Aborted wraps ErrAborted with the supplied detail. Use for
// optimistic-concurrency / transactional-conflict cases.
func Aborted(detail string) error { return wrapped(ErrAborted, detail) }

// OutOfRange wraps ErrOutOfRange with the supplied detail.
func OutOfRange(detail string) error { return wrapped(ErrOutOfRange, detail) }

// Unimplemented wraps ErrUnimplemented with the supplied detail. Use
// for stubbed RPCs and feature-flagged paths.
func Unimplemented(detail string) error { return wrapped(ErrUnimplemented, detail) }

// Internal wraps ErrInternal with the supplied detail. Prefer letting
// generic errors fall through to ToConnect's CodeInternal default
// rather than constructing this explicitly — opaque internal errors
// shouldn't leak detail to clients.
func Internal(detail string) error { return faulted(ErrInternal, detail) }

// Unavailable wraps ErrUnavailable with the supplied detail.
func Unavailable(detail string) error { return faulted(ErrUnavailable, detail) }

// DataLoss wraps ErrDataLoss with the supplied detail.
func DataLoss(detail string) error { return faulted(ErrDataLoss, detail) }

// Unauthenticated wraps ErrUnauthenticated with the supplied reason.
func Unauthenticated(reason string) error { return wrapped(ErrUnauthenticated, reason) }

// PlanLimit wraps ErrPlanLimit with the supplied detail. Use when the
// caller's plan/tier disallows the operation (seat cap, feature gate,
// per-model allowlist). Distinct from ResourceExhausted so upstream
// code can react specifically to plan-related rejections.
func PlanLimit(detail string) error { return wrapped(ErrPlanLimit, detail) }

// InsufficientBalance wraps ErrInsufficientBalance with the supplied
// detail. Use when a billing wallet / credit account doesn't have
// funds for the operation.
func InsufficientBalance(detail string) error { return wrapped(ErrInsufficientBalance, detail) }

// Expired wraps ErrExpired with the supplied detail. Use for
// time-limited resources (invitation, session, password-reset token)
// that have aged out.
func Expired(detail string) error { return wrapped(ErrExpired, detail) }

// Draining wraps ErrServerDraining with the supplied detail: the message a
// transport tells its own peers when it ends their call because this
// replica is going away ("gateway draining: reconnect to a live replica").
// errors.Is(err, ErrServerDraining) holds, so every layer that recognises a
// drain recognises this one; the client reads Unavailable and the detail.
func Draining(detail string) error { return wrapped(ErrServerDraining, detail) }

// detailError is what every constructor returns: the application's own
// words, carrying a sentinel for identity.
//
// Error() is the DETAIL alone. connect.Error.Message() is literally
// err.Error(), so anything Error() appends is published to every caller —
// and the sentinel is an errors.Is identity, not a message. Formatting
// "<detail>: <sentinel>" put that identity on the wire:
//
//	order demo-order-ok cannot become ORDER_STATUS_DELIVERED: svcerr: failed precondition
//
// Nothing on the far side could use it. The Connect code already carries
// the category and ReasonHeader carries the machine-readable one, so the
// tag was pure noise — noise forge's own frontend runtime then had to
// delete with a regex before showing the message to a user.
//
// The identity is not lost: Unwrap returns the sentinel, so errors.Is,
// Code and codeForRecognized are unchanged. It simply stops being TEXT.
type detailError struct {
	detail   string
	sentinel error
}

func (e *detailError) Error() string { return e.detail }
func (e *detailError) Unwrap() error { return e.sentinel }

// wrapped is the canonical form used by every constructor: the client
// reads the detail, errors.Is(err, sentinel) still reports true. An empty
// detail has nothing to say, so the sentinel is returned as-is (its own
// text is readable prose for exactly this reason).
func wrapped(sentinel error, detail string) error {
	if detail == "" {
		return sentinel
	}
	return &detailError{detail: detail, sentinel: sentinel}
}

// ToConnect converts a domain error into a *connect.Error with the
// right code. Behavior:
//
//   - nil input returns nil.
//   - An already-Connect error (anything that errors.As-matches
//     *connect.Error) is returned as-is so handlers can construct
//     specific errors and still funnel them through the same helper.
//     A *connect.Error behind a [WithCause] cause is NOT one: the outer
//     error classifies, and the cause stays server-side.
//   - A wrapped sentinel from this package maps to the matching
//     connect.Code; the original error is preserved as the cause so
//     errors.Is keeps working downstream.
//   - context.Canceled / context.DeadlineExceeded map to
//     CodeCanceled / CodeDeadlineExceeded so handler-side cancellation
//     is surfaced cleanly.
//   - Anything else — a raw driver / SDK error this package does not
//     recognise — maps to CodeInternal with the FIXED message
//     [InternalMessage]. Its text is never client-visible, because that
//     text is written by postgres or a vendor SDK and routinely contains
//     SQL, schema names and connection strings. The original is kept as
//     an unexported cause, reachable server-side via [Cause] and by
//     errors.Is / errors.As.
func ToConnect(err error) *connect.Error {
	if err == nil {
		return nil
	}
	ce, ok := visibleAs[*connect.Error](err)
	if !ok {
		code := codeFor(err)
		if faultCode(code) {
			// The first place svcerr sees a server fault no constructor
			// stamped — a driver error, a bare sentinel — is the best origin
			// there is. Client-class codes skip this: no stack, no cost.
			err = withStack(err, 2)
		}
		ce = connect.NewError(code, clientSafe(err))
	}
	// A reason code annotated via WithReason rides through to the client
	// as Connect error metadata under ReasonHeader, so the frontend routes
	// on a stable code instead of matching the human-readable message text.
	// Applied to both freshly-built and passed-through Connect errors.
	// Absent a WithReason annotation the metadata is left untouched, so
	// the no-reason path is byte-identical to before.
	if reason, ok := reasonOf(err); ok {
		ce.Meta().Set(ReasonHeader, reason)
	}
	// Metadata annotated via WithMetadata rides the same way. The nearest
	// annotation of a key wins, as the nearest WithReason does.
	for key, value := range metadataOf(err) {
		ce.Meta().Set(key, value)
	}
	return ce
}

// Wrap is the canonical handler-layer construct:
//
//	if err != nil {
//	    return nil, svcerr.Wrap(err)
//	}
//
// It is identical to ToConnect but returns error so it composes with
// `return ..., err`-style handler signatures. Wrap(nil) returns nil.
func Wrap(err error) error {
	ce := ToConnect(err)
	if ce == nil {
		return nil
	}
	return ce
}

// InternalMessage is the ONLY text an unrecognised server-side failure
// puts on the wire. It matches what the plain-HTTP stack
// (middleware.HTTPStack) has always returned for a 500, so a client sees
// the same thing whichever transport it used.
//
// The distinction that matters: a message the APPLICATION chose —
// svcerr.NotFound("user"), svcerr.Internal("create order failed") — is
// intentional and passes through untouched. A message the application
// never wrote — `ERROR: relation "accounts" does not exist (SQLSTATE
// 42P01) dsn=postgres://app:s3cr3t@db.internal:5432/prod` — is not a
// message at all, it is a diagnostic, and diagnostics are for the
// operator. connect.Error.Message() is literally err.Error(), so
// handing a driver error to connect.NewError publishes it verbatim to
// an unauthenticated caller.
const InternalMessage = "internal server error"

// redactedError separates what the CLIENT may read from what the SERVER
// needs to keep.
//
//   - Error — and therefore connect.Error.Message — returns only msg.
//   - Unwrap returns err, the error the client is CLASSIFIED by: the code,
//     the message, the reason header and the class are all read down this
//     chain (see visible.go).
//   - cause is the server-only detail. It is reachable through [Cause], and
//     through errors.Is / errors.As via the Is and As methods below — but
//     never through Unwrap, so nothing walking the chain to decide what the
//     client is told can land on it.
//
// When ToConnect redacts an unrecognised error, the detail withheld IS err —
// its text is the thing being kept off the wire — and cause is nil.
type redactedError struct {
	msg   string
	err   error
	cause error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.err }

// Is lets errors.Is reach the cause, so a server-side sentinel check keeps
// working through WithCause.
func (e *redactedError) Is(target error) bool {
	return e.cause != nil && errors.Is(e.cause, target)
}

// As lets errors.As reach the cause (`var pgErr *pgconn.PgError` still
// matches) — for every type but *connect.Error. That one is withheld because
// errors.As is exactly how connect-go maps a handler's returned error to the
// response: a handler that returned a WithCause value without svcerr.Wrap
// would otherwise publish a downstream service's code and message verbatim.
// Server code that wants the downstream verdict reads it from [Cause].
func (e *redactedError) As(target any) bool {
	if e.cause == nil {
		return false
	}
	if _, ok := target.(**connect.Error); ok {
		return false
	}
	return errors.As(e.cause, target)
}

// withheld is what the client was not shown.
func (e *redactedError) withheld() error {
	if e.cause != nil {
		return unstack(e.cause)
	}
	return unstack(e.err)
}

// WithCause returns an error that shows err's message to the client
// while carrying cause for the server's eyes only.
//
// Use it wherever a safe, deliberate summary and an unsafe diagnostic
// both exist and both matter — the classic case being a repository
// failure, where the client should learn "get item failed" and the
// operator needs the *pgconn.PgError behind it:
//
//	return svcerr.Wrap(svcerr.WithCause(
//	    svcerr.Internal(fmt.Sprintf("%s %s failed", op, entity)), dbErr))
//
// err decides everything the client reads: the Connect code, the message,
// the [ReasonHeader] reason, and the [Class] observability derives from
// them. cause decides none of it — not when it is another svcerr kind, not
// when it carries its own reason or WithClass marker, and not when it is a
// *connect.Error from a downstream call, whose code and message describe
// the DEPENDENCY's failure and routinely name internal hosts. The one thing a
// cause informs is cancellation: a request whose context was canceled is
// [ClassCanceled] however it was labelled (see [Classify]).
//
// cause stays reachable for the server: through [Cause] for the log, and
// through errors.Is / errors.As (so `var pgErr *pgconn.PgError` still
// matches) — except that errors.As never yields a *connect.Error from it,
// because connect-go publishes whatever errors.As finds there.
func WithCause(err, cause error) error {
	if err == nil {
		return nil
	}
	if cause == nil {
		return err
	}
	return &redactedError{msg: err.Error(), err: err, cause: cause}
}

// Cause returns the server-only detail withheld from the client, or nil
// when err carries none. It is what a logging interceptor calls to log
// the truth about a failure the client was only told "internal server
// error" about.
//
// It sees through *connect.Error, so calling it on the value a handler
// returned works without unwrapping first.
func Cause(err error) error {
	var re *redactedError
	if errors.As(err, &re) {
		return re.withheld()
	}
	return nil
}

// clientMessage returns the text this package is willing to publish for
// err, and whether it recognised the error at all.
//
// It reads the NEAREST detail — never the accumulated Error() string. That
// distinction is the whole guard. A recognised sentinel says "this failure
// is a NotFound"; it says nothing about the text wrapped around it on the
// way out, and the service-layer skill tells authors to wrap:
//
//	return Thing{}, fmt.Errorf("get thing: %w", err)
//
// Because connect.Error.Message() is literally err.Error(), publishing the
// outer string published whatever an author put in it — request ids,
// internal hostnames, and in the measured case a DSN with a password, to
// an unauthenticated caller. Wrapping is for the OPERATOR: it survives as a
// cause (see [Cause]) and reaches the log. The client reads the detail the
// constructor was given, and a bare sentinel's own category text when
// there is no detail.
//
// The corollary for callers: prose meant for a client goes in the
// constructor's argument. Nothing outside it crosses — and nothing behind a
// WithCause cause, which is read only through the client view.
func clientMessage(err error) (string, bool) {
	// An explicit WithMessage override wins over everything below it. It
	// MUST be checked first: the error it wraps is usually a constructor
	// result, so the detailError branch would otherwise match the inner
	// error and publish the composed text the override exists to replace
	// ("that coupon code isn't valid" losing to "coupon not found").
	if me, ok := visibleAs[*messageError](err); ok {
		return me.msg, true
	}
	if de, ok := visibleAs[*detailError](err); ok {
		return de.detail, true
	}
	if _, sentinel, recognised := codeForRecognized(err); recognised {
		return sentinel.Error(), true
	}
	return "", false
}

// clientSafe reduces err to what the client may read. An unrecognised
// error carries text nobody on this side of the wire chose, so only
// [InternalMessage] goes out; either way the original is kept as a cause
// and stays errors.Is/As-able.
func clientSafe(err error) error {
	msg, ok := clientMessage(err)
	if !ok {
		return &redactedError{msg: InternalMessage, err: err}
	}
	if msg == err.Error() {
		return err // nothing was wrapped around it: the error IS the message
	}
	return &redactedError{msg: msg, err: err}
}

// Code returns the connect.Code that ToConnect would assign to err — the
// code the client is told. An error nothing recognises is CodeInternal, as
// ToConnect sends it; a nil err is CodeUnknown, matching connect.CodeOf.
//
// Like ToConnect it reads only the outer error of a [WithCause]: a kind or a
// *connect.Error in the cause does not change it.
func Code(err error) connect.Code {
	if err == nil {
		return connect.CodeUnknown
	}
	if ce, ok := visibleAs[*connect.Error](err); ok {
		return ce.Code()
	}
	return codeFor(err)
}

// codeFor walks the sentinel→code mapping. Order does not matter
// because each sentinel is distinct; the function returns CodeInternal
// when no sentinel matches and the error is not a context cancellation.
func codeFor(err error) connect.Code {
	code, _, _ := codeForRecognized(err)
	return code
}

// codeForRecognized is codeFor plus the fact that decides whether the
// error's MESSAGE may be shown to a client: whether a sentinel from this
// package (or a context cancellation) actually matched.
//
// recognised=true means the application built this error deliberately —
// svcerr.NotFound("user"), svcerr.Internal("create order failed") — so
// its text was written by someone on this side of the wire.
// recognised=false is the fallback branch: the error came from a driver,
// an SDK, or the standard library, its text was written by neither the
// application nor forge, and CodeInternal is a guess rather than a
// decision. Only that branch gets redacted; see clientSafe.
//
// It reads the client view (visibleIs): a sentinel behind a WithCause cause
// is the server's business and recognises nothing here.
func codeForRecognized(err error) (connect.Code, error, bool) {
	switch {
	case visibleIs(err, context.Canceled), visibleIs(err, ErrCanceled):
		return connect.CodeCanceled, ErrCanceled, true
	case visibleIs(err, context.DeadlineExceeded), visibleIs(err, ErrDeadlineExceeded):
		return connect.CodeDeadlineExceeded, ErrDeadlineExceeded, true
	case visibleIs(err, ErrInvalidArgument):
		return connect.CodeInvalidArgument, ErrInvalidArgument, true
	case visibleIs(err, ErrNotFound):
		return connect.CodeNotFound, ErrNotFound, true
	case visibleIs(err, ErrAlreadyExists):
		return connect.CodeAlreadyExists, ErrAlreadyExists, true
	case visibleIs(err, ErrPermissionDenied):
		return connect.CodePermissionDenied, ErrPermissionDenied, true
	// ErrPlanLimit MUST be checked before ErrResourceExhausted because
	// constructors share the same Connect code; matching the broader
	// sentinel first would shadow the more specific one.
	case visibleIs(err, ErrPlanLimit):
		return connect.CodeResourceExhausted, ErrPlanLimit, true
	case visibleIs(err, ErrResourceExhausted):
		return connect.CodeResourceExhausted, ErrResourceExhausted, true
	// Same shadowing rule: ErrInsufficientBalance and ErrExpired share
	// FailedPrecondition with the canonical sentinel; check them first.
	case visibleIs(err, ErrInsufficientBalance):
		return connect.CodeFailedPrecondition, ErrInsufficientBalance, true
	case visibleIs(err, ErrExpired):
		return connect.CodeFailedPrecondition, ErrExpired, true
	case visibleIs(err, ErrFailedPrecondition):
		return connect.CodeFailedPrecondition, ErrFailedPrecondition, true
	case visibleIs(err, ErrAborted):
		return connect.CodeAborted, ErrAborted, true
	case visibleIs(err, ErrOutOfRange):
		return connect.CodeOutOfRange, ErrOutOfRange, true
	// ErrScaffoldStub shares Unimplemented's wire code, so it is checked
	// first for the same reason ErrPlanLimit precedes ErrResourceExhausted:
	// the sentinels are distinct values and matching the broader one first
	// would shadow the specific one.
	case visibleIs(err, ErrScaffoldStub):
		return connect.CodeUnimplemented, ErrScaffoldStub, true
	case visibleIs(err, ErrUnimplemented):
		return connect.CodeUnimplemented, ErrUnimplemented, true
	case visibleIs(err, ErrInternal):
		return connect.CodeInternal, ErrInternal, true
	// ErrServerDraining shares Unavailable's wire code: checked first, for
	// the same shadowing reason as ErrPlanLimit.
	case visibleIs(err, ErrServerDraining):
		return connect.CodeUnavailable, ErrServerDraining, true
	case visibleIs(err, ErrUnavailable):
		return connect.CodeUnavailable, ErrUnavailable, true
	case visibleIs(err, ErrDataLoss):
		return connect.CodeDataLoss, ErrDataLoss, true
	case visibleIs(err, ErrUnauthenticated):
		return connect.CodeUnauthenticated, ErrUnauthenticated, true
	case visibleIs(err, ErrUnknown):
		return connect.CodeUnknown, ErrUnknown, true
	default:
		return connect.CodeInternal, nil, false
	}
}

// IsCanceled reports whether err carries (or wraps) ErrCanceled or
// context.Canceled.
func IsCanceled(err error) bool {
	return errors.Is(err, ErrCanceled) || errors.Is(err, context.Canceled)
}

// IsDeadlineExceeded reports whether err carries (or wraps)
// ErrDeadlineExceeded or context.DeadlineExceeded.
func IsDeadlineExceeded(err error) bool {
	return errors.Is(err, ErrDeadlineExceeded) || errors.Is(err, context.DeadlineExceeded)
}

// IsInvalidArgument reports whether err carries (or wraps) ErrInvalidArgument.
func IsInvalidArgument(err error) bool { return errors.Is(err, ErrInvalidArgument) }

// IsNotFound reports whether err carries (or wraps) ErrNotFound.
func IsNotFound(err error) bool { return errors.Is(err, ErrNotFound) }

// IsAlreadyExists reports whether err carries (or wraps) ErrAlreadyExists.
func IsAlreadyExists(err error) bool { return errors.Is(err, ErrAlreadyExists) }

// IsPermissionDenied reports whether err carries (or wraps) ErrPermissionDenied.
func IsPermissionDenied(err error) bool { return errors.Is(err, ErrPermissionDenied) }

// IsResourceExhausted reports whether err carries (or wraps) ErrResourceExhausted.
func IsResourceExhausted(err error) bool { return errors.Is(err, ErrResourceExhausted) }

// IsFailedPrecondition reports whether err carries (or wraps) ErrFailedPrecondition.
func IsFailedPrecondition(err error) bool { return errors.Is(err, ErrFailedPrecondition) }

// IsAborted reports whether err carries (or wraps) ErrAborted.
func IsAborted(err error) bool { return errors.Is(err, ErrAborted) }

// IsOutOfRange reports whether err carries (or wraps) ErrOutOfRange.
func IsOutOfRange(err error) bool { return errors.Is(err, ErrOutOfRange) }

// IsUnimplemented reports whether err carries (or wraps) ErrUnimplemented.
func IsUnimplemented(err error) bool { return errors.Is(err, ErrUnimplemented) }

// IsInternal reports whether err carries (or wraps) ErrInternal.
func IsInternal(err error) bool { return errors.Is(err, ErrInternal) }

// IsUnavailable reports whether err carries (or wraps) ErrUnavailable.
func IsUnavailable(err error) bool { return errors.Is(err, ErrUnavailable) }

// IsDataLoss reports whether err carries (or wraps) ErrDataLoss.
func IsDataLoss(err error) bool { return errors.Is(err, ErrDataLoss) }

// IsUnauthenticated reports whether err carries (or wraps) ErrUnauthenticated.
func IsUnauthenticated(err error) bool { return errors.Is(err, ErrUnauthenticated) }

// IsPlanLimit reports whether err carries (or wraps) ErrPlanLimit.
// Distinct from IsResourceExhausted (which would also match rate-limit
// errors); use this when you specifically need "plan-tier rejected."
func IsPlanLimit(err error) bool { return errors.Is(err, ErrPlanLimit) }

// IsInsufficientBalance reports whether err carries (or wraps)
// ErrInsufficientBalance.
func IsInsufficientBalance(err error) bool { return errors.Is(err, ErrInsufficientBalance) }

// IsExpired reports whether err carries (or wraps) ErrExpired.
func IsExpired(err error) bool { return errors.Is(err, ErrExpired) }

// IsServerDraining reports whether err carries (or wraps) ErrServerDraining:
// the call ended because this replica is shutting down. Matching is by
// identity, never by message.
func IsServerDraining(err error) bool { return errors.Is(err, ErrServerDraining) }

// IsClassified reports whether err carries a verdict someone on this side of
// the wire made deliberately — a *connect.Error, or any sentinel from this
// package. It is the question "does this error already know its own code?",
// which is what a caller needs before deciding whether to impose one.
//
// The distinction matters wherever a boundary receives errors from two
// sources. A driver or SDK error is unclassified: nobody chose CodeInternal
// for it, so imposing that default is honest. An svcerr sentinel is the
// opposite — the application named the failure, and overwriting its code
// discards a decision. Without this predicate the only cheap test is
// `errors.As(&connect.Error{})`, which sees the first kind and misses the
// second, silently turning a caller's 400 into a 500.
//
// The verdict must be the outer error's: a classified [WithCause] cause does
// not classify an unclassified error, because a caller that passes a
// classified error through verbatim would forward the cause's verdict.
func IsClassified(err error) bool {
	if err == nil {
		return false
	}
	if _, ok := visibleAs[*connect.Error](err); ok {
		return true
	}
	_, _, recognised := codeForRecognized(err)
	return recognised
}

// WrapOr is [Wrap] for an error that may or may not already know its code:
//
//	rows, err := s.deps.Projects.List(ctx, userID)
//	if err != nil {
//	    return nil, svcerr.WrapOr(err, svcerr.Internal("failed to list projects"))
//	}
//
// When err is classified ([IsClassified]: an svcerr kind, a context
// cancellation or deadline, or a *connect.Error) it is Wrap(err): the
// verdict someone already made — a store's NotFound, a deadline, a
// validator's InvalidArgument — reaches the client with its own code and
// message. Otherwise it is Wrap(WithCause(fallback, err)): the client reads
// fallback's code, message, reason and class, and err stays the server-side
// cause the logging interceptor reports (see [Cause]), reachable through
// errors.Is / errors.As.
//
// It replaces two spellings that are each wrong half the time:
// `connect.NewError(connect.CodeInternal, err)` publishes err's text (a
// driver's SQL, a DSN, a host) and flattens a NotFound into Internal, and
// `return nil, svcerr.Internal("failed to list projects")` with err dropped
// turns a deadline into a server fault and loses the cause from the log.
//
// A *connect.Error passes through verbatim, because it is a verdict too:
// one this process built while it migrates to svcerr, or one a helper
// returns. That includes a *connect.Error a REMOTE Connect client returned,
// whose code and message describe the dependency's failure — so the result
// of a call to another service never goes through WrapOr. Name what this
// rpc tells its own caller instead:
//
//	svcerr.Wrap(svcerr.WithCause(svcerr.Unavailable("billing is unavailable"), remoteErr))
//
// WrapOr(nil, _) is nil. A nil fallback is treated as [ErrInternal]. An
// unclassified fallback is redacted like any other unrecognised error: the
// client reads [InternalMessage].
func WrapOr(err, fallback error) error {
	if err == nil {
		return nil
	}
	if IsClassified(err) {
		return Wrap(err)
	}
	if fallback == nil {
		fallback = ErrInternal
	}
	return Wrap(WithCause(fallback, err))
}

// WithDetail attaches a structured proto.Message detail to the connect
// error that ToConnect would build for err. Used when the service
// layer wants to surface client-readable structured context (e.g., a
// validation-failure message describing which field failed).
//
// Behavior:
//   - WithDetail(nil, _) returns nil.
//   - WithDetail(err, nil) returns ToConnect(err) unchanged.
//   - WithDetail(err, msg) returns ToConnect(err) with the supplied
//     proto attached as a *connect.ErrorDetail. If detail-encoding
//     fails (which would only happen for a non-marshalable proto, in
//     practice never), the error is returned without the detail —
//     attaching detail is best-effort and never masks the underlying
//     mapping.
//
// The returned error is always a *connect.Error so handlers can
// `return nil, svcerr.WithDetail(err, &validationFailure{...})`
// directly.
func WithDetail(err error, detail proto.Message) error {
	if err == nil {
		return nil
	}
	ce := ToConnect(err)
	if ce == nil {
		return nil
	}
	if detail == nil {
		return ce
	}
	d, dErr := connect.NewErrorDetail(detail)
	if dErr != nil {
		// Best-effort: return the mapped error without the detail
		// rather than masking the original failure with a detail-
		// encoding error.
		return ce
	}
	ce.AddDetail(d)
	return ce
}

// messageError replaces the text a client reads while leaving the error
// chain — and therefore the Connect code — completely untouched.
//
// Error() is msg alone, which is what makes it the client-visible message:
// connect.Error.Message() is literally err.Error(). Unwrap returns the
// original, so errors.Is/errors.As, codeFor and codeForRecognized all keep
// working through it, and the WRAPPED error survives as the server-side
// cause (see [Cause]) exactly as it would have without the override.
type messageError struct {
	msg string
	err error
}

func (e *messageError) Error() string { return e.msg }
func (e *messageError) Unwrap() error { return e.err }

// WithMessage sets the EXACT text the client reads, on any code, without
// changing the code or breaking the error chain.
//
//	// service layer keeps its own domain sentinel:
//	return svcerr.WithMessage(svcerr.NotFound("coupon"), "that coupon code isn't valid")
//	// handler layer, unchanged: return nil, svcerr.Wrap(err)
//
// # Why this exists
//
// Every other constructor here composes its message from a DETAIL plus a
// shape the package chose. NotFound is the clearest case: it appends " not
// found", so NotFound("that coupon code isn't valid") renders "that coupon
// code isn't valid not found" and there is no argument that produces the
// sentence a user should see. Nor can WithCause be used for it — that
// deliberately takes the OUTER error's message and demotes the inner one to
// a server-side cause, so the display copy is the thing it drops.
//
// The practical consequence was that any handler needing user-facing copy on
// a standard code had to leave svcerr and hand-roll
// connect.NewError(connect.CodeNotFound, errors.New("...")) — which is the
// per-handler error mapping forgeconv-no-handler-error-mapping exists to
// stop, and which loses the sentinel, the reason header and the cause along
// with it. The rule was right and the library had no answer; this is the
// answer.
//
// # What it does NOT do
//
// It does not widen what reaches the wire. The message is a string the
// application passes in deliberately — the same standing as the detail given
// to any constructor — and it REPLACES the message rather than appending to
// it, so no wrapped context comes along. An unrecognised driver error given
// a message is still an unrecognised driver error: it keeps CodeInternal,
// and its own text stays withheld as the cause.
//
// Behavior:
//   - WithMessage(nil, _) returns nil.
//   - WithMessage(err, "") returns err unchanged — an empty message is not a
//     message, and silently publishing "" would be worse than declining.
//   - Otherwise the returned error carries msg as its client-visible text,
//     preserves err's Connect code, stays errors.Is/As-transparent, and
//     composes with WithReason, WithCause and WithDetail in any order.
func WithMessage(err error, msg string) error {
	if err == nil {
		return nil
	}
	if msg == "" {
		return err
	}
	return &messageError{msg: msg, err: err}
}

// reasonError annotates an error with a stable machine-readable reason
// code without disturbing the underlying error chain. It Unwraps to the
// wrapped error, so errors.Is/errors.As, codeFor, and ToConnect's
// already-Connect passthrough all keep working through it; the reason is
// only materialized as wire metadata at the ToConnect boundary.
type reasonError struct {
	code string
	err  error
}

func (r *reasonError) Error() string { return r.err.Error() }
func (r *reasonError) Unwrap() error { return r.err }

// WithReason annotates err with a stable, machine-readable reason code
// that the svcerr→Connect mapping surfaces as error metadata under
// ReasonHeader ("x-forge-error-reason"). The reason code is a thin,
// explicit convention: the app author supplies a domain-specific
// snake_case code and forge carries it to the wire, where the frontend
// error mapper routes on the code instead of matching message text.
//
//	// service layer:
//	return svcerr.WithReason(
//	    svcerr.FailedPrecondition("no active subscription"),
//	    "no_active_subscription",
//	)
//	// handler layer (unchanged): return nil, svcerr.Wrap(err)
//
// Behavior:
//   - WithReason(nil, _) returns nil.
//   - WithReason(err, "") returns err unchanged (no annotation, so the
//     mapped Connect error carries no ReasonHeader metadata).
//   - Otherwise the returned error wraps err, preserves errors.Is/As and
//     the mapped Connect code, and carries the code through Wrap/ToConnect
//     to ReasonHeader metadata.
//
// svcerr itself never invents reason codes; it only plumbs the ones its
// caller supplies. forge's own generated-CRUD layer IS such a caller: it
// defines a fixed vocabulary (the crud.Reason* constants) and stamps one
// on every error it produces, so a scaffolded app's frontend never reads
// a null reason on a CRUD path. Hand-written handlers choose their own
// codes; reuse a crud.Reason* name where the meaning matches.
func WithReason(err error, code string) error {
	if err == nil {
		return nil
	}
	if code == "" {
		return err
	}
	return &reasonError{code: code, err: err}
}

// metadataError annotates an error with one extra Connect error-metadata
// entry. Like reasonError it Unwraps to the wrapped error and is materialized
// only at the ToConnect boundary.
type metadataError struct {
	key, value string
	err        error
}

func (m *metadataError) Error() string { return m.err.Error() }
func (m *metadataError) Unwrap() error { return m.err }

// WithMetadata annotates err with one Connect error-metadata entry, which
// ToConnect (and so Wrap) sets on the wire error beside [ReasonHeader].
//
// It exists for the STRUCTURED FACTS a reason implies: a reason says which
// refusal happened, and some refusals have a datum the client needs to act
// on — the scopes a credential lacks, say. Putting that datum in the message
// would put the client back to parsing prose, which is what the reason
// header exists to end.
//
//	return svcerr.WithMetadata(
//	    svcerr.WithReason(svcerr.PermissionDenied(msg), "scope_missing"),
//	    "x-forge-error-scopes", "deploy:write,secret:read",
//	)
//
// Behavior:
//   - WithMetadata(nil, _, _) returns nil.
//   - An empty key or an empty value returns err unchanged.
//   - Otherwise the returned error wraps err, preserves errors.Is/As and the
//     mapped Connect code and message, and composes with WithReason,
//     WithMessage, WithCause and WithDetail in any order. The nearest
//     annotation of a key wins. Metadata behind a WithCause cause is not
//     err's and is not sent.
//
// Keys are the caller's. Prefer an "x-<product>-error-<name>" spelling, and
// remember that a browser client reads it only if the server's CORS policy
// exposes it.
func WithMetadata(err error, key, value string) error {
	if err == nil {
		return nil
	}
	if key == "" || value == "" {
		return err
	}
	return &metadataError{key: key, value: value, err: err}
}

// metadataOf collects every WithMetadata annotation on err's client-visible
// chain, nearest first, keeping the nearest value of each key.
func metadataOf(err error) map[string]string {
	var out map[string]string
	// walkVisible follows Unwrap only, so a WithCause cause is never reached.
	walkVisible(err, func(e error) bool {
		if m, ok := e.(*metadataError); ok {
			if out == nil {
				out = map[string]string{}
			}
			if _, seen := out[m.key]; !seen {
				out[m.key] = m.value
			}
		}
		return false
	})
	return out
}

// reasonOf returns the nearest reason code annotated on err's chain, if
// any. Additional wrapping (fmt.Errorf("...: %w", err)) around a
// WithReason error is transparent because the walk follows Unwrap. A reason
// behind a WithCause cause is not err's reason and is not returned.
func reasonOf(err error) (string, bool) {
	if re, ok := visibleAs[*reasonError](err); ok {
		return re.code, true
	}
	return "", false
}
