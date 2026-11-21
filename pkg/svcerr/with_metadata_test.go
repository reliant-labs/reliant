// with_metadata_test.go — structured facts beside the reason, on the wire.
//
// A reason says WHICH refusal happened; some refusals carry a datum the client
// needs to act on (the scopes a credential lacks). WithMetadata is how a
// service sends that datum without writing it into the message, so the
// properties pinned here are that it reaches the Connect error over a real
// client, that it never disturbs the code, message or reason, and that a
// WithCause cause's metadata stays server-side like the rest of the cause.
package svcerr_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

const scopesKey = "x-forge-error-scopes"

func TestWithMetadata_SetsMetadataBesideTheReason(t *testing.T) {
	t.Parallel()
	const msg = "your organization permissions do not include deploy:write"
	err := svcerr.WithMetadata(
		svcerr.WithReason(svcerr.PermissionDenied(msg), "scope_missing"),
		scopesKey, "deploy:write,secret:read")

	ce := svcerr.ToConnect(err)
	if ce.Code() != connect.CodePermissionDenied {
		t.Fatalf("code = %v, want PermissionDenied", ce.Code())
	}
	if ce.Message() != msg {
		t.Fatalf("message = %q, want %q", ce.Message(), msg)
	}
	if got := ce.Meta().Get(svcerr.ReasonHeader); got != "scope_missing" {
		t.Fatalf("reason = %q", got)
	}
	if got := ce.Meta().Get(scopesKey); got != "deploy:write,secret:read" {
		t.Fatalf("%s = %q", scopesKey, got)
	}
	if !errors.Is(err, svcerr.ErrPermissionDenied) {
		t.Fatal("errors.Is lost the sentinel through WithMetadata")
	}
}

func TestWithMetadata_ComposesInAnyOrder(t *testing.T) {
	t.Parallel()
	base := svcerr.FailedPrecondition("nope")
	orders := map[string]error{
		"metadata outside": svcerr.WithMetadata(svcerr.WithMessage(svcerr.WithReason(base, "r"), "shown"), "x-k", "v"),
		"metadata inside":  svcerr.WithReason(svcerr.WithMessage(svcerr.WithMetadata(base, "x-k", "v"), "shown"), "r"),
		"wrapped by fmt":   svcerr.WithReason(svcerr.WithMessage(wrapf(svcerr.WithMetadata(base, "x-k", "v")), "shown"), "r"),
	}
	for name, err := range orders {
		ce := svcerr.ToConnect(err)
		if ce.Code() != connect.CodeFailedPrecondition || ce.Message() != "shown" ||
			ce.Meta().Get(svcerr.ReasonHeader) != "r" || ce.Meta().Get("x-k") != "v" {
			t.Errorf("%s: code=%v message=%q reason=%q x-k=%q", name, ce.Code(), ce.Message(),
				ce.Meta().Get(svcerr.ReasonHeader), ce.Meta().Get("x-k"))
		}
	}
}

func TestWithMetadata_NearestWinsAndEmptyIsNoop(t *testing.T) {
	t.Parallel()
	err := svcerr.WithMetadata(svcerr.WithMetadata(svcerr.Internal("x"), "x-k", "inner"), "x-k", "outer")
	if got := svcerr.ToConnect(err).Meta().Get("x-k"); got != "outer" {
		t.Fatalf("nearest annotation should win, got %q", got)
	}
	base := svcerr.Internal("x")
	if svcerr.WithMetadata(base, "", "v") != base || svcerr.WithMetadata(base, "x-k", "") != base {
		t.Fatal("an empty key or value must return err unchanged")
	}
	if svcerr.WithMetadata(nil, "x-k", "v") != nil {
		t.Fatal("WithMetadata(nil) must be nil")
	}
}

func TestWithMetadata_PassedThroughConnectError(t *testing.T) {
	t.Parallel()
	ce := connect.NewError(connect.CodeUnavailable, errors.New("down"))
	out := svcerr.ToConnect(svcerr.WithMetadata(ce, "x-k", "v"))
	if out.Code() != connect.CodeUnavailable || out.Meta().Get("x-k") != "v" {
		t.Fatalf("code=%v x-k=%q", out.Code(), out.Meta().Get("x-k"))
	}
}

// A cause is for the log: its metadata is not the outer error's.
func TestWithMetadata_CauseMetadataStaysServerSide(t *testing.T) {
	t.Parallel()
	cause := svcerr.WithMetadata(svcerr.Internal("db"), "x-k", "leak")
	ce := svcerr.ToConnect(svcerr.WithCause(svcerr.Unavailable("try later"), cause))
	if got := ce.Meta().Get("x-k"); got != "" {
		t.Fatalf("a cause's metadata reached the client: %q", got)
	}
}

// Over a real Connect client: what a browser or a forge CLI actually reads.
func TestWithMetadata_ReachesARealClient(t *testing.T) {
	t.Parallel()
	const procedure = "/test.v1.Test/Do"
	mux := http.NewServeMux()
	mux.Handle(procedure, connect.NewUnaryHandler(procedure,
		func(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
			return nil, svcerr.Wrap(svcerr.WithMetadata(
				svcerr.WithReason(svcerr.PermissionDenied("missing deploy:write"), "scope_missing"),
				scopesKey, "deploy:write"))
		}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	client := connect.NewClient[emptypb.Empty, emptypb.Empty](srv.Client(), srv.URL+procedure)
	_, err := client.CallUnary(context.Background(), connect.NewRequest(&emptypb.Empty{}))
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("want a *connect.Error, got %v", err)
	}
	if ce.Code() != connect.CodePermissionDenied || ce.Message() != "missing deploy:write" {
		t.Fatalf("code=%v message=%q", ce.Code(), ce.Message())
	}
	if ce.Meta().Get(svcerr.ReasonHeader) != "scope_missing" || ce.Meta().Get(scopesKey) != "deploy:write" {
		t.Fatalf("reason=%q scopes=%q", ce.Meta().Get(svcerr.ReasonHeader), ce.Meta().Get(scopesKey))
	}
}

func wrapf(err error) error { return &wrapper{err} }

type wrapper struct{ err error }

func (w *wrapper) Error() string { return "ctx: " + w.err.Error() }
func (w *wrapper) Unwrap() error { return w.err }
