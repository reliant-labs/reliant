// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/reliant-labs/reliant/internal/temporal/temporaltest"
	types "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
	"github.com/reliant-labs/reliant/internal/workflow/threadwake"
	wfyaml "github.com/reliant-labs/reliant/internal/workflow/yaml"
)

// A user message that reaches a thread with NO background spawns while its run
// is finishing must not be stranded: either this run gives it a turn, or a run
// is started for it (late_wake.go).
//
// A user reaches a running thread two ways — SendMessage on an active run
// (chat_send.go, the db.Active() branch) and SendAgentMessage, which is what
// the composer's queue sends while the agent is streaming
// (ChatInput.handleQueue). Both queue an agent_messages row, then ring the
// thread-wake doorbell. SendMessage used to write the message straight into
// history instead; on a real stack that put it BEFORE the reply of the turn in
// flight, so history ended with the assistant and the turn the wake bought
// yielded without answering (TestSendMessage_RunningWorkflowNeverWritesIntoHistoryMidTurn).
//
// The windows, in the order a finishing run passes through them:
//
//   - during the last turn, after it read history and before or after its
//     pending_inbox probe: the loop-exit gate re-enters for the wake;
//   - after the gate let the loop exit, while the run's completion
//     bookkeeping runs: the run continues as a fresh run, which takes the
//     turn.
//
// These tests used to pin the first window as stranded on main and on #564
// alike; against main's workflow code, the ones that are not controls still
// fail.

// lateUserMessageEnv is the spawn e2e env with a thread's history and
// mailbox modelled, so a turn records what it could see. Nothing in its
// script spawns.
type lateUserMessageEnv struct {
	*spawnE2EEnv
	mailbox *fakeMailbox

	// history is each thread's transcript in seq order: user messages
	// (SaveMessageToThread, or delivered by the drain) and each turn's reply,
	// which — as in CallLLMActivity — is saved when its stream ends.
	// readByTurn is what each of a thread's turns found there when it read
	// history. Guarded by spawnE2EEnv.mu.
	history    map[string][]string
	readByTurn map[string][][]string

	// midTurn runs during the parent's turn N, after its reply is saved and
	// before its pending_inbox probe. afterProbe runs after the probe, before the turn
	// returns: the last moment before the loop-exit gate. onCompleted runs
	// inside the run's "completed" status activity: after the gate let the
	// loop exit, while the run finishes.
	midTurn     map[int]func(thread string)
	afterProbe  map[int]func(thread string)
	onCompleted func(thread string)
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
	env.RegisterActivityWithOptions(
		func(_ context.Context, input map[string]interface{}) (map[string]interface{}, error) {
			e.mu.Lock()
			e.statuses = append(e.statuses, input)
			onCompleted, thread := e.onCompleted, e.parentThread
			e.mu.Unlock()
			if input["status"] == "completed" && onCompleted != nil {
				onCompleted(thread)
			}
			return map[string]interface{}{"success": true}, nil
		},
		activity.RegisterOptions{Name: "WorkflowStatus"},
	)
	return e
}

