// Copyright (c) 2026 Reliant Labs
package worktreesweep

import (
	"context"
	"testing"

	"github.com/reliant-labs/reliant/internal/worktreereclaim"
	"github.com/stretchr/testify/require"
)

func TestGitRecoveryRolloutDefaultDoesNotSend(t *testing.T) {
	s := New(nil, nil).WithGitRecovery(nil, nil, false)
	require.False(t, s.recoverHeld(context.Background(), target{}, nil, worktreereclaim.Worktree{State: worktreereclaim.StateArchived}))
	// Missing dependencies can never turn an enable bit into a destructive path.
	s.WithGitRecovery(nil, nil, true)
	require.False(t, s.recoverHeld(context.Background(), target{}, nil, worktreereclaim.Worktree{State: worktreereclaim.StateArchived}))
}
