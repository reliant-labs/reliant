package suiteas

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
)

// assertions has the shape of testify's require.New(t): ErrorAs is a method
// whose target is interface{}.
type assertions struct{ t *testing.T }

func (a assertions) ErrorAs(err error, target interface{}) {
	a.t.Helper()
	if !errors.As(err, target) {
		a.t.Fatalf("want %T in the chain of %v", target, err)
	}
}

func TestDeny(t *testing.T) {
	var connectErr *connect.Error
	assertions{t}.ErrorAs(Deny(), &connectErr)
	if connectErr.Code() != connect.CodeNotFound {
		t.Fatalf("Deny: %v", connectErr.Code())
	}
}
