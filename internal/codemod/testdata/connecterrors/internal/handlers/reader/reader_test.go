package reader

import (
	"testing"

	"connectrpc.com/connect"

	"example.com/cescratch/internal/handlers/testonly"
)

// reader's tests import testonly; reader's code does not. testonly gets bare
// kinds, so this connect.CodeOf becomes svcerr.Code.
func TestTestonlyCode(t *testing.T) {
	if got := connect.CodeOf(testonly.Missing()); got != connect.CodeNotFound {
		t.Fatalf("Missing: %v", got)
	}
}
