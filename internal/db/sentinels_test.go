// Copyright (c) 2025 Reliant Labs
package db

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// The Repository's own sentinels are svcerr kinds with their text and
// identity unchanged, like internal/db/core's.
func TestRepositorySentinelsAreClassified(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code connect.Code
		text string
	}{
		{ErrDaemonOwnedByAnotherUser, connect.CodePermissionDenied, "daemon is owned by another user"},
		{ErrWorkflowSlugTaken, connect.CodeAlreadyExists, "workflow slug already taken"},
	} {
		wrapped := fmt.Errorf("%w: daemon d-1", tc.err)
		if !errors.Is(wrapped, tc.err) {
			t.Errorf("%q: errors.Is lost the sentinel", tc.text)
		}
		if tc.err.Error() != tc.text {
			t.Errorf("Error() = %q, want %q", tc.err.Error(), tc.text)
		}
		wire := svcerr.ToConnect(wrapped)
		if wire.Code() != tc.code || wire.Message() != tc.text {
			t.Errorf("wire = %v %q, want %v %q", wire.Code(), wire.Message(), tc.code, tc.text)
		}
	}
}
