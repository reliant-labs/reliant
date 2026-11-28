// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/agentmailbox"
	"github.com/reliant-labs/reliant/internal/auth"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/ptr"
	"github.com/reliant-labs/reliant/internal/questions"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/threads"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/toolexec/toolexectest"
)

// placingRouter is the router of a user whose cloud VM has the project and
// whose laptop has just connected: default resolution prefers the laptop
// (connected and local), an id selector resolves the machine it names.
type placingRouter struct {
	*wakeRecordingRouter
	defaultID string
	known     map[string]bool
}

func (r *placingRouter) PlacementDaemonID(_ context.Context, _ string, daemonID string) (string, error) {
	if daemonID == "" {
		return r.defaultID, nil
	}
	if r.known[daemonID] {
		return daemonID, nil
	}
	return "", toolexec.ErrNoDaemon
}

// The reported incident: a chat started with the default machine, on a VM with
// the project installed. The laptop then connects as the router's preferred
// default, but has no install of the project. The chat must be pinned to the
// VM before its first run, woken there, and must still be on the VM for its
// next run.
func TestUnpinnedChatStaysOnTheMachineThatHasTheProjectWhenALaptopConnects(t *testing.T) {
	e := newStartDaemonEnv(t)
	vm := e.daemon(t, "test-user")
	laptop := e.daemon(t, "test-user")
	require.NoError(t, e.repo.UpsertProjectDaemon(context.Background(), e.projectID, vm, "/work/project", nil))
	installed, err := e.repo.MarkProjectDaemonInstalledInScope(context.Background(), e.projectID, vm, nil)
	require.NoError(t, err)
	require.True(t, installed)

	router := &placingRouter{wakeRecordingRouter: e.router, defaultID: laptop, known: map[string]bool{vm: true, laptop: true}}
	e.svc.deps.Router = router

	// First run: StartChat with the default machine picker value.
	resp, err := e.start(t, nil)
	require.NoError(t, err)
	chatID := resp.Msg.GetChat().GetId()

	chat, err := store.GetChat(e.ctx, e.repo.ORM(), chatID)
	require.NoError(t, err)
	require.NotNil(t, chat.ActiveDaemonID, "an unpinned start is pinned before its run")
	assert.Equal(t, vm, *chat.ActiveDaemonID, "the machine with the project, not the connected laptop")
	require.Len(t, e.temporal.inputs, 1)
	assert.Equal(t, vm, e.temporal.inputs[0].Inputs["session_daemon_id"])

	require.Len(t, e.router.selectors, 1, "one attended wake")
	require.NotNil(t, e.router.selectors[0], "the wake names a machine; default resolution would have woken the laptop")
	assert.Equal(t, vm, e.router.selectors[0].ID)
}

