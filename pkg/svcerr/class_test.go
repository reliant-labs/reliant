package svcerr_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// class_test.go — who has to act on an error.
//
// A product's prod ERROR stream was dominated by conditions only the user
// could fix — a disconnected laptop, an exhausted provider subscription — so
// Sentry and the ERROR alert paged on the system working correctly. Classify
// is the one place that decides; these tests pin both halves: what the kind
// implies, and what an explicit marker overrides.

func TestClassify_ByKind(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want svcerr.Class
	}{
		{"nil", nil, svcerr.ClassNone},

		// The caller or user must act.
		{"invalid argument", svcerr.InvalidArgument("name required"), svcerr.ClassUser},
		{"not found", svcerr.NotFound("user"), svcerr.ClassUser},
		{"bare not found sentinel", svcerr.ErrNotFound, svcerr.ClassUser},
		{"already exists", svcerr.AlreadyExists("slug taken"), svcerr.ClassUser},
		{"permission denied", svcerr.PermissionDenied("admin only"), svcerr.ClassUser},
		{"unauthenticated", svcerr.Unauthenticated("no token"), svcerr.ClassUser},
		{"failed precondition", svcerr.FailedPrecondition("not ready"), svcerr.ClassUser},
		{"insufficient balance", svcerr.InsufficientBalance("wallet empty"), svcerr.ClassUser},
		{"expired", svcerr.Expired("invite expired"), svcerr.ClassUser},
		{"out of range", svcerr.OutOfRange("page 9"), svcerr.ClassUser},
		{"resource exhausted", svcerr.ResourceExhausted("rate limited"), svcerr.ClassUser},
		{"plan limit", svcerr.PlanLimit("seat cap"), svcerr.ClassUser},
		{"hand-built connect 4xx", connect.NewError(connect.CodeNotFound, errors.New("nope")), svcerr.ClassUser},

		// The caller went away.
		{"context canceled", context.Canceled, svcerr.ClassCanceled},
		{"wrapped context canceled", fmt.Errorf("stream: %w", context.Canceled), svcerr.ClassCanceled},
		{"canceled sentinel", svcerr.Canceled("stopped"), svcerr.ClassCanceled},
		{"connect canceled", connect.NewError(connect.CodeCanceled, errors.New("client closed")), svcerr.ClassCanceled},

		// This replica went away: a rollout drained it. The client reads
		// Unavailable, and still nobody has to act.
		{"server draining", svcerr.Draining("gateway draining: reconnect"), svcerr.ClassCanceled},
		{"bare draining sentinel", svcerr.ErrServerDraining, svcerr.ClassCanceled},
		{"draining behind a handler's label", svcerr.WithCause(svcerr.Internal("stream ended"), svcerr.ErrServerDraining), svcerr.ClassCanceled},

		// Our system failed, or cannot tell that it did not.
		{"unrecognised raw error", errors.New("pq: connection refused"), svcerr.ClassServer},
		{"internal", svcerr.Internal("write failed"), svcerr.ClassServer},
		{"unavailable", svcerr.Unavailable("payments offline"), svcerr.ClassServer},
		{"data loss", svcerr.DataLoss("checksum mismatch"), svcerr.ClassServer},
		{"unknown", svcerr.Unknown("?"), svcerr.ClassServer},
		{"deadline exceeded", context.DeadlineExceeded, svcerr.ClassServer},
		{"aborted", svcerr.Aborted("conflict"), svcerr.ClassServer},
		{"unimplemented", svcerr.Unimplemented("later"), svcerr.ClassServer},
		{"hand-built connect 5xx", connect.NewError(connect.CodeInternal, errors.New("boom")), svcerr.ClassServer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := svcerr.Classify(tc.err); got != tc.want {
				t.Errorf("Classify = %v, want %v", got, tc.want)
			}
			if tc.err == nil {
				return
			}
			// The class must not depend on where in the stack it is asked:
			// wrapped for context by the service layer, and again as the
			// *connect.Error the handler returns.
			wrapped := fmt.Errorf("op: %w", tc.err)
			if got := svcerr.Classify(wrapped); got != tc.want {
				t.Errorf("Classify(fmt.Errorf(%%w)) = %v, want %v", got, tc.want)
			}
			if got := svcerr.Classify(svcerr.Wrap(wrapped)); got != tc.want {
				t.Errorf("Classify(Wrap(...)) = %v, want %v", got, tc.want)
			}
			if got, want := svcerr.IsUserError(tc.err), tc.want == svcerr.ClassUser; got != want {
				t.Errorf("IsUserError = %v, want %v", got, want)
			}
		})
	}
}

func TestWithClass_OverridesKind(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
		want svcerr.Class
	}{
		// The motivating case: Unavailable is the honest code for "your
		// machine is offline", and nobody on the server side can fix it.
		{"unavailable marked user", svcerr.WithClass(svcerr.Unavailable("your machine is not connected"), svcerr.ClassUser), svcerr.ClassUser},
		// A worker-side error that never crosses an RPC boundary.
		{"raw error marked user", svcerr.WithClass(errors.New("provider rejected the credential"), svcerr.ClassUser), svcerr.ClassUser},
		// A 4xx that is really ours.
		{"not found marked server", svcerr.WithClass(svcerr.NotFound("project row"), svcerr.ClassServer), svcerr.ClassServer},
		// The nearest marker wins.
		{"outer marker wins", svcerr.WithClass(svcerr.WithClass(errors.New("x"), svcerr.ClassUser), svcerr.ClassServer), svcerr.ClassServer},
		// An explicit marker beats the cancellation rule as well.
		{"canceled marked user", svcerr.WithClass(context.Canceled, svcerr.ClassUser), svcerr.ClassUser},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, err := range []error{
				tc.err,
				fmt.Errorf("resolve daemon: %w", tc.err),
				svcerr.Wrap(fmt.Errorf("resolve daemon: %w", tc.err)),
				svcerr.WithReason(tc.err, "machine_offline"),
			} {
				if got := svcerr.Classify(err); got != tc.want {
					t.Errorf("Classify(%v) = %v, want %v", err, got, tc.want)
				}
			}
		})
	}
}

