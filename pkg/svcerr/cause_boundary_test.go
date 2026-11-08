package svcerr_test

// cause_boundary_test.go — a WithCause cause is the OPERATOR's, never the
// client's.
//
// WithCause promises the cause "NEVER contributes to Error()". It used to
// keep that promise for the text of the outer error and break it everywhere
// else: the cause sat on the same Unwrap chain as the outer error, so every
// client-facing decision that walked the chain with errors.As / errors.Is
// could land on it. Measured, with a downstream Connect error as the cause:
//
//	svcerr.ToConnect(svcerr.WithCause(svcerr.Internal("charge failed"), downstream))
//	→ not_found "billing row 42 missing on shard db-7.internal"
//
// The downstream service's code AND message — an internal hostname — went to
// the client verbatim, and the "charge failed" the handler chose was dropped.
// An svcerr kind as the cause leaked its code; a bare outer sentinel leaked
// the cause's detail as the message; a WithReason on the cause leaked its
// reason; a WithClass on the cause silenced a server fault in the logs.
//
// These tests pin the boundary: the client's code, message, reason header and
// Classify all come from the outer error, and the cause stays reachable for
// the server through Cause, errors.Is and errors.As.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

const downstreamSecret = "billing row 42 missing on shard db-7.internal"

// downstreamNotFound is what a Connect client returns when the service it
// called answered not_found — including the reason header that service set.
func downstreamNotFound() *connect.Error {
	ce := connect.NewError(connect.CodeNotFound, errors.New(downstreamSecret))
	ce.Meta().Set(svcerr.ReasonHeader, "row_missing")
	return ce
}

// TestWithCause_DownstreamConnectErrorDoesNotReachTheClient is the probe that
// found the defect, verbatim.
func TestWithCause_DownstreamConnectErrorDoesNotReachTheClient(t *testing.T) {
	t.Parallel()
	downstream := downstreamNotFound()
	err := svcerr.WithCause(svcerr.Internal("charge failed"), downstream)

	ce := svcerr.ToConnect(err)
	if ce.Code() != connect.CodeInternal || ce.Message() != "charge failed" {
		t.Errorf("client reads %v %q, want internal %q — the outer error classifies, the cause is for logs",
			ce.Code(), ce.Message(), "charge failed")
	}
	if strings.Contains(ce.Error(), "db-7") {
		t.Errorf("the downstream message reached the wire: %q", ce.Error())
	}
	if got := ce.Meta().Get(svcerr.ReasonHeader); got != "" {
		t.Errorf("reason header = %q, want none — the downstream service's reason is not ours to publish", got)
	}
	if got := svcerr.Code(err); got != connect.CodeInternal {
		t.Errorf("svcerr.Code = %v, want internal", got)
	}
	if got := svcerr.Classify(err); got != svcerr.ClassServer {
		t.Errorf("Classify = %v, want server — a not_found from a dependency is our fault here", got)
	}
	// Mapping must not have stamped anything onto the dependency's error.
	if got := downstream.Meta().Get(svcerr.ReasonHeader); got != "row_missing" {
		t.Errorf("the downstream error's own metadata was rewritten to %q", got)
	}

	// The operator keeps all of it.
	if got := svcerr.Cause(ce); got != downstream {
		t.Errorf("Cause = %v, want the downstream error", got)
	}
	if !errors.Is(ce, downstream) {
		t.Error("errors.Is no longer reaches the cause server-side")
	}
	if !errors.Is(ce, svcerr.ErrInternal) {
		t.Error("errors.Is lost the outer sentinel")
	}
}

