// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/svcerr"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const moveUser = "move-user"

// moveFixture is one user's projects, machines and chats in a test database.
type moveFixture struct {
	t       *testing.T
	ctx     context.Context
	repo    *db.Repo
	service *Service
	now     time.Time
}

func newMoveFixture(t *testing.T) *moveFixture {
	t.Helper()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	return &moveFixture{
		t:       t,
		ctx:     context.WithValue(context.Background(), auth.UserIDContextKey, moveUser),
		repo:    repo,
		service: &Service{deps: Deps{Repo: repo}, threads: threads.NewService(repo)},
		now:     time.Now().UTC(),
	}
}

// project creates a project of userID with its main worktree, and returns
// both ids.
func (f *moveFixture) project(userID, name string) (projectID, mainWorktreeID string) {
	f.t.Helper()
	projectID = "move-" + uuid.NewString()
	require.NoError(f.t, f.repo.CreateProject(f.ctx, &db.Project{
		ID: projectID, UserID: userID, Name: name, Path: "/created/" + name,
		IsGitRepo: true, CreatedAt: f.now, UpdatedAt: f.now, LastActive: f.now,
	}))
	mainWorktreeID = f.worktree(projectID, "main", "/created/"+name, true)
	return projectID, mainWorktreeID
}

func (f *moveFixture) worktree(projectID, name, path string, isMain bool) string {
	f.t.Helper()
	id := uuid.NewString()
	require.NoError(f.t, f.repo.CreateWorktree(f.ctx, &db.Worktree{
		ID: id, Name: name, Path: path, Branch: name, BaseBranch: "main", ProjectID: projectID,
		Status: int32(reliantv1.WorktreeStatus_WORKTREE_STATUS_ACTIVE), IsMain: isMain,
		CreatedAt: f.now, UpdatedAt: f.now, LastActive: f.now,
	}))
	return id
}

func (f *moveFixture) machine(userID, name string) string {
	f.t.Helper()
	id := uuid.NewString()
	require.NoError(f.t, f.repo.UpsertDaemon(f.ctx, &db.Daemon{ID: id, UserID: userID, Name: name}))
	return id
}

func (f *moveFixture) install(projectID, daemonID, path string) {
	f.t.Helper()
	require.NoError(f.t, f.repo.UpsertProjectDaemon(f.ctx, projectID, daemonID, path, nil))
}

// chat creates an idle chat with a finished root run, so it has a root thread
// for the move note.
func (f *moveFixture) chat(projectID, worktreeID string, daemonID *string) string {
	f.t.Helper()
	return f.chatWithRun(projectID, worktreeID, daemonID, db.Completed())
}

func (f *moveFixture) chatWithRun(projectID, worktreeID string, daemonID *string, status db.WorkflowStatus) string {
	f.t.Helper()
	chatID := uuid.NewString()
	require.NoError(f.t, f.repo.CreateChat(f.ctx, &db.Chat{
		ID: chatID, UserID: moveUser, Title: "Moving chat", ProjectID: projectID, WorktreeID: &worktreeID,
		WorkflowID: &chatID, ActiveDaemonID: daemonID, State: db.ChatStateIdle, CreatedAt: f.now, UpdatedAt: f.now,
	}))
	_, _, _, err := threads.NewService(f.repo).CreateWorkflowWithThread(f.ctx, threads.CreateWorkflowWithThreadOpts{
		Workflow: &db.Workflow{
			ID: chatID, ChatID: chatID, WorkflowName: "builtin://agent",
			Thread: chatID, Status: status, CreatedAt: f.now,
		},
		ThreadID: chatID,
		ChatID:   chatID,
	})
	require.NoError(f.t, err)
	return chatID
}

