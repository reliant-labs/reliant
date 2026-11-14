// Copyright (c) 2025 Reliant Labs
package chatselection

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
)

// A version conflict is another tab or client writing first; the user reloads.
// It stays an Aborted on the wire with the same text, and is a user error, so
// RunTx's "Transaction failed" line is not an ERROR (prod a9117392).
func TestVersionConflictIsAUserErrorThatStaysAborted(t *testing.T) {
	t.Parallel()
	err := fmt.Errorf("update selection: %w", ErrVersionConflict)
	assert.Equal(t, svcerr.ClassUser, svcerr.Classify(err))
	assert.Equal(t, connect.CodeAborted, svcerr.Code(err))
	assert.True(t, errors.Is(err, ErrVersionConflict))
	assert.Contains(t, err.Error(), "the chat's selection changed since it was read; reload and try again")
}