// A chat from before pinning (stored unpinned) restarts through SendMessage:
// its run lands on the machine that has the project, the pin is written, and a
// later run keeps it while the laptop is still the router's default.
func TestSendMessageToALegacyUnpinnedChatPinsItToTheMachineWithTheProject(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Completed())
	chat, err := store.GetChat(ctx, repo.ORM(), fx.chatID)
	require.NoError(t, err)
	require.Nil(t, chat.ActiveDaemonID, "precondition: a chat from before pinning")

	vm, laptop := uuid.NewString(), uuid.NewString()
	for _, id := range []string{vm, laptop} {
		require.NoError(t, repo.UpsertDaemon(ctx, &db.Daemon{ID: id, UserID: "test-user"}))
	}
	require.NoError(t, repo.UpsertProjectDaemon(ctx, chat.ProjectID, vm, "/work/project", nil))
	installed, err := repo.MarkProjectDaemonInstalledInScope(ctx, chat.ProjectID, vm, nil)
	require.NoError(t, err)
	require.True(t, installed)

	auth.SetUserJWT("test-user", "user-jwt")
	t.Cleanup(func() { auth.SetUserJWT("test-user", "") })
	temporal := &inputRecordingTemporal{}
	wake := &wakeRecordingRouter{Recorder: &toolexectest.Recorder{}}
	svc := &Service{
		deps: Deps{
			Repo:        repo,
			Workflows:   store.NewWorkflowExtStore(repo.ORM()),
			Worktrees:   store.NewWorktreeExtStore(repo.ORM()),
			ProjectRows: store.NewProjectExtStore(repo.ORM()), TriggerEvents: store.NewTriggerEventExtStore(repo.ORM()),
			Spawns: store.NewToolCallExtStore(repo.ORM()), Selections: store.NewChatSelectionExtStore(repo.ORM()), Mailbox: agentmailbox.New(agentmailbox.Deps{Store: store.NewAgentMessageExtStore(repo.ORM()), Announcer: repo.UpdateWriter()}), AgentMessages: store.NewAgentMessageExtStore(repo.ORM()), Messages: store.NewMessageExtStore(repo.ORM()), ToolBlocks: store.NewMessageContentBlockExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM()), Temporal: temporal,
			Router: &placingRouter{wakeRecordingRouter: wake, defaultID: laptop, known: map[string]bool{vm: true, laptop: true}},
		},
		threads: threads.New(threads.Deps{Repo: repo, Messages: store.NewMessageExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM())}),
		runs:    runs.New(runs.Deps{Repo: repo, Resolver: questions.NewFromRepo(repo), Chats: store.NewChatsWithActivityExtStore(repo.ORM()), Workflows: store.NewWorkflowExtStore(repo.ORM()), Questions: store.NewQuestionExtStore(repo.ORM()), Temporal: temporal, Pause: nil}),
	}

	_, err = svc.SendMessage(ctx, sendMessageRequest(t, fx.chatID, "continue"))
	require.NoError(t, err)

	stored, err := store.GetChat(ctx, repo.ORM(), fx.chatID)
	require.NoError(t, err)
	require.NotNil(t, stored.ActiveDaemonID)
	assert.Equal(t, vm, *stored.ActiveDaemonID)
	require.NotEmpty(t, temporal.inputs)
	assert.Equal(t, vm, temporal.inputs[len(temporal.inputs)-1].Inputs["session_daemon_id"])
	require.Len(t, wake.selectors, 1)
	require.NotNil(t, wake.selectors[0])
	assert.Equal(t, vm, wake.selectors[0].ID, "the send wakes the machine it pins, not the laptop")

	_, err = svc.SendMessage(ctx, sendMessageRequest(t, fx.chatID, "and again"))
	require.NoError(t, err)
	stored, err = store.GetChat(ctx, repo.ORM(), fx.chatID)
	require.NoError(t, err)
	assert.Equal(t, vm, *stored.ActiveDaemonID)
}

// With nothing recorded about where the project lives, default resolution
// still answers (a fresh project, or a user whose only machine hosts it).
func TestStartChatWithNoInstallsPinsTheRoutersDefault(t *testing.T) {
	e := newStartDaemonEnv(t)
	only := e.daemon(t, "test-user")
	e.svc.deps.Router = &placingRouter{wakeRecordingRouter: e.router, defaultID: only, known: map[string]bool{only: true}}

	resp, err := e.start(t, nil)
	require.NoError(t, err)

	chat, err := store.GetChat(e.ctx, e.repo.ORM(), resp.Msg.GetChat().GetId())
	require.NoError(t, err)
	require.NotNil(t, chat.ActiveDaemonID)
	assert.Equal(t, only, *chat.ActiveDaemonID)
}

// A chat with no machine by design is never pinned, whatever the router would
// answer.
func TestStartChatNoMachineIsNeverPinned(t *testing.T) {
	e := newStartDaemonEnv(t)
	only := e.daemon(t, "test-user")
	e.svc.deps.Router = &placingRouter{wakeRecordingRouter: e.router, defaultID: only, known: map[string]bool{only: true}}

	resp, err := e.start(t, func(r *reliantv1.StartChatRequest) { r.NoMachine = ptr.Of(true) })
	require.NoError(t, err)

	chat, err := store.GetChat(e.ctx, e.repo.ORM(), resp.Msg.GetChat().GetId())
	require.NoError(t, err)
	assert.Nil(t, chat.ActiveDaemonID)
	assert.True(t, chat.NoMachine)
}