func (f *moveFixture) move(req *reliantv1.MoveChatToProjectRequest) (*reliantv1.Chat, error) {
	f.t.Helper()
	resp, err := f.service.MoveChatToProject(f.ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return resp.Msg.Chat, nil
}

func (f *moveFixture) row(chatID string) *db.Chat {
	f.t.Helper()
	chat, err := f.repo.GetChat(f.ctx, chatID)
	require.NoError(f.t, err)
	return chat
}

// hiddenNotes are the hidden system messages on the chat's root thread.
func (f *moveFixture) hiddenNotes(chatID string) []string {
	f.t.Helper()
	thread := chatID
	messages, err := f.repo.ListMessages(f.ctx, chatID, db.MessageListOptions{Thread: &thread})
	require.NoError(f.t, err)
	var notes []string
	for _, m := range messages {
		if m.Role != reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM || m.DisplayStyle == nil ||
			*m.DisplayStyle != reliantv1.DisplayStyle_DISPLAY_STYLE_HIDDEN {
			continue
		}
		blocks, err := f.repo.ListContentBlocks(f.ctx, m.ID)
		require.NoError(f.t, err)
		require.Len(f.t, blocks, 1)
		require.NotNil(f.t, blocks[0].Content)
		notes = append(notes, *blocks[0].Content)
	}
	return notes
}

func requireCode(t *testing.T, err error, code connect.Code) {
	t.Helper()
	require.Error(t, err)
	assert.Equal(t, code, svcerr.ToConnect(err).Code(), "error: %v", err)
}

func TestMoveChatToProject_MovesChatPlansAndMachineAndLeavesANote(t *testing.T) {
	t.Parallel()
	f := newMoveFixture(t)
	from, _ := f.project(moveUser, "billing")
	fromMain := f.worktree(from, "billing-main-row", "/created/billing", true)
	to, toMain := f.project(moveUser, "properties")
	laptop := f.machine(moveUser, "laptop")
	f.install(from, laptop, "/home/me/billing")
	f.install(to, laptop, "/home/me/properties")
	chatID := f.chat(from, fromMain, &laptop)

	planProject := from
	require.NoError(t, f.repo.CreatePlan(f.ctx, &db.Plan{
		ID: uuid.NewString(), ThreadID: chatID, Title: "the plan", Status: 1, ProjectID: &planProject,
		CreatedAt: f.now, UpdatedAt: f.now,
	}))

	moved, err := f.move(&reliantv1.MoveChatToProjectRequest{ChatId: chatID, ProjectId: to})
	require.NoError(t, err)
	assert.Equal(t, to, moved.ProjectId)

	row := f.row(chatID)
	assert.Equal(t, to, row.ProjectID)
	require.NotNil(t, row.WorktreeID)
	assert.Equal(t, toMain, *row.WorktreeID, "the chat runs in the new project's main checkout")
	require.NotNil(t, row.ActiveDaemonID)
	assert.Equal(t, laptop, *row.ActiveDaemonID, "the chat's machine has the new project, so it stays")

	plans, err := f.repo.ListPlansByChatID(f.ctx, chatID)
	require.NoError(t, err)
	require.Len(t, plans, 1)
	require.NotNil(t, plans[0].ProjectID)
	assert.Equal(t, to, *plans[0].ProjectID, "the chat's plans move with it")

	notes := f.hiddenNotes(chatID)
	require.Len(t, notes, 1)
	assert.Contains(t, notes[0], `from project "billing" to project "properties"`)
	assert.Contains(t, notes[0], "`/home/me/billing`", "the note names the old checkout the history's paths are in")
	assert.Contains(t, notes[0], "`/home/me/properties` on self-hosted machine "+laptop[:8], "and where the chat works now")
}

// noteFailingRepo is the test database with the move note's write failing.
type noteFailingRepo struct {
	Repository
}

func (noteFailingRepo) SaveMessageToThread(context.Context, string, string, int32, string, *string, []string, *int32) (*db.Message, error) {
	return nil, errors.New("note write refused")
}

// The note commits with the move or not at all: a chat moved without it
// would run its next turn on the old project's paths with no warning.
func TestMoveChatToProject_NoteFailureLeavesTheChatWhereItWas(t *testing.T) {
	t.Parallel()
	f := newMoveFixture(t)
	from, fromMain := f.project(moveUser, "billing")
	to, _ := f.project(moveUser, "properties")
	laptop := f.machine(moveUser, "laptop")
	f.install(from, laptop, "/home/me/billing")
	f.install(to, laptop, "/home/me/properties")
	chatID := f.chat(from, fromMain, &laptop)
	f.service.deps.Repo = noteFailingRepo{Repository: f.repo}

	_, err := f.move(&reliantv1.MoveChatToProjectRequest{ChatId: chatID, ProjectId: to})
	requireCode(t, err, connect.CodeInternal)

	row := f.row(chatID)
	assert.Equal(t, from, row.ProjectID, "the move rolled back with the note")
	require.NotNil(t, row.WorktreeID)
	assert.Equal(t, fromMain, *row.WorktreeID)
	assert.Empty(t, f.hiddenNotes(chatID))
}

func TestMoveChatToProject_SameProjectIsANoOp(t *testing.T) {
	t.Parallel()
	f := newMoveFixture(t)
	project, main := f.project(moveUser, "billing")
	chatID := f.chat(project, main, nil)

	moved, err := f.move(&reliantv1.MoveChatToProjectRequest{ChatId: chatID, ProjectId: project})
	require.NoError(t, err)
	assert.Equal(t, project, moved.ProjectId)
	assert.Empty(t, f.hiddenNotes(chatID), "nothing moved, so nothing to tell the model")
}

func TestMoveChatToProject_RefusesWhatTheCallerDoesNotOwn(t *testing.T) {
	t.Parallel()
	f := newMoveFixture(t)
	mine, mineMain := f.project(moveUser, "mine")
	theirs, theirsMain := f.project("someone-else", "theirs")
	chatID := f.chat(mine, mineMain, nil)

	_, err := f.move(&reliantv1.MoveChatToProjectRequest{ChatId: chatID, ProjectId: theirs})
	requireCode(t, err, connect.CodeNotFound)
	assert.Equal(t, mine, f.row(chatID).ProjectID)

	// Another user's chat reads as not found, even into a project of mine.
	other := uuid.NewString()
	require.NoError(t, f.repo.CreateChat(f.ctx, &db.Chat{
		ID: other, UserID: "someone-else", Title: "theirs", ProjectID: theirs, WorktreeID: &theirsMain,
		State: db.ChatStateIdle, CreatedAt: f.now, UpdatedAt: f.now,
	}))
	_, err = f.move(&reliantv1.MoveChatToProjectRequest{ChatId: other, ProjectId: mine})
	requireCode(t, err, connect.CodeNotFound)
	assert.Equal(t, theirs, f.row(other).ProjectID)
}

func TestMoveChatToProject_RefusesAChatWithARunInProgress(t *testing.T) {
	t.Parallel()
	f := newMoveFixture(t)
	from, fromMain := f.project(moveUser, "billing")
	to, toMain := f.project(moveUser, "properties")
	chatID := f.chatWithRun(from, fromMain, nil, db.Active())

	_, err := f.move(&reliantv1.MoveChatToProjectRequest{ChatId: chatID, ProjectId: to})
	requireCode(t, err, connect.CodeFailedPrecondition)
	assert.Contains(t, err.Error(), "running")
	assert.Equal(t, from, f.row(chatID).ProjectID)
	assert.Empty(t, f.hiddenNotes(chatID))

	// The write re-checks the activity itself, so a run that starts after
	// the handler read the chat still wins.
	moved, err := f.repo.MoveChatToProject(f.ctx, db.ChatMove{
		UserID: moveUser, ChatID: chatID, FromProjectID: from, ToProjectID: to, WorktreeID: toMain,
	})
	require.NoError(t, err)
	assert.False(t, moved)
	assert.Equal(t, from, f.row(chatID).ProjectID)
}

func TestMoveChatToProject_BranchWorkspaceNeedsLeaveWorkspace(t *testing.T) {
	t.Parallel()
	f := newMoveFixture(t)
	from, _ := f.project(moveUser, "billing")
	branch := f.worktree(from, "fix-invoices", "/home/me/.reliant/worktrees/fix-invoices", false)
	to, toMain := f.project(moveUser, "properties")
	chatID := f.chat(from, branch, nil)

	_, err := f.move(&reliantv1.MoveChatToProjectRequest{ChatId: chatID, ProjectId: to})
	requireCode(t, err, connect.CodeFailedPrecondition)
	assert.Contains(t, err.Error(), `workspace "fix-invoices"`)
	row := f.row(chatID)
	assert.Equal(t, from, row.ProjectID)
	assert.Equal(t, branch, *row.WorktreeID)

	_, err = f.move(&reliantv1.MoveChatToProjectRequest{ChatId: chatID, ProjectId: to, LeaveWorkspace: true})
	require.NoError(t, err)
	row = f.row(chatID)
	assert.Equal(t, to, row.ProjectID)
	assert.Equal(t, toMain, *row.WorktreeID, "the chat leaves the workspace for the new project's main checkout")

	notes := f.hiddenNotes(chatID)
	require.Len(t, notes, 1)
	assert.Contains(t, notes[0], `workspace "fix-invoices" (branch `+"`fix-invoices`"+`) at `+"`/home/me/.reliant/worktrees/fix-invoices`")
	assert.Contains(t, notes[0], "That workspace stayed with its project")
}

func TestMoveChatToProject_PicksAMachineThatHasTheProject(t *testing.T) {
	t.Parallel()

	t.Run("a named machine must have the project", func(t *testing.T) {
		t.Parallel()
		f := newMoveFixture(t)
		from, fromMain := f.project(moveUser, "billing")
		to, _ := f.project(moveUser, "properties")
		laptop, desktop := f.machine(moveUser, "laptop"), f.machine(moveUser, "desktop")
		f.install(to, desktop, "/srv/properties")
		chatID := f.chat(from, fromMain, &laptop)

		_, err := f.move(&reliantv1.MoveChatToProjectRequest{ChatId: chatID, ProjectId: to, DaemonId: &laptop})
		requireCode(t, err, connect.CodeFailedPrecondition)
		assert.Equal(t, from, f.row(chatID).ProjectID)

		_, err = f.move(&reliantv1.MoveChatToProjectRequest{ChatId: chatID, ProjectId: to, DaemonId: &desktop})
		require.NoError(t, err)
		assert.Equal(t, desktop, *f.row(chatID).ActiveDaemonID)
	})

	t.Run("the chat's machine without the project gives way to one with it", func(t *testing.T) {
		t.Parallel()
		f := newMoveFixture(t)
		from, fromMain := f.project(moveUser, "billing")
		to, _ := f.project(moveUser, "properties")
		laptop, desktop := f.machine(moveUser, "laptop"), f.machine(moveUser, "desktop")
		f.install(from, laptop, "/home/me/billing")
		f.install(to, desktop, "/srv/properties")
		chatID := f.chat(from, fromMain, &laptop)

		_, err := f.move(&reliantv1.MoveChatToProjectRequest{ChatId: chatID, ProjectId: to})
		require.NoError(t, err)
		row := f.row(chatID)
		require.NotNil(t, row.ActiveDaemonID)
		assert.Equal(t, desktop, *row.ActiveDaemonID)
		notes := f.hiddenNotes(chatID)
		require.Len(t, notes, 1)
		assert.Contains(t, notes[0], "`/srv/properties` on self-hosted machine "+desktop[:8])
	})

	t.Run("a project installed nowhere keeps the chat's machine", func(t *testing.T) {
		t.Parallel()
		f := newMoveFixture(t)
		from, fromMain := f.project(moveUser, "billing")
		to, _ := f.project(moveUser, "properties")
		laptop := f.machine(moveUser, "laptop")
		chatID := f.chat(from, fromMain, &laptop)

		_, err := f.move(&reliantv1.MoveChatToProjectRequest{ChatId: chatID, ProjectId: to})
		require.NoError(t, err)
		row := f.row(chatID)
		require.NotNil(t, row.ActiveDaemonID)
		assert.Equal(t, laptop, *row.ActiveDaemonID)
	})

	t.Run("a project still being set up everywhere is refused", func(t *testing.T) {
		t.Parallel()
		f := newMoveFixture(t)
		from, fromMain := f.project(moveUser, "billing")
		to, _ := f.project(moveUser, "properties")
		laptop := f.machine(moveUser, "laptop")
		require.NoError(t, f.repo.UpsertQueuedProjectDaemon(f.ctx, to, laptop, "/srv/properties", nil, uuid.NewString()))
		desktop := f.machine(moveUser, "desktop")
		chatID := f.chat(from, fromMain, &desktop)

		_, err := f.move(&reliantv1.MoveChatToProjectRequest{ChatId: chatID, ProjectId: to})
		requireCode(t, err, connect.CodeFailedPrecondition)
		assert.Equal(t, from, f.row(chatID).ProjectID)
	})
}

func TestMoveNote_NoCheckoutOnTheNewMachine(t *testing.T) {
	t.Parallel()
	note := moveNote(&db.Project{Name: "billing"}, movePlace{path: "/home/me/billing"},
		&db.Project{Name: "properties"}, movePlace{})
	assert.True(t, strings.HasPrefix(note, `This conversation was moved from project "billing" to project "properties".`))
	assert.Contains(t, note, "not set up on this chat's machine yet")
}
