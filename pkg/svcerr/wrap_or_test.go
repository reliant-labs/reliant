package svcerr_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// TestWrapOr_ClassifiedPassesThrough: an error that already knows its code
// keeps it, and its message, whatever the fallback says. This is the half
// of WrapOr that fixes a handler which mapped every failure to one guessed
// code: a store's NotFound, a deadline and a cancellation are verdicts.
func TestWrapOr_ClassifiedPassesThrough(t *testing.T) {
	fallback := svcerr.Internal("failed to list projects")
	cases := []struct {
		name     string
		err      error
		wantCode connect.Code
		wantMsg  string
	}{
		{"constructor", svcerr.NotFound("chat"), connect.CodeNotFound, "chat not found"},
		{"bare sentinel", svcerr.ErrPermissionDenied, connect.CodePermissionDenied, "permission denied"},
		{"wrapped constructor", fmt.Errorf("get chat c-1: %w", svcerr.NotFound("chat")), connect.CodeNotFound, "chat not found"},
		{"with message", svcerr.WithMessage(svcerr.ErrNotFound, "project not found or access denied"), connect.CodeNotFound, "project not found or access denied"},
		{"plan limit", svcerr.PlanLimit("seat cap"), connect.CodeResourceExhausted, "seat cap"},
		{"draining", svcerr.Draining("gateway draining"), connect.CodeUnavailable, "gateway draining"},
		{"context deadline", fmt.Errorf("list projects: %w", context.DeadlineExceeded), connect.CodeDeadlineExceeded, "deadline exceeded"},
		{"context canceled", context.Canceled, connect.CodeCanceled, "canceled"},
		{"connect error", connect.NewError(connect.CodeAborted, errors.New("version conflict")), connect.CodeAborted, "version conflict"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := svcerr.WrapOr(tc.err, fallback)
			var ce *connect.Error
			if !errors.As(got, &ce) {
				t.Fatalf("WrapOr returned %T, want *connect.Error", got)
			}
			if ce.Code() != tc.wantCode || ce.Message() != tc.wantMsg {
				t.Fatalf("got %v %q, want %v %q", ce.Code(), ce.Message(), tc.wantCode, tc.wantMsg)
			}
			if errors.Is(got, fallback) {
				t.Fatalf("a classified error must not carry the fallback: %v", got)
			}
			if want := svcerr.Wrap(tc.err); svcerr.Code(got) != svcerr.Code(want) || got.Error() != want.Error() {
				t.Fatalf("WrapOr(classified) = %v, want Wrap's %v", got, want)
			}
		})
	}
}

// TestWrapOr_ConnectErrorIsTheSameValue: a *connect.Error is passed through
// verbatim (WrapOr is Wrap for it), so mid-migration helpers that still
// build one compose unchanged.
func TestWrapOr_ConnectErrorIsTheSameValue(t *testing.T) {
	ce := connect.NewError(connect.CodeFailedPrecondition, errors.New("chat has not started"))
	if got := svcerr.WrapOr(ce, svcerr.Internal("x")); got != error(ce) {
		t.Fatalf("WrapOr(*connect.Error) = %v, want the same value", got)
	}
}

// TestWrapOr_UnclassifiedReadsTheFallback: a driver error is never on the
// wire. The client reads the fallback's code and message; the original is
// the server-side cause, found by Cause, errors.Is and errors.As.
func TestWrapOr_UnclassifiedReadsTheFallback(t *testing.T) {
	driver := errors.New(`ERROR: relation "projects" does not exist dsn=postgres://u:secret@db/x`)
	got := svcerr.WrapOr(fmt.Errorf("list projects: %w", driver), svcerr.Internal("failed to list projects"))

	var ce *connect.Error
	if !errors.As(got, &ce) {
		t.Fatalf("WrapOr returned %T, want *connect.Error", got)
	}
	if ce.Code() != connect.CodeInternal || ce.Message() != "failed to list projects" {
		t.Fatalf("client reads %v %q, want internal %q", ce.Code(), ce.Message(), "failed to list projects")
	}
	if strings.Contains(ce.Error(), "secret") || strings.Contains(ce.Message(), "relation") {
		t.Fatalf("driver text reached the client: %q", ce.Error())
	}
	if cause := svcerr.Cause(got); cause == nil || !errors.Is(cause, driver) {
		t.Fatalf("Cause = %v, want the original error", cause)
	}
	if !errors.Is(got, driver) {
		t.Fatal("errors.Is must reach the original error server-side")
	}
	if svcerr.Code(got) != connect.CodeInternal {
		t.Fatalf("svcerr.Code = %v, want internal", svcerr.Code(got))
	}
}

// TestWrapOr_FallbackDecidesEverythingTheClientReads: the fallback's code,
// reason and class are the result's — WrapOr is the place a handler names
// what this rpc tells its caller, not just a message.
func TestWrapOr_FallbackDecidesEverythingTheClientReads(t *testing.T) {
	fallback := svcerr.WithClass(
		svcerr.WithReason(svcerr.Unavailable("your machine is not connected"), "daemon_offline"),
		svcerr.ClassUser)
	got := svcerr.WrapOr(errors.New("nats: no responders"), fallback)

	var ce *connect.Error
	if !errors.As(got, &ce) {
		t.Fatalf("WrapOr returned %T, want *connect.Error", got)
	}
	if ce.Code() != connect.CodeUnavailable || ce.Message() != "your machine is not connected" {
		t.Fatalf("client reads %v %q", ce.Code(), ce.Message())
	}
	if r := ce.Meta().Get(svcerr.ReasonHeader); r != "daemon_offline" {
		t.Fatalf("reason header = %q, want daemon_offline", r)
	}
	if c := svcerr.Classify(got); c != svcerr.ClassUser {
		t.Fatalf("Classify = %v, want the fallback's ClassUser", c)
	}
}

// TestWrapOr_Degenerate covers the inputs that have one sensible answer.
func TestWrapOr_Degenerate(t *testing.T) {
	if got := svcerr.WrapOr(nil, svcerr.Internal("x")); got != nil {
		t.Fatalf("WrapOr(nil) = %v, want nil", got)
	}
	// A nil fallback is ErrInternal: the client reads the bare category.
	got := svcerr.WrapOr(errors.New("raw"), nil)
	var ce *connect.Error
	if !errors.As(got, &ce) || ce.Code() != connect.CodeInternal || ce.Message() != svcerr.ErrInternal.Error() {
		t.Fatalf("nil fallback: got %v", got)
	}
	// An unclassified fallback is redacted like any unrecognised error.
	got = svcerr.WrapOr(errors.New("raw"), errors.New("not a kind"))
	if !errors.As(got, &ce) || ce.Code() != connect.CodeInternal || ce.Message() != svcerr.InternalMessage {
		t.Fatalf("unclassified fallback: got %v", got)
	}
}
