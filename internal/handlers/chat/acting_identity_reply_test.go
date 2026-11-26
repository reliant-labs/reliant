// Copyright (c) 2025 Reliant Labs
package chat

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"

	"github.com/reliant-labs/reliant/internal/agentmailbox"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/questions"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/store"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// An automation's run speaks as the bot; a person who replies in its chat does
// not. The ended run's own inputs carry the bot identity (that is what the run
// was launched with), and a person's continuation must not inherit it, exactly
// as it does not inherit the launch marker.
func TestContinueQueued_APersonsRunNeverInheritsTheBotIdentity(t *testing.T) {
	t.Parallel()

	f := newContinueFixture(t, false)
	f.temporal.lastInputs[v2.InputKeyActingIdentity] = "app"
	f.temporal.lastInputs[v2.InputKeyAutomation] = "pr-review"
	f.temporal.lastInputs[v2.InputKeyUnattended] = true
	require.NoError(t, setupQueued(t, f))

	require.Equal(t, 1, f.continueConcurrently(t, 1))
	started := f.temporal.runsStarted()
	require.Len(t, started, 1)
	inputs := started[0].input.Inputs
	assert.NotContains(t, inputs, v2.InputKeyActingIdentity, "a person's turn acts as the person")
	assert.NotContains(t, inputs, v2.InputKeyAutomation)
	assert.NotContains(t, inputs, v2.InputKeyLaunchRun)
}

func setupQueued(t *testing.T, f continueFixture) error {
	t.Helper()
	_, err := f.repo.SetChatQueuedForMachine(f.ctx, f.chatID, true)
	return err
}

// The same rule for a person's message to a finished automation chat.
func TestSendMessage_RestartOfAnAutomationChatDropsTheBotIdentity(t *testing.T) {
	t.Parallel()

	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Completed())

	temporal := &inputCapturingTemporalClient{
		absorbTestTemporalClient: absorbTestTemporalClient{exists: true, status: enums.WORKFLOW_EXECUTION_STATUS_COMPLETED},
	}
	service := &Service{deps: Deps{Repo: repo, Workflows: store.NewWorkflowExtStore(repo.ORM()), Worktrees: store.NewWorktreeExtStore(repo.ORM()), ProjectRows: store.NewProjectExtStore(repo.ORM()), TriggerEvents: store.NewTriggerEventExtStore(repo.ORM()), Questions: store.NewQuestionExtStore(repo.ORM()), Spawns: store.NewToolCallExtStore(repo.ORM()), Selections: store.NewChatSelectionExtStore(repo.ORM()), Messages: store.NewMessageExtStore(repo.ORM()), ToolBlocks: store.NewMessageContentBlockExtStore(repo.ORM()), Senders: store.NewThreadExtStore(repo.ORM()), Temporal: temporal, Mailbox: agentmailbox.New(agentmailbox.Deps{Store: store.NewAgentMessageExtStore(repo.ORM()), Announcer: repo.UpdateWriter()}), AgentMessages: store.NewAgentMessageExtStore(repo.ORM())}, runs: runs.New(runs.Deps{Repo: repo, Resolver: questions.NewFromRepo(repo), Chats: store.NewChatsWithActivityExtStore(repo.ORM()), Workflows: store.NewWorkflowExtStore(repo.ORM()), Questions: store.NewQuestionExtStore(repo.ORM()), Temporal: temporal, Pause: nil})}

	_, err := service.SendMessage(ctx, sendMessageRequest(t, fx.chatID, "one more thing"))
	require.NoError(t, err)
	require.Len(t, temporal.inputs, 1)
	assert.NotContains(t, temporal.inputs[0].Inputs, v2.InputKeyActingIdentity)
	assert.NotContains(t, temporal.inputs[0].Inputs, v2.InputKeyAutomation)
}
