// Copyright (c) 2025 Reliant Labs
package services

import (
	"testing"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/stretchr/testify/require"
)

func strPtr(s string) *string { return &s }

// TestActiveWorkflowNameForResume covers the transition_to ghost-recovery bug:
// after a completed one-shot pipeline hands the chat off to its transition
// target, the db.Workflow ROW name stays stale (UpdateWorkflowName is
// pending-only) while chat.WorkflowName holds the target. A resume/ghost
// restart must prefer chat.WorkflowName so it does not re-run the finished
// pipeline (e.g. forge-one-shot) on an already-built project.
func TestActiveWorkflowNameForResume(t *testing.T) {
	tests := []struct {
		name     string
		chat     *db.Chat
		workflow *db.Workflow
		want     string
	}{
		{
			name:     "post-transition: chat moved on, row name is stale",
			chat:     &db.Chat{WorkflowName: strPtr("builtin://agent")},
			workflow: &db.Workflow{WorkflowName: "builtin://forge-one-shot"},
			want:     "builtin://agent", // must NOT restart the finished pipeline
		},
		{
			name:     "no transition: chat and row agree (fresh-start unaffected)",
			chat:     &db.Chat{WorkflowName: strPtr("builtin://agent")},
			workflow: &db.Workflow{WorkflowName: "builtin://agent"},
			want:     "builtin://agent",
		},
		{
			name:     "defensive: chat has no name, fall back to row",
			chat:     &db.Chat{WorkflowName: nil},
			workflow: &db.Workflow{WorkflowName: "builtin://forge-one-shot"},
			want:     "builtin://forge-one-shot",
		},
		{
			name:     "defensive: chat name empty, fall back to row",
			chat:     &db.Chat{WorkflowName: strPtr("")},
			workflow: &db.Workflow{WorkflowName: "builtin://forge-one-shot"},
			want:     "builtin://forge-one-shot",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, activeWorkflowNameForResume(tt.chat, tt.workflow))
		})
	}
}