// TestWithCause_OuterClassifies covers every pairing the boundary must hold
// for: what the client reads (code, message, reason header) and who
// observability says must act (Classify) all come from the outer error,
// whatever the cause is.
func TestWithCause_OuterClassifies(t *testing.T) {
	t.Parallel()
	raw := &pgShapedError{sqlstate: "42P01", detail: `relation "charges" does not exist ` + plantedSecret}

	cases := []struct {
		name       string
		err        func() error
		wantCode   connect.Code
		wantMsg    string
		wantReason string
		wantClass  svcerr.Class
	}{
		{
			name:      "inner connect error under an outer server error",
			err:       func() error { return svcerr.WithCause(svcerr.Internal("charge failed"), downstreamNotFound()) },
			wantCode:  connect.CodeInternal,
			wantMsg:   "charge failed",
			wantClass: svcerr.ClassServer,
		},
		{
			name: "inner connect error, outer reason",
			err: func() error {
				return svcerr.WithReason(svcerr.WithCause(svcerr.Internal("charge failed"), downstreamNotFound()), "charge_failed")
			},
			wantCode:   connect.CodeInternal,
			wantMsg:    "charge failed",
			wantReason: "charge_failed",
			wantClass:  svcerr.ClassServer,
		},
		{
			name: "inner svcerr kind under an outer server error",
			err: func() error {
				return svcerr.WithCause(svcerr.Internal("charge failed"), svcerr.NotFound(downstreamSecret))
			},
			wantCode:  connect.CodeInternal,
			wantMsg:   "charge failed",
			wantClass: svcerr.ClassServer,
		},
		{
			name: "inner svcerr kind carrying its own copy and reason",
			err: func() error {
				inner := svcerr.WithReason(svcerr.WithMessage(svcerr.NotFound("row"), downstreamSecret), "row_missing")
				return svcerr.WithCause(svcerr.Internal("charge failed"), inner)
			},
			wantCode:  connect.CodeInternal,
			wantMsg:   "charge failed",
			wantClass: svcerr.ClassServer,
		},
		{
			name: "bare outer sentinel over an inner detail",
			// The outer has no detail of its own, so the message is the
			// sentinel's text — never the nearest detail found in the cause.
			err:       func() error { return svcerr.WithCause(svcerr.ErrInternal, svcerr.NotFound(downstreamSecret)) },
			wantCode:  connect.CodeInternal,
			wantMsg:   svcerr.ErrInternal.Error(),
			wantClass: svcerr.ClassServer,
		},
		{
			name: "inner user-class error under an outer server error",
			err: func() error {
				return svcerr.WithCause(svcerr.Internal("charge failed"),
					svcerr.WithClass(errors.New("provider declined: "+downstreamSecret), svcerr.ClassUser))
			},
			wantCode:  connect.CodeInternal,
			wantMsg:   "charge failed",
			wantClass: svcerr.ClassServer,
		},
		{
			name: "inner typed error declaring user class under an outer server error",
			err: func() error {
				return svcerr.WithCause(svcerr.Internal("charge failed"), &usageLimitErr{class: svcerr.ClassUser})
			},
			wantCode:  connect.CodeInternal,
			wantMsg:   "charge failed",
			wantClass: svcerr.ClassServer,
		},
		{
			name: "inner user kind under an unrecognised outer (the panic-recovery shape)",
			err: func() error {
				return svcerr.WithCause(errors.New(svcerr.InternalMessage), svcerr.InvalidArgument(downstreamSecret))
			},
			wantCode:  connect.CodeInternal,
			wantMsg:   svcerr.InternalMessage,
			wantClass: svcerr.ClassServer,
		},
		{
			name:      "outer user error with an inner server cause",
			err:       func() error { return svcerr.WithCause(svcerr.NotFound("coupon"), raw) },
			wantCode:  connect.CodeNotFound,
			wantMsg:   "coupon not found",
			wantClass: svcerr.ClassUser,
		},
		{
			name: "outer user error with an inner connect server error",
			err: func() error {
				return svcerr.WithCause(svcerr.InvalidArgument("invalid scopes"),
					connect.NewError(connect.CodeInternal, errors.New("scope service on "+downstreamSecret)))
			},
			wantCode:  connect.CodeInvalidArgument,
			wantMsg:   "invalid scopes",
			wantClass: svcerr.ClassUser,
		},
		{
			name: "outer reason wins, inner reason withheld",
			err: func() error {
				return svcerr.WithCause(
					svcerr.WithReason(svcerr.FailedPrecondition("no active plan"), "no_plan"),
					svcerr.WithReason(svcerr.Internal(downstreamSecret), "ledger_down"))
			},
			wantCode:   connect.CodeFailedPrecondition,
			wantMsg:    "no active plan",
			wantReason: "no_plan",
			wantClass:  svcerr.ClassUser,
		},
		{
			name: "wrapped on the way out",
			err: func() error {
				return fmt.Errorf("charge %d: %w", 42, svcerr.WithCause(svcerr.Internal("charge failed"), downstreamNotFound()))
			},
			wantCode:  connect.CodeInternal,
			wantMsg:   "charge failed",
			wantClass: svcerr.ClassServer,
		},
		{
			name: "nested causes",
			err: func() error {
				return svcerr.WithCause(svcerr.WithCause(svcerr.Unavailable("billing is unavailable"), downstreamNotFound()),
					svcerr.WithClass(svcerr.NotFound(downstreamSecret), svcerr.ClassUser))
			},
			wantCode:  connect.CodeUnavailable,
			wantMsg:   "billing is unavailable",
			wantClass: svcerr.ClassServer,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.err()

			ce := svcerr.ToConnect(err)
			if ce.Code() != tc.wantCode || ce.Message() != tc.wantMsg {
				t.Errorf("client reads %v %q, want %v %q", ce.Code(), ce.Message(), tc.wantCode, tc.wantMsg)
			}
			for _, secret := range []string{"db-7", plantedSecret, "SQLSTATE"} {
				if strings.Contains(ce.Error(), secret) {
					t.Errorf("the cause reached the wire: %q", ce.Error())
				}
			}
			if got := ce.Meta().Get(svcerr.ReasonHeader); got != tc.wantReason {
				t.Errorf("reason header = %q, want %q", got, tc.wantReason)
			}
			if got := svcerr.Code(err); got != tc.wantCode {
				t.Errorf("svcerr.Code = %v, want %v — it must agree with what the client was sent", got, tc.wantCode)
			}
			if got := svcerr.Classify(err); got != tc.wantClass {
				t.Errorf("Classify = %v, want %v", got, tc.wantClass)
			}
			if got := svcerr.Classify(ce); got != tc.wantClass {
				t.Errorf("Classify(mapped) = %v, want %v — the interceptor classifies the value the handler returned", got, tc.wantClass)
			}
			if svcerr.Cause(ce) == nil {
				t.Error("the cause is no longer reachable for the log")
			}
		})
	}
}

