// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"slices"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/testsuite"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	types "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/reliant-labs/reliant/internal/workflow/threadwake"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// What happens to a user message that reaches a thread with NO background
// spawns while the run's last turn is in flight — after that turn read its
// mailbox and history, before the loop-exit gate?
//
// A user reaches a running thread two ways, and both ring the thread-wake
// doorbell:
//
//   - SendMessage on an active run (chat_send.go, the db.Active() branch)
//     saves the message straight into the thread's history, then rings it
//     with ReasonUserMessage.
//   - SendAgentMessage, which is what the composer's queue sends while the
//     agent is streaming (ChatInput.handleQueue), writes an agent_messages
//     row, then rings it with ReasonMailbox.
//
// THESE TESTS PIN A PRE-EXISTING BUG; they do not endorse it. Neither path
// gets a turn in this run unless the message is a mailbox row that lands
// before the turn's pending_inbox probe. The doorbell is counted
// (ChildWorkflowTracker.threadWakes) but read only by the loop-exit gate's
// live-spawn branch: with nothing live, awaitLiveDetachedSpawnsOrHandoff
// returns before it looks. That is true of main's gate and of the gate that
// measures from the turn's start (#564): both exit here. A fix that lets a
// mid-turn wake re-enter the loop must flip the two "stranded" assertions.
//
// After the run ends, the history message waits for the user's next send,
// whose fresh run reads it. The mailbox row waits for the same thing, but the
// completed run has stamped the thread terminal
// (CascadeTerminalStatusToThreadSubtree), so the reconciler's
// resolveOrphanedAgentMessages marks the row undelivered on its next pass
// (every 30s) unless that send comes first.

// lateUserMessageEnv is the spawn e2e env with a thread's history and
// mailbox modelled, so a turn records what it could see. Nothing in its
// script spawns.
type lateUserMessageEnv struct {
	*spawnE2EEnv
	mailbox *fakeMailbox

	// history is each thread's saved user messages (SaveMessageToThread),
	// and readByTurn is what each of a thread's turns found there when it
	// read history. Guarded by spawnE2EEnv.mu.
	history    map[string][]string
	readByTurn map[string][][]string

	// midTurn runs during the parent's turn N, after it drained its mailbox
	// and read history and before its pending_inbox probe. afterProbe runs
	// after the probe, before the turn returns: the last moment before the
	// loop-exit gate.
	midTurn    map[int]func(thread string)
	afterProbe map[int]func(thread string)
}

func newLateUserMessageEnv(t *testing.T, env *testsuite.TestWorkflowEnvironment) *lateUserMessageEnv {
	t.Helper()
	// One turn with nothing to do: it is the run's last.
	base := newSpawnE2EEnv(t, env, []scriptedToolCallsResponse{{toolCalls: nil}})
	e := &lateUserMessageEnv{
		spawnE2EEnv: base,
		mailbox:     &fakeMailbox{queued: map[string][]string{}, delivered: map[string][]string{}},
		history:     map[string][]string{},
		readByTurn:  map[string][][]string{},
	}

	// agent.yaml's pending_inbox while-condition (see spawnReportUnreadYAML).
	wf, err := wfyaml.ParseWorkflow([]byte(spawnReportUnreadYAML))
	require.NoError(t, err)
	wfJSON, err := protojson.Marshal(wf)
	require.NoError(t, err)
	env.RegisterActivityWithOptions(
		func(_ context.Context, _ map[string]string) (LoadedWorkflow, error) {
			return LoadedWorkflow{WorkflowJSON: wfJSON}, nil
		},
		activity.RegisterOptions{Name: "ActivityLoadWorkflow"},
	)
	env.RegisterActivityWithOptions(
		func(ctx context.Context, input types.ActivityInput) (map[string]interface{}, error) {
			return e.callLLM(ctx, input)
		},
		activity.RegisterOptions{Name: "CallLLM"},
	)
	return e
}

// callLLM is CallLLMActivity's shape: drain the mailbox, read history, take
// the turn, then probe the mailbox for pending_inbox.
func (e *lateUserMessageEnv) callLLM(ctx context.Context, input types.ActivityInput) (map[string]interface{}, error) {
	thread := input.Runtime.Thread
	e.mailbox.drain(thread)
	e.mu.Lock()
	e.readByTurn[thread] = append(e.readByTurn[thread], slices.Clone(e.history[thread]))
	e.mu.Unlock()

	out, err := e.callLLMStub(ctx, input)
	if err != nil {
		return nil, err
	}

	e.mu.Lock()
	turn := e.callLLMByThread[thread]
	isParent := thread == e.parentThread
	midTurn, afterProbe := e.midTurn[turn], e.afterProbe[turn]
	e.mu.Unlock()
	if isParent && midTurn != nil {
		midTurn(thread)
	}
	out["pending_inbox"] = e.mailbox.hasQueued(thread)
	if isParent && afterProbe != nil {
		afterProbe(thread)
	}
	return out, nil
}

