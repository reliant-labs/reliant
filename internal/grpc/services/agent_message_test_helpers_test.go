// Copyright (c) 2025 Reliant Labs
package services

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
)

// queueHumanMessage puts a human message in a thread's mailbox, exactly as
// SendAgentMessage does. Shared by the mailbox tests in this package.
func queueHumanMessage(t *testing.T, repo *db.Repo, chatID, fromThreadID, toThreadID, body string, createdAt time.Time) string {
	t.Helper()
	id := uuid.NewString()
	require.NoError(t, repo.EnqueueAgentMessage(t.Context(), &db.AgentMessage{
		ID:           id,
		ChatID:       chatID,
		FromThreadID: fromThreadID,
		ToThreadID:   toThreadID,
		Kind:         core.AgentMessageKindHumanMessage,
		Body:         body,
		Status:       core.AgentMessageStatusQueued,
		CreatedAt:    createdAt,
	}))
	return id
}
