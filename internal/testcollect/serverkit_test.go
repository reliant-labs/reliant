package testcollect

import (
	"testing"

	"github.com/reliant-labs/forge/pkg/serverkit"
)

// The client speaks serverkit's protocol, the same path, without linking
// serverkit into the forge binary.
func TestProtocolMatchesServerkit(t *testing.T) {
	if CoveragePath != serverkit.CoveragePath {
		t.Errorf("CoveragePath %q, serverkit serves %q", CoveragePath, serverkit.CoveragePath)
	}
}