// sendMessage is SendMessage on an active run: the message is saved into the
// thread's history, then the doorbell rings.
func (e *lateUserMessageEnv) sendMessage(thread, text string) {
	e.mu.Lock()
	e.history[thread] = append(e.history[thread], text)
	e.mu.Unlock()
	e.env.SignalWorkflow(ThreadWakeSignalName, ThreadWakeSignal{Thread: thread, Reason: threadwake.ReasonUserMessage})
}

// queueMessage is SendAgentMessage: an agent_messages row, then the doorbell.
func (e *lateUserMessageEnv) queueMessage(thread, text string) {
	e.mailbox.enqueue(thread, text)
	e.env.SignalWorkflow(ThreadWakeSignalName, ThreadWakeSignal{Thread: thread, Reason: threadwake.ReasonMailbox})
}

// readInThisRun reports whether any of the parent's turns saw text, in
// history or delivered from its mailbox.
func (e *lateUserMessageEnv) readInThisRun(text string) bool {
	delivered, _ := e.mailbox.snapshot(e.parentThread)
	if slices.Contains(delivered, text) {
		return true
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, seen := range e.readByTurn[e.parentThread] {
		if slices.Contains(seen, text) {
			return true
		}
	}
	return false
}

type LateUserMessageE2ESuite struct {
	suite.Suite
	temporaltest.WorkflowTestSuite
}

func TestLateUserMessageE2E(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(LateUserMessageE2ESuite))
}

// PINNED BUG. SendMessage lands while the run's last turn is in flight. The
// probe looks only at the mailbox, so nothing in the turn sees a history row,
// and the gate ignores the doorbell with nothing live: the run ends with the
// message saved and unanswered. It is read only when the user sends again.
func (s *LateUserMessageE2ESuite) TestSendMessageDuringTheLastTurnIsNotReadByThisRun() {
	env := s.NewTestWorkflowEnvironment()
	e := newLateUserMessageEnv(s.T(), env)
	const msg = "one more thing"
	e.midTurn = map[int]func(string){1: func(thread string) { e.sendMessage(thread, msg) }}

	e.execute("chat-late-send")

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	e.mu.Lock()
	saved := slices.Clone(e.history[e.parentThread])
	e.mu.Unlock()
	s.Require().Equal([]string{msg}, saved, "the message was saved to the thread")
	s.Require().False(e.readInThisRun(msg),
		"STRANDED (pinned): the run ended without a turn that read the user's message; parent took %d turns", e.parentTurns())
	s.Require().Equal(1, e.parentTurns())
}

// A queued message that lands before the last turn's probe is caught by
// pending_inbox, and the next turn delivers it. Not a bug: the control for
// the case below.
func (s *LateUserMessageE2ESuite) TestQueuedMessageBeforeTheProbeIsDeliveredByThisRun() {
	env := s.NewTestWorkflowEnvironment()
	e := newLateUserMessageEnv(s.T(), env)
	const msg = "queued mid-turn"
	e.midTurn = map[int]func(string){1: func(thread string) { e.queueMessage(thread, msg) }}

	e.execute("chat-late-queue-before-probe")

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	s.Require().True(e.readInThisRun(msg), "pending_inbox must buy the turn that delivers it")
	s.Require().Equal(2, e.parentTurns())
}

// PINNED BUG. The same queued message, landing after the probe. Nothing
// reads the mailbox again, and the gate ignores its doorbell with nothing
// live: the run ends with the row still queued.
func (s *LateUserMessageE2ESuite) TestQueuedMessageAfterTheProbeIsLeftQueued() {
	env := s.NewTestWorkflowEnvironment()
	e := newLateUserMessageEnv(s.T(), env)
	const msg = "queued after the probe"
	e.afterProbe = map[int]func(string){1: func(thread string) { e.queueMessage(thread, msg) }}

	e.execute("chat-late-queue-after-probe")

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	_, queued := e.mailbox.snapshot(e.parentThread)
	s.Require().Equal([]string{msg}, queued,
		"STRANDED (pinned): the run ended with the user's message still queued in the mailbox; parent took %d turns", e.parentTurns())
	s.Require().False(e.readInThisRun(msg))
	s.Require().Equal(1, e.parentTurns())
}