// callLLM is CallLLMActivity's shape: drain the mailbox into history, read
// history, stream the reply and save it, then probe the mailbox for
// pending_inbox.
func (e *lateUserMessageEnv) callLLM(ctx context.Context, input types.ActivityInput) (map[string]interface{}, error) {
	thread := input.Runtime.Thread
	e.mailbox.drain(thread)
	e.mu.Lock()
	delivered, _ := e.mailbox.snapshot(thread)
	for _, msg := range delivered {
		if !slices.Contains(e.history[thread], msg) {
			e.history[thread] = append(e.history[thread], msg)
		}
	}
	read := slices.Clone(e.history[thread])
	e.readByTurn[thread] = append(e.readByTurn[thread], read)
	e.mu.Unlock()

	out, err := e.callLLMStub(ctx, input)
	if err != nil {
		return nil, err
	}

	e.mu.Lock()
	turn := e.callLLMByThread[thread]
	// A history that already ends with the assistant has nothing new to
	// answer: call_llm yields without a reply (the assistant-tail guard).
	if len(read) == 0 || !isAssistantReply(read[len(read)-1]) {
		e.history[thread] = append(e.history[thread], assistantReply(turn))
	}
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

// sendMessage is SendMessage on an active run, and queueMessage is
// SendAgentMessage: each queues an agent_messages row, then rings the
// doorbell.
func (e *lateUserMessageEnv) sendMessage(thread, text string) { e.queueMessage(thread, text) }

func (e *lateUserMessageEnv) queueMessage(thread, text string) {
	e.mailbox.enqueue(thread, text)
	e.env.SignalWorkflow(ThreadWakeSignalName, ThreadWakeSignal{Thread: thread, Reason: threadwake.ReasonMailbox})
}

func assistantReply(turn int) string { return fmt.Sprintf("assistant: turn %d", turn) }

func isAssistantReply(entry string) bool { return strings.HasPrefix(entry, "assistant: ") }

// readInThisRun reports whether one of the parent's turns ANSWERED text: read
// a history in which text comes after the last assistant reply. Merely being
// in the history is not enough — a user message ordered before the reply that
// ignored it leaves the history ending with the assistant, and the next turn
// yields instead of answering it.
func (e *lateUserMessageEnv) readInThisRun(text string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, seen := range e.readByTurn[e.parentThread] {
		at := slices.Index(seen, text)
		if at < 0 {
			continue
		}
		answered := true
		for _, later := range seen[at+1:] {
			if isAssistantReply(later) {
				answered = false
			}
		}
		if answered {
			return true
		}
	}
	return false
}

// successorOf builds the env for the run a finished one continued as, sharing
// its database: the same thread history and mailbox.
func (e *lateUserMessageEnv) successorOf(t *testing.T, env *testsuite.TestWorkflowEnvironment, carried WorkflowInput) *lateUserMessageEnv {
	t.Helper()
	next := newLateUserMessageEnv(t, env)
	next.history = e.history
	next.mailbox = e.mailbox
	next.parentThread = carried.ExecContext.Thread
	return next
}

// requireContinuedAsFreshRun asserts the run handed off to a fresh run at
// graph entry and returns the successor's input.
func requireContinuedAsFreshRun(t *testing.T, env *testsuite.TestWorkflowEnvironment) WorkflowInput {
	t.Helper()
	require.True(t, env.IsWorkflowCompleted())
	var contErr *workflow.ContinueAsNewError
	require.True(t, errors.As(env.GetWorkflowError(), &contErr),
		"STRANDED: the run completed with input no turn read, instead of continuing as a run for it; got %v", env.GetWorkflowError())
	var carried WorkflowInput
	require.NoError(t, converter.GetDefaultDataConverter().FromPayloads(contErr.Input, &carried))
	require.Nil(t, carried.Resume, "the successor is a fresh run at graph entry, as SendMessage would start")
	require.NotContains(t, carried.Inputs, InputKeyLaunchRun,
		"a run a person's late reply started is not the chat's launch run")
	return carried
}

type LateUserMessageE2ESuite struct {
	suite.Suite
	temporaltest.WorkflowTestSuite
}

func TestLateUserMessageE2E(t *testing.T) {
	t.Parallel()
	suite.Run(t, new(LateUserMessageE2ESuite))
}

// SendMessage lands at the very end of the run's last turn, after its
// pending_inbox probe: no look this turn takes can see it. The gate re-enters
// for the doorbell, and the next turn delivers and answers it.
func (s *LateUserMessageE2ESuite) TestSendMessageDuringTheLastTurnIsReadByThisRun() {
	env := s.NewTestWorkflowEnvironment()
	e := newLateUserMessageEnv(s.T(), env)
	const msg = "one more thing"
	e.afterProbe = map[int]func(string){1: func(thread string) { e.sendMessage(thread, msg) }}

	e.execute("chat-late-send")

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	s.Require().True(e.readInThisRun(msg),
		"STRANDED: the run ended without a turn that answered the user's message; parent took %d turns", e.parentTurns())
	s.Require().Equal(2, e.parentTurns(), "exactly one more turn, for the message")
}

// A queued message that lands before the last turn's probe is caught by
// pending_inbox, and the next turn delivers it. It was already delivered
// before this fix; it is the control for the case below, and must still cost
// exactly one turn now that the doorbell would re-enter too.
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

// The same queued message, landing after the probe. Nothing reads the
// mailbox again; the gate re-enters for its doorbell, and the next turn's
// drain delivers it.
func (s *LateUserMessageE2ESuite) TestQueuedMessageAfterTheProbeIsDeliveredByThisRun() {
	env := s.NewTestWorkflowEnvironment()
	e := newLateUserMessageEnv(s.T(), env)
	const msg = "queued after the probe"
	e.afterProbe = map[int]func(string){1: func(thread string) { e.queueMessage(thread, msg) }}

	e.execute("chat-late-queue-after-probe")

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError())
	_, queued := e.mailbox.snapshot(e.parentThread)
	s.Require().Empty(queued,
		"STRANDED: the run ended with the user's message still queued in the mailbox; parent took %d turns", e.parentTurns())
	s.Require().True(e.readInThisRun(msg))
	s.Require().Equal(2, e.parentTurns())
}

