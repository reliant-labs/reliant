package streaming

import (
	"context"
	"errors"
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

// TestUserUpdateFilter_SubscribedChatBypassesProjectFilter pins that the chat a
// stream explicitly subscribes to, and the chats an agent started from it, are
// delivered whatever project the stream is scoped to. A deep link or
// notification can open a chat outside the client's current project; its run
// status and spawned sub-agent chats are project-filtered user updates, so
// without the exemption they never arrived.
func TestUserUpdateFilter_SubscribedChatBypassesProjectFilter(t *testing.T) {
	t.Parallel()
	current, foreign := "proj-a", "proj-b"
	const subscribed, child, stranger = "chat-sub", "chat-child", "chat-other"

	lookups := map[string]int{}
	newFilter := func(subscribeChatID string) *userUpdateFilter {
		f := &userUpdateFilter{projectID: current, subscribeChatID: subscribeChatID, childOfSubscribed: map[string]bool{}}
		f.parentOf = func(_ context.Context, chatID string) (string, error) {
			lookups[chatID]++
			switch chatID {
			case child:
				return subscribed, nil
			case "chat-flaky":
				return "", errors.New("db down")
			}
			return "", nil
		}
		return f
	}
	chatUpdate := func(typ db.UserUpdateType, chatID, project string) *db.UserUpdate {
		return &db.UserUpdate{
			SequenceNumber: 5, UpdateType: typ, ChatID: &chatID, ProjectID: &project,
			EntityType: db.EntityTypeChat, EntityID: chatID,
		}
	}

	t.Run("subscribed chat in another project passes", func(t *testing.T) {
		f := newFilter(subscribed)
		for _, typ := range []db.UserUpdateType{db.UserUpdateChatActivityChanged, db.UserUpdateChatStateChange, db.UserUpdateChatTitleChanged} {
			assert.True(t, f.matches(context.Background(), chatUpdate(typ, subscribed, foreign)), typ.String())
		}
	})
	t.Run("update identified only by entity id passes", func(t *testing.T) {
		f := newFilter(subscribed)
		u := &db.UserUpdate{SequenceNumber: 5, UpdateType: db.UserUpdateChatStateChange, EntityType: db.EntityTypeChat, EntityID: subscribed}
		assert.True(t, f.matches(context.Background(), u))
	})
	t.Run("sub-agent chat of the subscribed chat passes, lookup memoized", func(t *testing.T) {
		f := newFilter(subscribed)
		for i := 0; i < 3; i++ {
			assert.True(t, f.matches(context.Background(), chatUpdate(db.UserUpdateChatCreated, child, foreign)))
		}
		assert.Equal(t, 1, lookups[child])
	})
	t.Run("unrelated chat in another project is dropped", func(t *testing.T) {
		f := newFilter(subscribed)
		assert.False(t, f.matches(context.Background(), chatUpdate(db.UserUpdateChatActivityChanged, stranger, foreign)))
	})
	t.Run("lookup failure drops the update without pinning the answer", func(t *testing.T) {
		f := newFilter(subscribed)
		assert.False(t, f.matches(context.Background(), chatUpdate(db.UserUpdateChatCreated, "chat-flaky", foreign)))
		assert.False(t, f.matches(context.Background(), chatUpdate(db.UserUpdateChatCreated, "chat-flaky", foreign)))
		assert.Equal(t, 2, lookups["chat-flaky"])
	})
	t.Run("without a chat subscription the project filter stands", func(t *testing.T) {
		f := newFilter("")
		assert.False(t, f.matches(context.Background(), chatUpdate(db.UserUpdateChatActivityChanged, subscribed, foreign)))
	})
	t.Run("project-matching update still passes", func(t *testing.T) {
		f := newFilter(subscribed)
		assert.True(t, f.matches(context.Background(), chatUpdate(db.UserUpdateChatActivityChanged, stranger, current)))
	})
}
