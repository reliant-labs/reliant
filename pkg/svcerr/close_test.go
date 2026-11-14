package svcerr_test

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// close_test.go — an expected close changes who must act, never the wire.

func TestExpectedClose_KeepsTheWireAndClassesCanceled(t *testing.T) {
	t.Parallel()
	inner := svcerr.Unavailable("read nothing for 90s; closing so the daemon redials")
	err := svcerr.ExpectedClose(inner, "peer_idle")

	if got := svcerr.Code(err); got != connect.CodeUnavailable {
		t.Errorf("Code = %v, want Unavailable: the peer redials on it", got)
	}
	if err.Error() != inner.Error() {
		t.Errorf("message = %q, want %q", err.Error(), inner.Error())
	}
	if !errors.Is(err, svcerr.ErrUnavailable) {
		t.Error("errors.Is(ErrUnavailable) lost through the marker")
	}
	if got := svcerr.ToConnect(err); got.Code() != connect.CodeUnavailable {
		t.Errorf("ToConnect code = %v, want Unavailable", got.Code())
	}
	if c := svcerr.Classify(err); c != svcerr.ClassCanceled {
		t.Errorf("Classify = %v, want canceled", c)
	}
	if reason, ok := svcerr.CloseReason(err); !ok || reason != "peer_idle" {
		t.Errorf("CloseReason = %q, %v; want peer_idle, true", reason, ok)
	}
	// Survives wrapping in process, and a connect boundary.
	wrapped := connect.NewError(connect.CodeUnavailable, fmt.Errorf("stream: %w", err))
	if reason, ok := svcerr.CloseReason(wrapped); !ok || reason != "peer_idle" {
		t.Errorf("CloseReason through wrapping = %q, %v; want peer_idle, true", reason, ok)
	}
}

func TestExpectedClose_OnlyWhatTheHandlerMarked(t *testing.T) {
	t.Parallel()
	for name, err := range map[string]error{
		"nil":                     nil,
		"bare unavailable":        svcerr.Unavailable("dependency refused"),
		"canceled by class alone": svcerr.WithClass(svcerr.Unavailable("x"), svcerr.ClassCanceled),
		"drain":                   svcerr.Draining("rollout"),
		"reclassified as a fault": svcerr.WithClass(svcerr.ExpectedClose(svcerr.Unavailable("x"), "peer_idle"), svcerr.ClassServer),
		"marker on a cause":       svcerr.WithCause(svcerr.Internal("failed"), svcerr.ExpectedClose(errors.New("x"), "peer_idle")),
	} {
		if svcerr.IsExpectedClose(err) {
			t.Errorf("%s: IsExpectedClose = true, want false", name)
		}
	}
	if svcerr.ExpectedClose(nil, "peer_idle") != nil {
		t.Error("ExpectedClose(nil) != nil")
	}
	if reason, _ := svcerr.CloseReason(svcerr.ExpectedClose(errors.New("x"), "")); reason != svcerr.CloseReasonExpected {
		t.Errorf("empty reason recorded as %q, want %q", reason, svcerr.CloseReasonExpected)
	}
}
