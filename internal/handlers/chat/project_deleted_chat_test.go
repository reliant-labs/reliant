package chat

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/chataccess"
	"github.com/reliant-labs/reliant/internal/chataccess/chataccesstest"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/projectpurge"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/toolexec/toolexectest"
	"github.com/reliant-labs/reliant/internal/viewertest"
)

// A chat of a project scheduled for deletion is hidden with it (control-plane
// docs/design/unified-shell-and-projects.md §11.3): whoever saved its id, its
// owner or a viewer of a shared chat, is answered NotFound by every read and
// write until the project is restored, while the store's own unauthorized
// reads (the purge's) still see it.
func TestAChatOfADeletedProjectIsHiddenFromASavedIDUntilRestored(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	f := viewertest.New(t, repo)
	svc := NewTestChat(t, WithChatDeps(Deps{TriggerReads: &MockTriggerReads{}, Repo: repo, Chats: chataccesstest.For(repo), Workflows: store.NewWorkflowExtStore(repo.ORM()), Worktrees: store.NewWorktreeExtStore(repo.ORM()), ProjectRows: store.NewProjectExtStore(repo.ORM()), TriggerEvents: store.NewTriggerEventExtStore(repo.ORM()), Orgs: f.Membership, Router: &wakeRecordingRouter{Recorder: &toolexectest.Recorder{}}}))

	transcript := func(ctx context.Context) error {
		_, err := svc.ListMessages(ctx, connect.NewRequest(&reliantv1.ListMessagesRequest{ChatId: f.ChatID}))
		return err
	}
	getChat := func(ctx context.Context) error {
		_, err := svc.GetChat(ctx, connect.NewRequest(&reliantv1.GetChatRequest{ChatId: f.ChatID}))
		return err
	}
	markUnread := func(ctx context.Context) error {
		_, err := svc.MarkUnreadChat(ctx, connect.NewRequest(&reliantv1.MarkUnreadChatRequest{ChatId: f.ChatID}))
		return err
	}
	rename := func(ctx context.Context) error {
		title := "renamed"
		_, err := svc.UpdateChat(ctx, connect.NewRequest(&reliantv1.UpdateChatRequest{ChatId: f.ChatID, Title: &title}))
		return err
	}
	bareRow := func(ctx context.Context) error {
		_, err := chataccess.NewFromORM(repo.ORM()).GetChatRowWithUserCheck(ctx, f.ChatID, f.OwnerID)
		return err
	}

	// Before: the owner reads and writes, the viewer reads.
	for name, call := range map[string]func(context.Context) error{"transcript": transcript, "chat": getChat, "mark unread": markUnread, "rename": rename, "bare row": bareRow} {
		require.NoError(t, call(f.Owner), "owner %s before the delete", name)
	}
	require.NoError(t, transcript(f.Viewer), "viewer's transcript before the delete")
	require.NoError(t, getChat(f.Viewer), "viewer's chat before the delete")

	org := viewertest.OrgID
	decided := core.ProjectDecision{OwnerUserID: f.OwnerID, OrgID: &org}
	_, err := projectpurge.New(projectpurge.Deps{DB: repo.ORM()}).ScheduleProjectDeletion(context.Background(), f.ProjectID, f.OwnerID,
		time.Date(2026, time.January, 12, 12, 0, 0, 0, time.UTC), decided)
	require.NoError(t, err)

	for name, call := range map[string]func(context.Context) error{"transcript": transcript, "chat": getChat, "mark unread": markUnread, "rename": rename} {
		err := call(f.Owner)
		require.Error(t, err, "owner %s with a saved id after the delete", name)
		assert.Equal(t, connect.CodeNotFound, svcerr.Code(err), "owner %s: %v", name, err)
	}
	for name, call := range map[string]func(context.Context) error{"transcript": transcript, "chat": getChat} {
		err := call(f.Viewer)
		require.Error(t, err, "viewer %s with a saved id after the delete", name)
		assert.Equal(t, connect.CodeNotFound, svcerr.Code(err), "viewer %s: %v", name, err)
	}
	assert.True(t, errors.Is(bareRow(f.Owner), core.ErrChatNotFound), "the checked bare-row read refuses it too")
	row, err := store.GetChatRow(context.Background(), repo.ORM(), f.ChatID)
	require.NoError(t, err, "the store's unauthorized read, which the purge relies on, still sees the chat")
	assert.True(t, row.ProjectDeleted)

	_, err = projectpurge.New(projectpurge.Deps{DB: repo.ORM()}).RestoreProject(context.Background(), f.ProjectID, decided)
	require.NoError(t, err)
	for name, call := range map[string]func(context.Context) error{"transcript": transcript, "chat": getChat, "mark unread": markUnread, "rename": rename, "bare row": bareRow} {
		require.NoError(t, call(f.Owner), "owner %s after the restore", name)
	}
	require.NoError(t, transcript(f.Viewer), "viewer's transcript after the restore")
}
