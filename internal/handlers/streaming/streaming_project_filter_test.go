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

// TestChatFocusFilter_CarriesTheOpenChatsCrossProjectUpdates pins that the
// chat a client has open, and the chats an agent started from it, get their
// user-level updates whatever project the client's user stream is scoped to.
// A deep link or notification can open a chat outside the client's current
// project; its run status and spawned sub-agent chats are project-filtered
// user updates, so the chat stream forwards exactly those the user stream
// drops — and never one it delivers, so nothing arrives twice.
func TestChatFocusFilter_CarriesTheOpenChatsCrossProjectUpdates(t *testing.T) {
	t.Parallel()
	current, foreign := "proj-a", "proj-b"
	const open, child, stranger = "chat-open", "chat-child", "chat-other"

	lookups := map[string]int{}
	newFilter := func() *chatFocusFilter {
		f := newChatFocusFilter(nil, current, open)
		f.parentOf = func(_ context.Context, chatID string) (string, error) {
			lookups[chatID]++
			switch chatID {
			case child:
				return open, nil
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

	t.Run("open chat in another project is forwarded", func(t *testing.T) {
		f := newFilter()
		for _, typ := range []db.UserUpdateType{db.UserUpdateChatActivityChanged, db.UserUpdateChatStateChange, db.UserUpdateChatTitleChanged} {
			assert.True(t, f.matches(context.Background(), chatUpdate(typ, open, foreign)), typ.String())
		}
	})
	t.Run("update identified only by entity id is forwarded", func(t *testing.T) {
		f := newFilter()
		project := foreign
		u := &db.UserUpdate{SequenceNumber: 5, UpdateType: db.UserUpdateChatStateChange, ProjectID: &project, EntityType: db.EntityTypeChat, EntityID: open}
		assert.True(t, f.matches(context.Background(), u))
	})
	t.Run("sub-agent chat of the open chat is forwarded, lookup memoized", func(t *testing.T) {
		f := newFilter()
		for i := 0; i < 3; i++ {
			assert.True(t, f.matches(context.Background(), chatUpdate(db.UserUpdateChatCreated, child, foreign)))
		}
		assert.Equal(t, 1, lookups[child])
	})
	t.Run("unrelated chat in another project is dropped", func(t *testing.T) {
		f := newFilter()
		assert.False(t, f.matches(context.Background(), chatUpdate(db.UserUpdateChatActivityChanged, stranger, foreign)))
	})
	t.Run("lookup failure drops the update without pinning the answer", func(t *testing.T) {
		f := newFilter()
		assert.False(t, f.matches(context.Background(), chatUpdate(db.UserUpdateChatCreated, "chat-flaky", foreign)))
		assert.False(t, f.matches(context.Background(), chatUpdate(db.UserUpdateChatCreated, "chat-flaky", foreign)))
		assert.Equal(t, 2, lookups["chat-flaky"])
	})
	t.Run("an update the user stream delivers is not forwarded", func(t *testing.T) {
		f := newFilter()
		assert.False(t, f.matches(context.Background(), chatUpdate(db.UserUpdateChatActivityChanged, open, current)),
			"the open chat's in-project update reaches the client on the user stream")
		assert.False(t, f.matches(context.Background(), &db.UserUpdate{UpdateType: db.UserUpdateDaemonHeartbeat}),
			"an ephemeral project-less update reaches the client on the user stream")
	})
	t.Run("a user stream with no project scope needs nothing forwarded", func(t *testing.T) {
		assert.Nil(t, newChatFocusFilter(nil, "", open))
	})
}

// TestUserUpdateFilter_IsTheProjectFilterAlone pins that the user stream no
// longer knows which chat is open: an update about a chat in another project
// is the chat stream's to deliver (chatFocusFilter), never this one's.
func TestUserUpdateFilter_IsTheProjectFilterAlone(t *testing.T) {
	t.Parallel()
	f := userUpdateFilter{projectID: "proj-a"}
	chatID, foreign, current := "chat-open", "proj-b", "proj-a"
	assert.False(t, f.matches(context.Background(), &db.UserUpdate{
		SequenceNumber: 5, UpdateType: db.UserUpdateChatActivityChanged, ChatID: &chatID, ProjectID: &foreign,
	}))
	assert.True(t, f.matches(context.Background(), &db.UserUpdate{
		SequenceNumber: 5, UpdateType: db.UserUpdateChatActivityChanged, ChatID: &chatID, ProjectID: &current,
	}))
}