// TestWithCause_ServerSideIdentitySurvives pins the other half of the
// contract: the cause is withheld from the CLIENT, not from the server.
// errors.Is and errors.As still reach it, so `var pgErr *pgconn.PgError`
// keeps matching — with one deliberate exception, *connect.Error, which is
// how connect-go itself finds the verdict it publishes.
func TestWithCause_ServerSideIdentitySurvives(t *testing.T) {
	t.Parallel()
	appSentinel := errors.New("ledger: row missing")
	raw := &pgShapedError{sqlstate: "42P01", detail: "relation does not exist"}
	err := svcerr.WithCause(svcerr.Internal("charge failed"), fmt.Errorf("%w: %w", appSentinel, raw))

	if !errors.Is(err, appSentinel) {
		t.Error("errors.Is no longer reaches a sentinel in the cause")
	}
	var typed *pgShapedError
	if !errors.As(err, &typed) || typed != raw {
		t.Error("errors.As no longer reaches a driver error in the cause")
	}
	if !errors.Is(svcerr.ToConnect(err), appSentinel) {
		t.Error("the cause is unreachable through the mapped connect error")
	}

	// A kind in the cause is still visible to errors.Is and the Is*
	// predicates — they ask what the error CARRIES. Code is what decides the
	// wire, and it does not see it.
	withKind := svcerr.WithCause(svcerr.Internal("charge failed"), svcerr.NotFound("row"))
	if !svcerr.IsNotFound(withKind) {
		t.Error("IsNotFound no longer reaches a kind in the cause")
	}
	if svcerr.Code(withKind) != connect.CodeInternal {
		t.Errorf("Code = %v, want internal", svcerr.Code(withKind))
	}

	// The exception: no *connect.Error behind a cause. connect-go's handler
	// maps a returned error with errors.As(err, &connectErr), so one that
	// could be found there would be published even without svcerr.Wrap.
	var ce *connect.Error
	if errors.As(svcerr.WithCause(svcerr.Internal("charge failed"), downstreamNotFound()), &ce) {
		t.Errorf("errors.As found the cause's *connect.Error (%v); connect-go would publish it", ce)
	}
	if got := connect.CodeOf(svcerr.WithCause(svcerr.Internal("charge failed"), downstreamNotFound())); got == connect.CodeNotFound {
		t.Error("connect.CodeOf read the cause's code")
	}
}

