// Copyright (c) 2025 Reliant Labs
package launch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/threads"
)

// stubPinner stands in for daemonplacement.Pinner: it targets one machine and
// writes through the repo, like the real one.
type stubPinner struct {
	repo   *db.Repo
	target string
	pinned []string
}

func (p *stubPinner) Target(context.Context, *db.Chat) string { return p.target }

func (p *stubPinner) Pin(ctx context.Context, chat *db.Chat) {
	if chat.ActiveDaemonID != nil && *chat.ActiveDaemonID != "" {
		return
	}
	stored, err := p.repo.PinChatDaemonIfUnset(ctx, chat.ID, p.target)
	if err != nil || stored == "" {
		return
	}
	chat.ActiveDaemonID = &stored
	p.pinned = append(p.pinned, chat.ID)
}

// A chat that names no machine is created already pinned to the one the pinner
// chooses, and its run carries session_daemon_id: this is what keeps it on that
// machine when another connects (research/CHAT_MACHINE_PLACEMENT.md).
func TestLaunchPinsANewChatThatNamesNoMachine(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	starter := &fakeStarter{}
	pinner := &stubPinner{repo: repo, target: "vm"}
	launcher := NewLauncher(repo, threads.NewService(repo), starter, &recordingRunRecorder{repo: repo}, "q", WithChatPinner(pinner))

	result, err := launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "builtin://agent",
		Params: mockModelParams(t), Messages: userSeed("hello"),
	})
	require.NoError(t, err)

	chat, err := repo.GetChat(ctx, result.Chat.ID)
	require.NoError(t, err)
	require.NotNil(t, chat.ActiveDaemonID)
	assert.Equal(t, "vm", *chat.ActiveDaemonID)
	_, input := starter.rootRun(t)
	assert.Equal(t, "vm", input.Inputs["session_daemon_id"])
}

// A machine the caller chose wins over the pinner, and a no-machine chat is
// never placed.
func TestLaunchPinnerNeverOverridesAChoiceOrNoMachine(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)

	t.Run("a chosen daemon", func(t *testing.T) {
		starter := &fakeStarter{}
		launcher := NewLauncher(repo, threads.NewService(repo), starter, &recordingRunRecorder{repo: repo}, "q",
			WithChatPinner(&stubPinner{repo: repo, target: "vm"}))
		result, err := launcher.Launch(ctx, chatStartEvent(), Spec{
			OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "builtin://agent",
			Params: mockModelParams(t), Messages: userSeed("hi"), DaemonID: "chosen",
		})
		require.NoError(t, err)
		chat, err := repo.GetChat(ctx, result.Chat.ID)
		require.NoError(t, err)
		assert.Equal(t, "chosen", *chat.ActiveDaemonID)
	})

	t.Run("no machine", func(t *testing.T) {
		starter := &fakeStarter{}
		pinner := &stubPinner{repo: repo, target: "vm"}
		launcher := NewLauncher(repo, threads.NewService(repo), starter, &recordingRunRecorder{repo: repo}, "q", WithChatPinner(pinner))
		result, err := launcher.Launch(ctx, chatStartEvent(), Spec{
			OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "builtin://agent",
			Params: mockModelParams(t), Messages: userSeed("hi"), NoMachine: true,
		})
		require.NoError(t, err)
		chat, err := repo.GetChat(ctx, result.Chat.ID)
		require.NoError(t, err)
		assert.Nil(t, chat.ActiveDaemonID)
		assert.True(t, chat.NoMachine)
		_, input := starter.rootRun(t)
		assert.NotContains(t, input.Inputs, "session_daemon_id")
	})
}

// Without a pinner a launch pins nothing: the OSS / test default.
func TestLaunchWithoutAPinnerLeavesTheChatUnpinned(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)
	result, err := launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID: launchTestUserID, ProjectID: projectID, Workflow: "builtin://agent",
		Params: mockModelParams(t), Messages: userSeed("hi"),
	})
	require.NoError(t, err)
	chat, err := repo.GetChat(ctx, result.Chat.ID)
	require.NoError(t, err)
	assert.Nil(t, chat.ActiveDaemonID)
}
