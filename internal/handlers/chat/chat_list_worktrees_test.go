package chat

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/agentmailbox"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/chataccess"
	"github.com/reliant-labs/reliant/internal/chataccess/chataccesstest"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/projectpurge"
	"github.com/reliant-labs/reliant/internal/store"
)

func seedListChatsWorktree(t *testing.T, ctx context.Context, repo *db.Repo, projectID, name string, isMain bool) *db.Worktree {
	t.Helper()
	now := time.Now().UTC()
	w := &db.Worktree{
		ID: uuid.NewString(), Name: name, Path: "/ws/" + name, Branch: "br-" + name, BaseBranch: "main",
		ProjectID: projectID, Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE),
		IsMain: isMain, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}
	require.NoError(t, repo.CreateWorktree(ctx, w))
	return w
}

func seedListChatsChat(t *testing.T, ctx context.Context, repo *db.Repo, projectID string, worktreeID *string, title string) string {
	t.Helper()
	now := time.Now().UTC()
	id := uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: id, Title: title, ProjectID: projectID, WorktreeID: worktreeID, UserID: "test-user",
		State: 2, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	return id
}

// The sidebar groups chats by workspace and must be able to do so without the
// project's full worktree list: ListChats carries each chat's workspace and the
// main workspace itself.
func TestListChats_CarriesEachChatsWorktreeSummary(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	svc := &Service{deps: Deps{Repo: repo, Chats: chataccess.NewFromORM(repo.ORM()), Workflows: store.NewWorkflowExtStore(repo.ORM()), Worktrees: store.NewWorktreeExtStore(repo.ORM()), ProjectRows: store.NewProjectExtStore(repo.ORM()), TriggerEvents: store.NewTriggerEventExtStore(repo.ORM()), Questions: store.NewQuestionExtStore(repo.ORM()), Spawns: store.NewToolCallExtStore(repo.ORM()), Selections: store.NewChatSelectionExtStore(repo.ORM()), Messages: store.NewMessageExtStore(repo.ORM()), ToolBlocks: store.NewMessageContentBlockExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM()), Mailbox: agentmailbox.New(agentmailbox.Deps{Store: store.NewAgentMessageExtStore(repo.ORM()), Announcer: repo.UpdateWriter()}), AgentMessages: store.NewAgentMessageExtStore(repo.ORM())}}

	projectID, mainID := newInvariantTestProject(t, ctx, repo, true)

	// Far more workspaces than any page: only two have chats. The response must
	// not grow with the rest.
	var feature, archived *db.Worktree
	for i := 0; i < 150; i++ {
		w := seedListChatsWorktree(t, ctx, repo, projectID, fmt.Sprintf("agent-%03d", i), false)
		switch i {
		case 7:
			feature = w
		case 8:
			archived = w
		}
	}
	require.NoError(t, repo.ArchiveWorktree(ctx, archived.ID))

	onMain := seedListChatsChat(t, ctx, repo, projectID, &mainID, "on main")
	noWorktree := seedListChatsChat(t, ctx, repo, projectID, nil, "no worktree")
	onFeature := seedListChatsChat(t, ctx, repo, projectID, &feature.ID, "on feature")
	onFeature2 := seedListChatsChat(t, ctx, repo, projectID, &feature.ID, "also on feature")
	onArchived := seedListChatsChat(t, ctx, repo, projectID, &archived.ID, "on archived worktree")

	resp, err := svc.ListChats(ctx, connect.NewRequest(&reliantv1.ListChatsRequest{ProjectId: projectID}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.Chats, 5)

	byID := map[string]*reliantv1.Chat{}
	for _, c := range resp.Msg.Chats {
		byID[c.Id] = c
	}

	t.Run("main worktree is carried even for a chat with no worktree", func(t *testing.T) {
		require.Len(t, resp.Msg.MainWorktrees, 1, "a project-scoped list carries that project's main workspace only")
		main := resp.Msg.MainWorktrees[0]
		assert.Equal(t, mainID, main.Id)
		assert.True(t, main.IsMain)
		assert.Equal(t, "main", main.Name)
		assert.Equal(t, "main", main.Branch)
		assert.Equal(t, projectID, main.ProjectId)
		assert.Nil(t, byID[noWorktree].Worktree, "a chat with no worktree_id has no worktree summary")
	})

	t.Run("each chat carries its own worktree", func(t *testing.T) {
		require.NotNil(t, byID[onMain].Worktree)
		assert.Equal(t, mainID, byID[onMain].Worktree.Id)
		assert.True(t, byID[onMain].Worktree.IsMain)

		for _, id := range []string{onFeature, onFeature2} {
			w := byID[id].Worktree
			require.NotNil(t, w)
			assert.Equal(t, feature.ID, w.Id)
			assert.Equal(t, feature.Name, w.Name)
			assert.Equal(t, feature.Branch, w.Branch)
			assert.Equal(t, feature.Path, w.Path)
			assert.False(t, w.IsMain)
			assert.Equal(t, reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE, w.Status)
			assert.Nil(t, w.DeletedAt)
		}
	})

	t.Run("a chat on an archived worktree still names it", func(t *testing.T) {
		w := byID[onArchived].Worktree
		require.NotNil(t, w, "an archived workspace must not hide the chat's workspace")
		assert.Equal(t, archived.ID, w.Id)
		require.NotNil(t, w.DeletedAt)
	})
}

