package cli

import (
	"strings"
	"testing"

	"github.com/reliant-labs/forge/internal/cloud"
)

// A machine credential is refused by the control plane whatever it holds, so
// the error says to run it as a person, never "ask an owner to grant obs:write".
func TestIngestTokenErrNamesTheMachineCredential(t *testing.T) {
	err := ingestTokenErr(&cloud.Error{Code: cloud.CodePermissionDenied, HTTPStatus: 403,
		Message: "telemetry ingest tokens are minted, rotated and revoked by a person, not a machine credential"})
	msg := err.Error()
	if !strings.Contains(msg, "forge login") || !strings.Contains(msg, "machine credential") || strings.Contains(msg, "ask an owner to grant") {
		t.Fatalf("machine-credential refusal = %q", msg)
	}
	// A person without the grant still gets the grant advice.
	if msg := ingestTokenErr(&cloud.Error{Code: cloud.CodePermissionDenied, Message: "obs:write is required"}).Error(); !strings.Contains(msg, "obs:write") {
		t.Fatalf("person refusal = %q", msg)
	}
}