// TestWithClass_IsInvisibleOnTheWire: the marker changes who observability
// says must act, and nothing a client can see.
func TestWithClass_IsInvisibleOnTheWire(t *testing.T) {
	t.Parallel()

	inner := svcerr.WithReason(svcerr.Unavailable("your machine is not connected"), "machine_offline")
	marked := svcerr.WithClass(inner, svcerr.ClassUser)

	if !errors.Is(marked, svcerr.ErrUnavailable) {
		t.Error("errors.Is lost the sentinel through WithClass")
	}
	if got := svcerr.Code(marked); got != connect.CodeUnavailable {
		t.Errorf("Code = %v, want unavailable — the marker must not change the code", got)
	}
	if marked.Error() != inner.Error() {
		t.Errorf("Error() = %q, want %q — the marker must not change the text", marked.Error(), inner.Error())
	}
	ce := svcerr.ToConnect(marked)
	if ce.Code() != connect.CodeUnavailable || ce.Message() != "your machine is not connected" {
		t.Errorf("wire = %v %q, want unavailable %q", ce.Code(), ce.Message(), "your machine is not connected")
	}
	if got := ce.Meta().Get(svcerr.ReasonHeader); got != "machine_offline" {
		t.Errorf("reason header = %q, want machine_offline", got)
	}

	// A typed error under the marker stays reachable with errors.As.
	var pathErr *typedErr
	if !errors.As(svcerr.WithClass(fmt.Errorf("x: %w", &typedErr{}), svcerr.ClassUser), &pathErr) {
		t.Error("errors.As lost a typed error through WithClass")
	}

	// Redaction is untouched: marking a raw driver error does not make its
	// text client-visible.
	raw := svcerr.ToConnect(svcerr.WithClass(errors.New("dsn=postgres://u:secret@db"), svcerr.ClassUser))
	if raw.Code() != connect.CodeInternal || raw.Message() != svcerr.InternalMessage {
		t.Errorf("raw marked error reached the wire as %v %q", raw.Code(), raw.Message())
	}
}

// usageLimitErr is a typed error that knows its own class, the way a
// provider's "subscription out of credit" error does.
type usageLimitErr struct{ class svcerr.Class }

func (e *usageLimitErr) Error() string            { return "AI provider usage limit reached" }
func (e *usageLimitErr) ErrorClass() svcerr.Class { return e.class }

func TestClassify_TypedErrorDeclaresItsClass(t *testing.T) {
	t.Parallel()
	declared := &usageLimitErr{class: svcerr.ClassUser}
	// Outside a WithCause, not behind one: a class declared on a CAUSE does
	// not count (TestWithCause_OuterClassifies).
	for name, err := range map[string]error{
		"bare":                     declared,
		"wrapped":                  fmt.Errorf("stream: %w", declared),
		"under an internal code":   connect.NewError(connect.CodeInternal, fmt.Errorf("call llm: %w", declared)),
		"joined with a raw error":  errors.Join(errors.New("cleanup failed too"), declared),
		"outside a redacted cause": svcerr.WithCause(declared, errors.New("provider said 429")),
	} {
		if got := svcerr.Classify(err); got != svcerr.ClassUser {
			t.Errorf("%s: Classify = %v, want user", name, got)
		}
	}

	// ClassNone is "no opinion": the kind decides.
	noOpinion := &usageLimitErr{class: svcerr.ClassNone}
	if got := svcerr.Classify(noOpinion); got != svcerr.ClassServer {
		t.Errorf("no-opinion raw error: Classify = %v, want server", got)
	}
	if got := svcerr.Classify(fmt.Errorf("%w: %w", svcerr.ErrPlanLimit, noOpinion)); got != svcerr.ClassUser {
		t.Errorf("no-opinion plan limit: Classify = %v, want user", got)
	}

	// The nearest explicit class wins over a deeper one.
	if got := svcerr.Classify(svcerr.WithClass(fmt.Errorf("x: %w", declared), svcerr.ClassServer)); got != svcerr.ClassServer {
		t.Errorf("outer WithClass: Classify = %v, want server", got)
	}
}

func TestWithClass_Degenerate(t *testing.T) {
	t.Parallel()
	if svcerr.WithClass(nil, svcerr.ClassUser) != nil {
		t.Error("WithClass(nil) must be nil")
	}
	base := errors.New("x")
	if svcerr.WithClass(base, svcerr.ClassNone) != base {
		t.Error("WithClass(err, ClassNone) must return err unchanged")
	}
}

func TestClass_String(t *testing.T) {
	t.Parallel()
	for class, want := range map[svcerr.Class]string{
		svcerr.ClassNone:     "none",
		svcerr.ClassServer:   "server",
		svcerr.ClassUser:     "user",
		svcerr.ClassCanceled: "canceled",
	} {
		if got := class.String(); got != want {
			t.Errorf("%d.String() = %q, want %q", class, got, want)
		}
	}
}

type typedErr struct{}

func (*typedErr) Error() string { return "typed" }
