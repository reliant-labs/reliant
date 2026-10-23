// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
)

// Every root run records its own Temporal run id on the chat as it starts —
// including a continue-as-new successor, whose start nothing in the API sees.
// Before, chats.run_id kept naming the closed predecessor, and queries aimed at
// it replayed that run's whole history on a worker (2026-10-23). A sub-agent's
// "started", and the "started" a pause resume sends for the same run, record
// nothing.
func TestRecordRootRunID(t *testing.T) {
	t.Parallel()
	type call struct{ chatID, workflowID, runID string }
	for name, tc := range map[string]struct {
		input WorkflowStatusInput
		want  []call
	}{
		"root run start (fresh, reset or continue-as-new)": {
			input: WorkflowStatusInput{ChatID: "chat-1", WorkflowID: "chat-1", Status: "started"},
			want:  []call{{"chat-1", "chat-1", "default-test-run-id"}},
		},
		"sub-agent start": {
			input: WorkflowStatusInput{ChatID: "chat-1", WorkflowID: "child", ParentWorkflowID: "chat-1", Status: "started"},
		},
		"resume of the same run": {
			input: WorkflowStatusInput{ChatID: "chat-1", WorkflowID: "chat-1", Status: "started", Resumed: true},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var got []call
			store := &MockWorkflowStatusStore{
				SetChatRunIDFunc: func(_ context.Context, chatID, workflowID, runID string) (bool, error) {
					got = append(got, call{chatID, workflowID, runID})
					return true, nil
				},
			}
			a := NewWorkflowStatusActivity(store)
			env := (&temporaltest.WorkflowTestSuite{}).NewTestActivityEnvironment()
			record := func(ctx context.Context, in WorkflowStatusInput) error {
				a.recordRootRunID(ctx, in)
				return nil
			}
			env.RegisterActivity(record)
			_, err := env.ExecuteActivity(record, tc.input)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
