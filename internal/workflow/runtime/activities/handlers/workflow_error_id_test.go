// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// idCapturingRepo records the entity id each chat_update is written under —
// the field both dedup paths collapse on.
type idCapturingRepo struct {
	db.Repository
	entityIDs []string
	payloads  []map[string]interface{}
}

func (r *idCapturingRepo) CreateChatUpdate(_ context.Context, _ string, _ db.UpdateType, entityID string, data string) error {
	r.entityIDs = append(r.entityIDs, entityID)
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return err
	}
	r.payloads = append(r.payloads, payload)
	return nil
}

// An explicit ErrorID must be the id the row is written under, in BOTH the
// entity id and the payload's own `id`. They have to agree: the server dedups
// on the entity id and the frontend dedups on the payload id, so a row that set
// only one of them would collapse in one place and duplicate in the other.
func TestWriteWorkflowErrorUsesExplicitID(t *testing.T) {
	t.Parallel()
	repo := &idCapturingRepo{}

	const explicit = "activity-error-wf-1-11114"
	got, err := WriteWorkflowError(context.Background(), repo, WorkflowErrorInput{
		ChatID:       "chat-1",
		WorkflowID:   "wf-1",
		ErrorMessage: "429 Too Many Requests",
		ErrorType:    "retry_exhaustion",
		ErrorID:      explicit,
	})
	require.NoError(t, err)

	assert.Equal(t, explicit, got)
	require.Len(t, repo.entityIDs, 1)
	assert.Equal(t, explicit, repo.entityIDs[0],
		"the entity id is what the server's last-write-per-entity collapse keys on")
	assert.Equal(t, explicit, repo.payloads[0]["id"],
		"the payload id is what the frontend's dedup keys on; the two must agree")
}

// The reconciler's path must keep working: a hard Temporal termination has no
// failing activity to key on, so the error is its own event and gets a minted
// uuid. Breaking this would silently stop reporting workflow deaths.
func TestWriteWorkflowErrorMintsIDWhenUnset(t *testing.T) {
	t.Parallel()
	repo := &idCapturingRepo{}

	first, err := WriteWorkflowError(context.Background(), repo, WorkflowErrorInput{
		ChatID:       "chat-1",
		WorkflowID:   "wf-1",
		ErrorMessage: "workflow terminated",
		ErrorType:    "workflow_terminated",
	})
	require.NoError(t, err)
	require.NotEmpty(t, first)

	second, err := WriteWorkflowError(context.Background(), repo, WorkflowErrorInput{
		ChatID:       "chat-1",
		WorkflowID:   "wf-1",
		ErrorMessage: "workflow terminated",
		ErrorType:    "workflow_terminated",
	})
	require.NoError(t, err)

	assert.NotEqual(t, first, second,
		"without an explicit id each error is its own event and must get its own row")
	assert.Equal(t, first, repo.payloads[0]["id"])
}
