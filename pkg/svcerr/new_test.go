package svcerr_test

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

func TestNewRuntimeCode(t *testing.T) {
	t.Parallel()
	sentinels := []error{svcerr.ErrCanceled, svcerr.ErrUnknown, svcerr.ErrInvalidArgument, svcerr.ErrDeadlineExceeded, svcerr.ErrNotFound, svcerr.ErrAlreadyExists, svcerr.ErrPermissionDenied, svcerr.ErrResourceExhausted, svcerr.ErrFailedPrecondition, svcerr.ErrAborted, svcerr.ErrOutOfRange, svcerr.ErrUnimplemented, svcerr.ErrInternal, svcerr.ErrUnavailable, svcerr.ErrDataLoss, svcerr.ErrUnauthenticated}
	for i, sentinel := range sentinels {
		code := connect.Code(i + 1)
		t.Run(code.String(), func(t *testing.T) {
			t.Parallel()
			cause := errors.New("postgres://secret@internal")
			got := svcerr.ToConnect(svcerr.WithReason(svcerr.WithCause(svcerr.New(code, "public copy"), cause), "test_reason"))
			if got.Code() != code || got.Message() != "public copy" {
				t.Fatalf("wire = %v %q", got.Code(), got.Message())
			}
			if !errors.Is(got, sentinel) || !errors.Is(svcerr.Cause(got), cause) {
				t.Fatal("lost sentinel or cause")
			}
			if got.Meta().Get(svcerr.ReasonHeader) != "test_reason" {
				t.Fatal("lost reason")
			}
			if svcerr.Classify(got) != svcerr.Classify(sentinel) {
				t.Fatal("wrong class")
			}
			empty := svcerr.ToConnect(svcerr.New(code, ""))
			if empty.Code() != code || empty.Message() != sentinel.Error() {
				t.Fatalf("empty = %v %q", empty.Code(), empty.Message())
			}
		})
	}
	for _, code := range []connect.Code{0, 99} {
		got := svcerr.ToConnect(svcerr.New(code, "invalid code"))
		if got.Code() != connect.CodeUnknown || got.Message() != "invalid code" {
			t.Fatalf("invalid = %v", got)
		}
	}
}
