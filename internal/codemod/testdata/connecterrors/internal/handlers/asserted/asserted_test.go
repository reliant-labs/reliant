package asserted

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
)

func codeOf(t *testing.T, err error) connect.Code {
	t.Helper()
	var ce *connect.Error
	if !errors.As(err, &ce) {
		t.Fatalf("want a connect error, got %v", err)
	}
	return ce.Code()
}

func TestRefuse(t *testing.T) {
	if got := codeOf(t, Refuse()); got != connect.CodePermissionDenied {
		t.Fatalf("Refuse: %v", got)
	}
}

// A second reader in the same package's tests: the report names both
// (reliant's worktree tests had two, and only one was named).
func TestRefuseIsConnect(t *testing.T) {
	if _, ok := Refuse().(*connect.Error); !ok {
		t.Fatal("Refuse: not a *connect.Error")
	}
}
