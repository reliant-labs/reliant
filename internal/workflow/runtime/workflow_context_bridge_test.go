// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWorkflowContextToTyped(t *testing.T) {
	t.Run("maps all known fields", func(t *testing.T) {
		ctx := map[string]interface{}{
			workflowContextKeyID:           "wf-123",
			workflowContextKeyName:         "test-workflow",
			workflowContextKeyPath:         "/tmp/worktree",
			workflowContextKeyBranch:       "feature/test",
			workflowContextKeyMode:         "auto",
			workflowContextKeyRunID:        "run-456",
			workflowContextKeySessionID:    "session-789",
			workflowContextKeyWorktreePath: "/tmp/worktree",
			workflowContextKeyChatID:       "chat-abc", // intentionally ignored by typed model
			workflowContextKeyInputs: map[string]interface{}{
				"thread": "main",
			},
		}

		typed := workflowContextToTyped(ctx)

		assert.Equal(t, "wf-123", typed.ID)
		assert.Equal(t, "test-workflow", typed.Name)
		assert.Equal(t, "/tmp/worktree", typed.Path)
		assert.Equal(t, "feature/test", typed.Branch)
		assert.Equal(t, "auto", typed.Mode)
		assert.Equal(t, "run-456", typed.RunID)
		assert.Equal(t, "session-789", typed.SessionID)
		assert.Equal(t, "/tmp/worktree", typed.WorktreePath)
	})

	t.Run("nil map returns empty typed context", func(t *testing.T) {
		typed := workflowContextToTyped(nil)
		assert.NotNil(t, typed)
		assert.Equal(t, "", typed.ID)
		assert.Equal(t, "", typed.Name)
		assert.Equal(t, "", typed.Path)
		assert.Equal(t, "", typed.Branch)
		assert.Equal(t, "", typed.Mode)
		assert.Equal(t, "", typed.RunID)
		assert.Equal(t, "", typed.SessionID)
		assert.Equal(t, "", typed.WorktreePath)
	})

	t.Run("non-string values are ignored", func(t *testing.T) {
		ctx := map[string]interface{}{
			workflowContextKeyID:           123,
			workflowContextKeyName:         true,
			workflowContextKeyPath:         []string{"not", "string"},
			workflowContextKeyBranch:       map[string]interface{}{"x": "y"},
			workflowContextKeyMode:         1.23,
			workflowContextKeyRunID:        nil,
			workflowContextKeySessionID:    999,
			workflowContextKeyWorktreePath: false,
		}

		typed := workflowContextToTyped(ctx)

		assert.Equal(t, "", typed.ID)
		assert.Equal(t, "", typed.Name)
		assert.Equal(t, "", typed.Path)
		assert.Equal(t, "", typed.Branch)
		assert.Equal(t, "", typed.Mode)
		assert.Equal(t, "", typed.RunID)
		assert.Equal(t, "", typed.SessionID)
		assert.Equal(t, "", typed.WorktreePath)
	})
}
