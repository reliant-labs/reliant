package testifyas

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
)

// errorAs has testify's require.ErrorAs signature: the target is interface{}.
func errorAs(t *testing.T, err error, target interface{}) {
	t.Helper()
	if !errors.As(err, target) {
		t.Fatalf("want %T in the chain of %v", target, err)
	}
}

func TestApprove(t *testing.T) {
	connectErr := new(connect.Error)
	errorAs(t, Approve(), &connectErr)
	if connectErr.Code() != connect.CodeFailedPrecondition {
		t.Fatalf("Approve: %v", connectErr.Code())
	}
}
