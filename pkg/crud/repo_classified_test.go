package crud

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// Persist, Fetch and the other repository closures are override seams too
// (db/crud-overrides: "Return svcerr sentinels ... from any op closure —
// pkg/crud reads the classification and preserves your code and message").
// mapRepoErr honoured that for NotFound and Aborted only. Every other
// classified repository error fell through to
// svcerr.WithCause(svcerr.Internal("<op> <entity> failed"), err), and its code
// reached the client only because svcerr used to classify a WithCause by its
// CAUSE — the leak that also published a downstream service's not_found
// verbatim. The message never survived: the client read "create user failed"
// with an application's PermissionDenied code.
//
// With the cause withheld from classification, that accident would have
// become CodeInternal for every such error — a caller's mistake reported as a
// server fault, and every client disconnect mid-query paging as one. These
// pin the explicit pass-through that replaces it.
func TestRepoSeam_PreservesClassifiedErrors(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode connect.Code
		wantMsg  string
	}{
		{"permission denied", svcerr.PermissionDenied("not yours"), connect.CodePermissionDenied, "not yours"},
		{"invalid argument", svcerr.InvalidArgument("customer_id is not one of yours"), connect.CodeInvalidArgument, "customer_id is not one of yours"},
		{"failed precondition", svcerr.FailedPrecondition("the order is closed"), connect.CodeFailedPrecondition, "the order is closed"},
		{"wrapped by the repo", fmt.Errorf("persist user: %w", svcerr.PermissionDenied("not yours")), connect.CodePermissionDenied, "not yours"},
		{"client went away", fmt.Errorf("insert users: %w", context.Canceled), connect.CodeCanceled, svcerr.ErrCanceled.Error()},
		{"deadline", fmt.Errorf("insert users: %w", context.DeadlineExceeded), connect.CodeDeadlineExceeded, svcerr.ErrDeadlineExceeded.Error()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := HandleCreate(CreateOp[createReq, createResp, *user]{
				EntityLower: "user",
				Entity:      func(context.Context, *createReq) (*user, error) { return &user{}, nil },
				Persist:     func(context.Context, *user) error { return tc.err },
				Pack:        func(*user) (*createResp, error) { return &createResp{}, nil },
			})
			_, err := h(context.Background(), connect.NewRequest(&createReq{}))

			cerr := new(connect.Error)
			if !errors.As(err, &cerr) {
				t.Fatalf("want *connect.Error, got %T", err)
			}
			if cerr.Code() != tc.wantCode || cerr.Message() != tc.wantMsg {
				t.Errorf("Persist returned %v: client reads %v %q, want %v %q",
					tc.err, cerr.Code(), cerr.Message(), tc.wantCode, tc.wantMsg)
			}
			if cerr.Meta().Get(svcerr.ReasonHeader) == "" {
				t.Error("no reason header — the vocabulary must stay total")
			}
		})
	}
}

// A dependency's *connect.Error reaching a repository seam is a classified
// verdict too — but only when it is the error itself, not a WithCause cause.
// The application that wrapped it chose the outer classification.
func TestRepoSeam_CauseOfAnUnclassifiedErrorStaysServerSide(t *testing.T) {
	downstream := connect.NewError(connect.CodeNotFound, errors.New("ledger row 42 on db-7.internal"))
	h := HandleCreate(CreateOp[createReq, createResp, *user]{
		EntityLower: "user",
		Entity:      func(context.Context, *createReq) (*user, error) { return &user{}, nil },
		Persist: func(context.Context, *user) error {
			return svcerr.WithCause(errors.New("ledger write failed"), downstream)
		},
		Pack: func(*user) (*createResp, error) { return &createResp{}, nil },
	})
	_, err := h(context.Background(), connect.NewRequest(&createReq{}))

	if got := connect.CodeOf(err); got != connect.CodeInternal {
		t.Errorf("code = %v, want Internal", got)
	}
	cerr := new(connect.Error)
	if errors.As(err, &cerr) && contains(cerr.Message(), "db-7") {
		t.Errorf("the dependency's message crossed the wire: %q", cerr.Message())
	}
}
