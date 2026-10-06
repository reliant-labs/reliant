// Copyright (c) 2025 Reliant Labs
package launch

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/threads"

	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// rendezvousStarter makes the root start and the title start each wait for the
// other to be issued. Issued one after the other, the first waits for a second
// that never comes and fails; issued concurrently, both proceed.
type rendezvousStarter struct {
	repo db.Repository

	rootEntered  chan struct{}
	titleEntered chan struct{}

	mu sync.Mutex
	// titleSawChat records whether the chat row existed when the title start
	// was issued: GenerateTitleWorkflow reads it, so the title may run
	// alongside the root start but never ahead of the commit.
	titleSawChat bool
	titleCalls   int
	rootCalls    int
	// failTitle makes the title start fail, to pin that it stays best-effort.
	failTitle bool
}

func newRendezvousStarter(repo db.Repository) *rendezvousStarter {
	return &rendezvousStarter{
		repo:         repo,
		rootEntered:  make(chan struct{}),
		titleEntered: make(chan struct{}),
	}
}

const rendezvousWait = 2 * time.Second

func (s *rendezvousStarter) ExecuteWorkflow(
	ctx context.Context, options client.StartWorkflowOptions, _ interface{}, args ...interface{},
) (client.WorkflowRun, error) {
	if _, isRoot := args[0].(v2.WorkflowInput); isRoot {
		s.mu.Lock()
		s.rootCalls++
		s.mu.Unlock()
		close(s.rootEntered)
		select {
		case <-s.titleEntered:
		case <-time.After(rendezvousWait):
			return nil, errors.New("the title start was not issued while the root start was in flight")
		}
		return &fakeWorkflowRun{id: options.ID, runID: "run-" + options.ID}, nil
	}

	chatID, _ := args[0].(map[string]interface{})["chat_id"].(string)
	_, chatErr := s.repo.GetChat(ctx, chatID)
	s.mu.Lock()
	s.titleCalls++
	s.titleSawChat = chatErr == nil
	s.mu.Unlock()
	close(s.titleEntered)
	select {
	case <-s.rootEntered:
	case <-time.After(rendezvousWait):
		return nil, errors.New("the root start was not issued while the title start was in flight")
	}
	if s.failTitle {
		return nil, errors.New("title queue unavailable")
	}
	return &fakeWorkflowRun{id: options.ID, runID: "run-" + options.ID}, nil
}

// The two Temporal starts a new chat makes are independent round trips, both
// after the commit. Issuing them one after the other put the title's round trip
// on every StartChat for nothing.
func TestLaunchStartsTitleConcurrentlyWithRoot(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	starter := newRendezvousStarter(repo)
	launcher := NewLauncher(repo, threads.NewService(repo), starter, &recordingRunRecorder{repo: repo}, "test-task-queue")

	result, err := launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID:   launchTestUserID,
		ProjectID:     projectID,
		Workflow:      "builtin://agent",
		Params:        mockModelParams(t),
		Messages:      userSeed("hello"),
		GenerateTitle: true,
	})
	require.NoError(t, err, "the root and title starts must be in flight together")
	require.NotNil(t, result.Chat)

	starter.mu.Lock()
	defer starter.mu.Unlock()
	assert.Equal(t, 1, starter.rootCalls)
	assert.Equal(t, 1, starter.titleCalls)
	assert.True(t, starter.titleSawChat,
		"the title start must only be issued once the chat row is committed")
}

// The title is best-effort: its start failing must not fail a launch whose run
// Temporal accepted.
func TestLaunchSucceedsWhenTitleStartFails(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	starter := newRendezvousStarter(repo)
	starter.failTitle = true
	launcher := NewLauncher(repo, threads.NewService(repo), starter, &recordingRunRecorder{repo: repo}, "test-task-queue")

	result, err := launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID:   launchTestUserID,
		ProjectID:     projectID,
		Workflow:      "builtin://agent",
		Params:        mockModelParams(t),
		Messages:      userSeed("hello"),
		GenerateTitle: true,
	})
	require.NoError(t, err)

	root, err := repo.GetWorkflow(ctx, result.WorkflowID)
	require.NoError(t, err)
	assert.Equal(t, db.Active(), root.Status, "the run Temporal accepted is marked active regardless of the title")
}

// The root start's failure is still the launch's failure, whatever the title
// start did alongside it.
func TestLaunchFailsWhenRootStartFailsEvenIfTitleStarts(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	starter := &fakeStarter{failRootStarts: 1}
	launcher, _ := newTestLauncher(t, repo, starter)

	_, err := launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID:   launchTestUserID,
		ProjectID:     projectID,
		Workflow:      "builtin://agent",
		Params:        mockModelParams(t),
		Messages:      userSeed("hello"),
		GenerateTitle: true,
	})
	require.ErrorIs(t, err, ErrInternal)
}

// The launcher has no daemon dependency at all — the greenfield probe used to
// make every new chat wait on a round trip to the user's machine. What a
// launch does instead is hand the question to the run: the request path costs
// a transaction and a Temporal start, and seeds exactly what the caller asked
// for. (internal/grpc/services pins the same with a daemon that never answers.)
func TestLaunchHandsTheGreenfieldProbeToTheRun(t *testing.T) {
	repo, ctx, projectID, _ := launchFixture(t)
	starter := &fakeStarter{}
	launcher, _ := newTestLauncher(t, repo, starter)

	result, err := launcher.Launch(ctx, chatStartEvent(), Spec{
		OwnerUserID:     launchTestUserID,
		ProjectID:       projectID,
		Workflow:        "builtin://agent",
		Params:          mockModelParams(t),
		Messages:        userSeed("build me a landing page"),
		GreenfieldProbe: true,
	})
	require.NoError(t, err)

	_, input := starter.rootRun(t)
	assert.True(t, input.GreenfieldProbe, "the run, not the launch, probes the first turn")

	rootThread := result.Chat.ID
	messages, err := repo.ListMessages(ctx, result.Chat.ID, db.MessageListOptions{Thread: &rootThread, Limit: 10})
	require.NoError(t, err)
	require.Len(t, messages, 1, "the launch seeds only what was asked for; the run adds any guidance")
	assert.Equal(t, reliantv1.MessageRole_MESSAGE_ROLE_USER, messages[0].Role)
}

func TestLaunchLeavesTheGreenfieldProbeOffUnlessAsked(t *testing.T) {
	for name, spec := range map[string]Spec{
		"not asked":  {},
		"no machine": {GreenfieldProbe: true, NoMachine: true},
	} {
		t.Run(name, func(t *testing.T) {
			repo, ctx, projectID, _ := launchFixture(t)
			starter := &fakeStarter{}
			launcher, _ := newTestLauncher(t, repo, starter)

			spec.OwnerUserID = launchTestUserID
			spec.ProjectID = projectID
			spec.Workflow = "builtin://agent"
			spec.Params = mockModelParams(t)
			spec.Messages = userSeed("hello")
			_, err := launcher.Launch(ctx, chatStartEvent(), spec)
			require.NoError(t, err)

			_, input := starter.rootRun(t)
			assert.False(t, input.GreenfieldProbe)
		})
	}
}