func TestListChats_NoWorktreesYieldsNoSummaries(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	svc := &Service{deps: Deps{Repo: repo, Chats: chataccess.NewFromORM(repo.ORM()), Workflows: store.NewWorkflowExtStore(repo.ORM()), Worktrees: store.NewWorktreeExtStore(repo.ORM()), ProjectRows: store.NewProjectExtStore(repo.ORM()), TriggerEvents: store.NewTriggerEventExtStore(repo.ORM()), Questions: store.NewQuestionExtStore(repo.ORM()), Spawns: store.NewToolCallExtStore(repo.ORM()), Selections: store.NewChatSelectionExtStore(repo.ORM()), Messages: store.NewMessageExtStore(repo.ORM()), ToolBlocks: store.NewMessageContentBlockExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM()), Mailbox: agentmailbox.New(agentmailbox.Deps{Store: store.NewAgentMessageExtStore(repo.ORM()), Announcer: repo.UpdateWriter()}), AgentMessages: store.NewAgentMessageExtStore(repo.ORM())}}

	projectID, _ := newInvariantTestProject(t, ctx, repo, false)
	id := seedListChatsChat(t, ctx, repo, projectID, nil, "lonely")

	resp, err := svc.ListChats(ctx, connect.NewRequest(&reliantv1.ListChatsRequest{ProjectId: projectID}))
	require.NoError(t, err)
	require.Len(t, resp.Msg.Chats, 1)
	assert.Equal(t, id, resp.Msg.Chats[0].Id)
	assert.Nil(t, resp.Msg.Chats[0].Worktree)
	assert.Empty(t, resp.Msg.MainWorktrees, "a project with no main worktree row reports none")
}

func TestListChats_DoesNotLeakAnotherUsersMainWorktree(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	owner := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	intruder := context.WithValue(context.Background(), auth.UserIDContextKey, "someone-else")
	svc := &Service{deps: Deps{Repo: repo, Chats: chataccess.NewFromORM(repo.ORM()), Workflows: store.NewWorkflowExtStore(repo.ORM()), Worktrees: store.NewWorktreeExtStore(repo.ORM()), ProjectRows: store.NewProjectExtStore(repo.ORM()), TriggerEvents: store.NewTriggerEventExtStore(repo.ORM()), Questions: store.NewQuestionExtStore(repo.ORM()), Spawns: store.NewToolCallExtStore(repo.ORM()), Selections: store.NewChatSelectionExtStore(repo.ORM()), Messages: store.NewMessageExtStore(repo.ORM()), ToolBlocks: store.NewMessageContentBlockExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM()), Mailbox: agentmailbox.New(agentmailbox.Deps{Store: store.NewAgentMessageExtStore(repo.ORM()), Announcer: repo.UpdateWriter()}), AgentMessages: store.NewAgentMessageExtStore(repo.ORM())}}

	projectID, _ := newInvariantTestProject(t, owner, repo, true)

	resp, err := svc.ListChats(intruder, connect.NewRequest(&reliantv1.ListChatsRequest{ProjectId: projectID}))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.Chats)
	assert.Empty(t, resp.Msg.MainWorktrees, "another user's project must not disclose its main workspace")
}

// projectReadCounter is the real repository, counting the project reads a
// chat list makes.
type projectReadCounter struct {
	*db.Repo
	gets, lists atomic.Int32
}

func (r *projectReadCounter) GetProject(ctx context.Context, id string) (*db.Project, error) {
	r.gets.Add(1)
	return r.Repo.GetProject(ctx, id)
}

func (r *projectReadCounter) ListProjectsByIDs(ctx context.Context, ids []string) ([]*db.Project, error) {
	r.lists.Add(1)
	return r.Repo.ListProjectsByIDs(ctx, ids)
}