// TestWithCause_IsClassifiedReadsTheOuterError: a classified cause does not
// make an unclassified error classified. pkg/crud passes a classified error
// through verbatim on exactly this predicate, so a true here would forward
// whatever the cause says.
func TestWithCause_IsClassifiedReadsTheOuterError(t *testing.T) {
	t.Parallel()
	if svcerr.IsClassified(svcerr.WithCause(errors.New("raw driver failure"), svcerr.NotFound("row"))) {
		t.Error("IsClassified = true for an unclassified outer error with a classified cause")
	}
	if svcerr.IsClassified(svcerr.WithCause(errors.New("raw driver failure"), downstreamNotFound())) {
		t.Error("IsClassified = true for an unclassified outer error with a connect cause")
	}
	if !svcerr.IsClassified(svcerr.WithCause(svcerr.Internal("charge failed"), errors.New("raw"))) {
		t.Error("IsClassified = false for a classified outer error")
	}
}

// TestWithCause_CanceledCauseIsQuiet is the one place the cause informs
// Classify, deliberately. A request whose context was canceled failed because
// the caller went away, however the call site labelled it — and handlers do
// label it: `svcerr.WithCause(svcerr.Internal("list failed"), err)` receives a
// context.Canceled every time a browser navigates away mid-query. Paging on
// that is the noise Classify exists to remove. The WIRE still reads the
// outer error.
func TestWithCause_CanceledCauseIsQuiet(t *testing.T) {
	t.Parallel()
	err := svcerr.WithCause(svcerr.Internal("list items failed"), fmt.Errorf("query: %w", context.Canceled))

	ce := svcerr.ToConnect(err)
	if ce.Code() != connect.CodeInternal || ce.Message() != "list items failed" {
		t.Errorf("client reads %v %q, want internal %q", ce.Code(), ce.Message(), "list items failed")
	}
	if got := svcerr.Classify(err); got != svcerr.ClassCanceled {
		t.Errorf("Classify = %v, want canceled", got)
	}
	// An explicit class on the outer error still wins over it.
	if got := svcerr.Classify(svcerr.WithClass(err, svcerr.ClassServer)); got != svcerr.ClassServer {
		t.Errorf("outer WithClass: Classify = %v, want server", got)
	}
}

// TestWithCause_WireRoundTrip checks what a real Connect client reads, for
// both ways a handler can hand the error back. Returning it without
// svcerr.Wrap is a convention violation, but it must not be a leak: connect-go
// maps an unwrapped error with errors.As, and used to find the cause's
// *connect.Error that way.
func TestWithCause_WireRoundTrip(t *testing.T) {
	t.Parallel()
	const procedure = "/svcerr.test.v1.BillingService/Charge"
	probe := func() error { return svcerr.WithCause(svcerr.Internal("charge failed"), downstreamNotFound()) }

	cases := []struct {
		name       string
		handlerErr func() error
		wantCode   connect.Code
		wantReason string
	}{
		{"through svcerr.Wrap", func() error { return svcerr.Wrap(svcerr.WithReason(probe(), "charge_failed")) }, connect.CodeInternal, "charge_failed"},
		{"returned without Wrap", probe, connect.CodeUnknown, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mux := http.NewServeMux()
			mux.Handle(procedure, connect.NewUnaryHandler(procedure,
				func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
					return nil, tc.handlerErr()
				}))
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)

			client := connect.NewClient[emptypb.Empty, emptypb.Empty](srv.Client(), srv.URL+procedure)
			_, err := client.CallUnary(context.Background(), connect.NewRequest(&emptypb.Empty{}))

			var ce *connect.Error
			if !errors.As(err, &ce) {
				t.Fatalf("want a *connect.Error from the client, got %v", err)
			}
			if ce.Code() != tc.wantCode || ce.Message() != "charge failed" {
				t.Errorf("client received %v %q, want %v %q", ce.Code(), ce.Message(), tc.wantCode, "charge failed")
			}
			if strings.Contains(ce.Error(), "db-7") {
				t.Errorf("the downstream message crossed the wire: %q", ce.Error())
			}
			if got := ce.Meta().Get(svcerr.ReasonHeader); got != tc.wantReason {
				t.Errorf("reason header = %q, want %q", got, tc.wantReason)
			}
		})
	}
}
