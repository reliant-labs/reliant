package machinewake

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/wirecheck"
)

func TestWakingConnectErrorKeepsItsDetail(t *testing.T) {
	ce := wirecheck.RoundTrip(t, (&wakingError{daemonID: "d-1"}).connectError())
	if ce.Code() != connect.CodeUnavailable {
		t.Fatalf("code = %v, want Unavailable", ce.Code())
	}
	if len(ce.Details()) != 1 {
		t.Fatalf("details = %d, want the DaemonWaking detail", len(ce.Details()))
	}
	if got := ce.Meta().Get("x-forge-error-reason"); got != errreason.DaemonWaking {
		t.Fatalf("reason = %q, want %q", got, errreason.DaemonWaking)
	}
}