// After the exit check: SendMessage lands while the run is reporting itself
// completed, after the gate has let the loop exit. No turn of this run can
// read it any more, so the run must continue as a fresh one — and that run's
// first turn reads it.
func (s *LateUserMessageE2ESuite) TestSendMessageWhileTheRunFinishesGetsARun() {
	env := s.NewTestWorkflowEnvironment()
	e := newLateUserMessageEnv(s.T(), env)
	const msg = "wait, also this"
	e.onCompleted = func(thread string) { e.sendMessage(thread, msg) }

	input := spawnE2EWorkflowInput("chat-late-send-finishing")
	input.Inputs[InputKeyLaunchRun] = true
	e.parentThread = input.ExecContext.Thread
	env.ExecuteWorkflow(DynamicWorkflow, input)

	carried := requireContinuedAsFreshRun(s.T(), env)
	s.Require().Equal(1, e.parentTurns())
	_, queued := e.mailbox.snapshot(e.parentThread)
	s.Require().Equal([]string{msg}, queued, "this run had already taken its last turn")

	var successorSuite temporaltest.WorkflowTestSuite
	env2 := successorSuite.NewTestWorkflowEnvironment()
	next := e.successorOf(s.T(), env2, carried)
	env2.ExecuteWorkflow(DynamicWorkflow, carried)

	s.Require().True(env2.IsWorkflowCompleted())
	s.Require().NoError(env2.GetWorkflowError(), "the successor reads the message and completes")
	s.Require().True(next.readInThisRun(msg), "the run started for the message must read it")
	s.Require().Equal(1, next.parentTurns())
}

// The same window for a queued row: the successor's first turn drains it.
func (s *LateUserMessageE2ESuite) TestQueuedMessageWhileTheRunFinishesGetsARun() {
	env := s.NewTestWorkflowEnvironment()
	e := newLateUserMessageEnv(s.T(), env)
	const msg = "queued as it finished"
	e.onCompleted = func(thread string) { e.queueMessage(thread, msg) }

	e.execute("chat-late-queue-finishing")

	carried := requireContinuedAsFreshRun(s.T(), env)
	_, queued := e.mailbox.snapshot(e.parentThread)
	s.Require().Equal([]string{msg}, queued, "this run had already taken its last turn")

	var successorSuite temporaltest.WorkflowTestSuite
	env2 := successorSuite.NewTestWorkflowEnvironment()
	next := e.successorOf(s.T(), env2, carried)
	env2.ExecuteWorkflow(DynamicWorkflow, carried)

	s.Require().True(env2.IsWorkflowCompleted())
	s.Require().NoError(env2.GetWorkflowError())
	_, queued = next.mailbox.snapshot(next.parentThread)
	s.Require().Empty(queued, "the run started for the row must drain it")
	s.Require().True(next.readInThisRun(msg))
}

// A wake for a DIFFERENT thread — a spawned agent's — is that thread's
// business, not a reason for the root run to go round again.
func (s *LateUserMessageE2ESuite) TestAWakeForAnotherThreadDoesNotRestartTheRun() {
	env := s.NewTestWorkflowEnvironment()
	e := newLateUserMessageEnv(s.T(), env)
	e.onCompleted = func(string) {
		e.env.SignalWorkflow(ThreadWakeSignalName, ThreadWakeSignal{Thread: "some-sub-agent", Reason: threadwake.ReasonMailbox})
	}

	e.execute("chat-late-other-thread")

	s.Require().True(env.IsWorkflowCompleted())
	s.Require().NoError(env.GetWorkflowError(), "the run completes; nothing of its own thread is unread")
	s.Require().Equal(1, e.parentTurns())
}
