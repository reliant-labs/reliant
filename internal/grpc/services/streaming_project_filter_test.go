package services

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/db"
)

// TestUserUpdateMatchesProject pins which user updates a project-scoped stream
// delivers. The web client always subscribes scoped to the current project, so
// anything this filter drops never reaches the UI.
//
// Workflow drafts belong to a user, not a project, and their update carries no
// project_id. Before the draft exemption, a project-scoped stream dropped every
// WORKFLOW_DRAFT_UPDATED, so an open workflow editor never learned that an agent
// in some chat had edited the workflow it was showing.
func TestUserUpdateMatchesProject(t *testing.T) {
	t.Parallel()
	project := "proj-a"
	other := "proj-b"

	cases := []struct {
		name      string
		update    db.UserUpdate
		projectID string
		want      bool
	}{
		{"unscoped stream receives everything", db.UserUpdate{UpdateType: db.UserUpdateChatCreated}, "", true},
		{"same-project chat update passes", db.UserUpdate{UpdateType: db.UserUpdateChatCreated, ProjectID: &project}, project, true},
		{"other-project chat update is dropped", db.UserUpdate{UpdateType: db.UserUpdateChatCreated, ProjectID: &other}, project, false},
		{"project-less persisted chat update is dropped", db.UserUpdate{SequenceNumber: 7, UpdateType: db.UserUpdateChatCreated}, project, false},
		{"project-less workflow draft update passes", db.UserUpdate{
			UpdateType: db.UserUpdateWorkflowDraftUpdated, EntityType: db.EntityTypeWorkflowDraft, EntityID: "draft-1",
		}, project, true},
		// A daemon serves a user, not a project; its ephemeral signals have no
		// project and must reach whichever project the client has open.
		{"ephemeral project-less daemon heartbeat passes", db.UserUpdate{
			UpdateType: db.UserUpdateDaemonHeartbeat, EntityType: db.EntityTypeSystem, EntityID: "daemon-1",
		}, project, true},
		{"ephemeral project-less refetch passes", db.UserUpdate{
			UpdateType: db.UserUpdateRefetch, EntityType: db.EntityTypeSystem, EntityID: "daemon-1",
		}, project, true},
		// Persisted project-less updates (seq > 0) keep the filter.
		{"persisted project-less refetch is dropped", db.UserUpdate{
			SequenceNumber: 42, UpdateType: db.UserUpdateRefetch, EntityType: db.EntityTypeSystem,
		}, project, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, userUpdateMatchesProject(&tc.update, tc.projectID))
		})
	}
}
