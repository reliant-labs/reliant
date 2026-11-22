package handlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// Registration without a repository is useful for catalog-only workers, but
// executing approval activities must fail clearly instead of calling a nil store.
func TestApprovalActivitiesRequireCloser(t *testing.T) {
	t.Parallel()
	t.Run("resolve", func(t *testing.T) {
		out, err := NewApprovalResolveActivity(nil).Execute(context.Background(), ApprovalResolveInput{ApprovalID: "pending", Status: "timeout"})
		require.EqualError(t, err, "approval resolution store is not configured")
		require.False(t, out.Success)
	})
	t.Run("cleanup", func(t *testing.T) {
		out, err := NewCleanupActivity(nil, nil, nil).Execute(context.Background(), CleanupInput{ChatID: "chat"})
		require.EqualError(t, err, "approval resolution store is not configured")
		require.Zero(t, out.ApprovalsCancelled)
	})
}