// The "All projects" list names the main workspace of every project the
// caller has, and reads those projects in one query however many there are:
// prod trace 0c768027fe97ec8721d215100311c27f made one GetProject per project
// (19 of them) on every sidebar refresh.
func TestListChats_ReadsTheMainWorkspacesProjectsInOneQuery(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	counter := &projectReadCounter{Repo: repo}
	svc := &Service{deps: Deps{Repo: counter, Chats: chataccesstest.For(counter)}}

	const projects = 12
	wantMains := map[string]string{}
	for i := 0; i < projects; i++ {
		projectID, mainID := newInvariantTestProject(t, ctx, repo, true)
		seedListChatsChat(t, ctx, repo, projectID, &mainID, fmt.Sprintf("chat %d", i))
		wantMains[mainID] = projectID
	}
	// A project scheduled for deletion is not the caller's to read, so its
	// main workspace is left out, exactly as one GetProject per project did.
	doomed, doomedMain := newInvariantTestProject(t, ctx, repo, true)
	doomedProject, err := repo.GetProject(ctx, doomed)
	require.NoError(t, err)
	_, err = projectpurge.New(projectpurge.Deps{DB: repo.ORM()}).ScheduleProjectDeletion(ctx, doomed, "test-user", time.Now().Add(time.Hour), doomedProject.Decision())
	require.NoError(t, err)

	resp, err := svc.ListChats(ctx, connect.NewRequest(&reliantv1.ListChatsRequest{}))
	require.NoError(t, err)

	gotMains := map[string]string{}
	for _, w := range resp.Msg.MainWorktrees {
		gotMains[w.Id] = w.ProjectId
	}
	assert.Equal(t, wantMains, gotMains, "every readable project's main workspace, and no other")
	assert.NotContains(t, gotMains, doomedMain)
	assert.Len(t, resp.Msg.Chats, projects)

	assert.Equal(t, int32(0), counter.gets.Load(), "no project is read one at a time")
	assert.Equal(t, int32(1), counter.lists.Load(), "the projects are read in one query")
}

// An empty project_id is the sidebar's "All projects" view: every chat and
// main workspace of the caller's projects, and nothing of anyone else's.
func TestListChats_AllProjectsSpansTheCallersProjectsOnly(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx := context.WithValue(context.Background(), auth.UserIDContextKey, "test-user")
	svc := &Service{deps: Deps{Repo: repo, Chats: chataccess.NewFromORM(repo.ORM()), Workflows: store.NewWorkflowExtStore(repo.ORM()), Worktrees: store.NewWorktreeExtStore(repo.ORM()), ProjectRows: store.NewProjectExtStore(repo.ORM()), TriggerEvents: store.NewTriggerEventExtStore(repo.ORM()), Questions: store.NewQuestionExtStore(repo.ORM()), Spawns: store.NewToolCallExtStore(repo.ORM()), Selections: store.NewChatSelectionExtStore(repo.ORM()), Messages: store.NewMessageExtStore(repo.ORM()), ToolBlocks: store.NewMessageContentBlockExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM()), Mailbox: agentmailbox.New(agentmailbox.Deps{Store: store.NewAgentMessageExtStore(repo.ORM()), Announcer: repo.UpdateWriter()}), AgentMessages: store.NewAgentMessageExtStore(repo.ORM())}}

	projectA, mainA := newInvariantTestProject(t, ctx, repo, true)
	projectB, mainB := newInvariantTestProject(t, ctx, repo, true)
	inA := seedListChatsChat(t, ctx, repo, projectA, &mainA, "in A")
	inB := seedListChatsChat(t, ctx, repo, projectB, &mainB, "in B")

	// Someone else's project, chat and main workspace.
	now := time.Now().UTC()
	foreignProject := "foreign-" + uuid.NewString()
	require.NoError(t, repo.CreateProject(ctx, &db.Project{
		ID: foreignProject, UserID: "someone-else", Name: "Foreign", Path: t.TempDir(),
		CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))
	foreignMain := seedListChatsWorktree(t, ctx, repo, foreignProject, "foreign-main", true)
	foreignChat := uuid.NewString()
	require.NoError(t, repo.CreateChat(ctx, &db.Chat{
		ID: foreignChat, Title: "not yours", ProjectID: foreignProject, WorktreeID: &foreignMain.ID,
		UserID: "someone-else", State: 2, CreatedAt: now, UpdatedAt: now, LastActive: now,
	}))

	resp, err := svc.ListChats(ctx, connect.NewRequest(&reliantv1.ListChatsRequest{}))
	require.NoError(t, err)

	chatIDs := map[string]string{}
	for _, c := range resp.Msg.Chats {
		chatIDs[c.Id] = c.ProjectId
	}
	assert.Equal(t, projectA, chatIDs[inA])
	assert.Equal(t, projectB, chatIDs[inB])
	assert.NotContains(t, chatIDs, foreignChat, "another user's chat must not be listed")

	mains := map[string]string{}
	for _, w := range resp.Msg.MainWorktrees {
		mains[w.Id] = w.ProjectId
	}
	assert.Equal(t, projectA, mains[mainA])
	assert.Equal(t, projectB, mains[mainB])
	assert.NotContains(t, mains, foreignMain.ID, "another user's main workspace must not be disclosed")
}
