// Copyright (c) 2025 Reliant Labs
package chat

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/handlers/run"
	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
	typedconfig "github.com/reliant-labs/reliant/pkg/config"
)

// Input addressed to a run lands on the thread an agent of the run reads
// (run_recipient.go; reliant #2394). A graph run (pr-fix) works in a
// `workflow` node on a thread of its own and runs no agent on its root, so a
// message queued on the root was never read.

// agentLoopTemporal is a run that answers AgentLoopThreadsQuery with the
// threads its agents read; every other query and call is closingRunTemporalClient's.
type agentLoopTemporal struct {
	closingRunTemporalClient
	listeners []v2.AgentLoopThread
}

func (c *agentLoopTemporal) QueryWorkflowWithOptions(ctx context.Context, req *client.QueryWorkflowWithOptionsRequest) (*client.QueryWorkflowWithOptionsResponse, error) {
	if req.QueryType == v2.AgentLoopThreadsQuery {
		c.mu.Lock()
		closed := c.closed
		c.mu.Unlock()
		return fakeQueryResponse(req, closed, jsonEncodedValue{v: c.listeners}, nil)
	}
	return c.closingRunTemporalClient.QueryWorkflowWithOptions(ctx, req)
}

// graphRunFixture is an absorb fixture whose run is a graph with agent node
// threads (pr-fix's `implement`, a parallel `review`).
func addNodeThread(t *testing.T, ctx context.Context, repo *db.Repo, chatID, title string) string {
	t.Helper()
	id := chatID + "-" + title
	titled := title
	_, err := repo.CreateThread(ctx, &db.Thread{
		ID: id, ChatID: chatID, Origin: db.ThreadOriginNode, Title: &titled,
		Status: db.ThreadStatusRunning, CreatedAt: time.Now().UTC(),
	})
	require.NoError(t, err)
	return id
}

func routedService(repo *db.Repo, temporal *agentLoopTemporal) *Service {
	return lateMessageService(repo, &temporal.closingRunTemporalClient).withTemporal(temporal)
}

func (s *Service) withTemporal(t Temporal) *Service {
	s.deps.Temporal = t
	return s
}

func queuedBodies(t *testing.T, ctx context.Context, repo *db.Repo, thread string) []string {
	t.Helper()
	rows, err := repo.ListQueuedAgentMessagesForThread(ctx, thread)
	require.NoError(t, err)
	var bodies []string
	for _, r := range rows {
		bodies = append(bodies, r.Body)
	}
	return bodies
}

func TestSendMessage_GraphRunRoutesToItsOneRunningAgent(t *testing.T) {
	t.Parallel()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Active())
	implement := addNodeThread(t, ctx, repo, fx.chatID, "implement")
	temporal := &agentLoopTemporal{listeners: []v2.AgentLoopThread{{Thread: implement, Node: "implement.agent_loop"}}}

	resp, err := routedService(repo, temporal).SendMessage(ctx, sendMessageRequest(t, fx.chatID, "the lease moved to 1899b37a"))
	require.NoError(t, err)
	assert.True(t, resp.Msg.Queued)
	assert.Equal(t, []string{"the lease moved to 1899b37a"}, queuedBodies(t, ctx, repo, implement),
		"the run's one running agent gets the message")
	assert.Empty(t, queuedBodies(t, ctx, repo, fx.rootThreadID),
		"nothing on the root thread, where no agent of a graph run reads")
}

func TestSendMessage_ParallelAgentsAreRefusedNotGuessed(t *testing.T) {
	t.Parallel()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Active())
	implement := addNodeThread(t, ctx, repo, fx.chatID, "implement")
	review := addNodeThread(t, ctx, repo, fx.chatID, "review")
	temporal := &agentLoopTemporal{listeners: []v2.AgentLoopThread{
		{Thread: implement, Node: "implement.agent_loop"},
		{Thread: review, Node: "review.agent_loop"},
	}}
	svc := routedService(repo, temporal)

	_, err := svc.SendMessage(ctx, sendMessageRequest(t, fx.chatID, "which one?"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, core.ErrRunRecipientAmbiguous))
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	for _, want := range []string{implement, review, "implement.agent_loop", "review.agent_loop", `"implement"`, `"review"`} {
		assert.Contains(t, err.Error(), want, "the refusal lists every running agent so the sender can pick one")
	}
	assert.Empty(t, queuedBodies(t, ctx, repo, implement))
	assert.Empty(t, queuedBodies(t, ctx, repo, review))
	assert.Empty(t, queuedBodies(t, ctx, repo, fx.rootThreadID))

	// Naming one delivers to it: by node name, or by thread id.
	byNode := sendMessageRequest(t, fx.chatID, "for the reviewer")
	node := "review"
	byNode.Msg.TargetThread = &node
	_, err = svc.SendMessage(ctx, byNode)
	require.NoError(t, err)
	assert.Equal(t, []string{"for the reviewer"}, queuedBodies(t, ctx, repo, review))

	byID := sendMessageRequest(t, fx.chatID, "for the implementer")
	byID.Msg.TargetThread = &implement
	_, err = svc.SendMessage(ctx, byID)
	require.NoError(t, err)
	assert.Equal(t, []string{"for the implementer"}, queuedBodies(t, ctx, repo, implement))
}

func TestSendMessage_NoRunningAgentIsNotDeliveredAndQueuesNothing(t *testing.T) {
	t.Parallel()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Active())
	temporal := &agentLoopTemporal{} // a graph between its agent steps

	_, err := routedService(repo, temporal).SendMessage(ctx, sendMessageRequest(t, fx.chatID, "anyone?"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, core.ErrRunNoListeningAgent))
	assert.Contains(t, err.Error(), "no agent is running in this run")
	assert.Empty(t, queuedBodies(t, ctx, repo, fx.rootThreadID), "never queued on the root, where nobody reads")
}

// An agent chat's own agent runs on its root: the default is unchanged.
func TestSendMessage_AgentChatRootStillTakesTheMessage(t *testing.T) {
	t.Parallel()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Active())
	temporal := &agentLoopTemporal{listeners: []v2.AgentLoopThread{{Thread: fx.rootThreadID, Node: "agent_loop"}}}

	_, err := routedService(repo, temporal).SendMessage(ctx, sendMessageRequest(t, fx.chatID, "steer"))
	require.NoError(t, err)
	assert.Equal(t, []string{"steer"}, queuedBodies(t, ctx, repo, fx.rootThreadID))
}

// A pending message never restarts a workflow. A graph run that closed
// before its agent's wake is not started again at graph entry for the
// message (which re-ran pr-fix for chat 4ece80a3): the row is withdrawn and
// the send says it was not delivered.
func TestSendMessage_GraphRunThatClosesBeforeTheWakeIsNotRestarted(t *testing.T) {
	t.Parallel()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Active())
	implement := addNodeThread(t, ctx, repo, fx.chatID, "implement")
	temporal := &agentLoopTemporal{listeners: []v2.AgentLoopThread{{Thread: implement, Node: "implement.agent_loop"}}}
	temporal.closeOnWake = true

	_, err := routedService(repo, temporal).SendMessage(ctx, sendMessageRequest(t, fx.chatID, "too late"))
	require.Error(t, err)
	assert.True(t, errors.Is(err, core.ErrRunNoListeningAgent))
	assert.Empty(t, temporal.runsStarted(), "the workflow must not be started over for a message")
	assert.Empty(t, queuedBodies(t, ctx, repo, implement), "the row is withdrawn, not left for the reconciler")
}

// A run that cannot say (a worker from before the query) gets the message
// where it always went: the root thread.
func TestSendMessage_RunThatCannotSayFallsBackToTheRoot(t *testing.T) {
	t.Parallel()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Active())
	temporal := &closingRunTemporalClient{} // answers no agent_loop_threads query

	_, err := lateMessageService(repo, temporal).SendMessage(ctx, sendMessageRequest(t, fx.chatID, "old worker"))
	require.NoError(t, err)
	assert.Equal(t, []string{"old worker"}, queuedBodies(t, ctx, repo, fx.rootThreadID))
}

// SignalRun (what send_to_run calls) on a graph run: no target reaches its
// one running agent; with none running it reports delivered=false and says
// why in the reason header, queuing nothing; with several it is refused with
// the list; a node name picks one.
func TestSignalRun_RoutesToTheRunsRunningAgent(t *testing.T) {
	t.Parallel()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Active())
	implement := addNodeThread(t, ctx, repo, fx.chatID, "implement")
	review := addNodeThread(t, ctx, repo, fx.chatID, "review")
	temporal := &agentLoopTemporal{}
	runs, err := run.New(run.Deps{
		Logger: slog.Default(),
		Config: &typedconfig.Config{},
		Repo:   repo,
		Chats:  routedService(repo, temporal),
	})
	require.NoError(t, err)
	signal := func(thread, content string) (*connect.Response[reliantv1.SignalRunResponse], error) {
		return runs.SignalRun(ctx, connect.NewRequest(&reliantv1.SignalRunRequest{
			RunId:    fx.chatID,
			Thread:   thread,
			Messages: []*reliantv1.InputMessage{{Role: reliantv1.MessageRole_MESSAGE_ROLE_USER, Content: content}},
		}))
	}

	// Between agent steps: nothing to read it.
	resp, err := signal("", "nobody home")
	require.NoError(t, err, "a live run with no agent running reports delivered=false, never an error")
	assert.False(t, resp.Msg.Delivered)
	assert.Equal(t, errreason.RunNoListeningAgent, resp.Header().Get(errreason.NotDeliveredHeader))
	assert.Empty(t, queuedBodies(t, ctx, repo, fx.rootThreadID))

	// One agent running: it gets the message.
	temporal.listeners = []v2.AgentLoopThread{{Thread: implement, Node: "implement.agent_loop"}}
	resp, err = signal("", "for the one agent")
	require.NoError(t, err)
	assert.True(t, resp.Msg.Delivered)
	assert.Equal(t, []string{"for the one agent"}, queuedBodies(t, ctx, repo, implement))

	// Two in parallel: refused with both listed, nothing queued.
	temporal.listeners = append(temporal.listeners, v2.AgentLoopThread{Thread: review, Node: "review.agent_loop"})
	_, err = signal("", "for whom?")
	require.Error(t, err)
	assert.True(t, errors.Is(err, core.ErrRunRecipientAmbiguous))
	assert.Contains(t, err.Error(), implement)
	assert.Contains(t, err.Error(), review)
	assert.Empty(t, queuedBodies(t, ctx, repo, review))

	// Named by node.
	resp, err = signal("review", "for the reviewer")
	require.NoError(t, err)
	assert.True(t, resp.Msg.Delivered)
	assert.Equal(t, []string{"for the reviewer"}, queuedBodies(t, ctx, repo, review))
}

// The composer's queue path routes the same way: a message queued to the
// main thread of a graph run reaches its one running agent, and several in
// parallel are refused with the list.
func TestSendAgentMessage_MainThreadOfAGraphRunRoutesToItsAgent(t *testing.T) {
	t.Parallel()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Active())
	implement := addNodeThread(t, ctx, repo, fx.chatID, "implement")
	temporal := &agentLoopTemporal{listeners: []v2.AgentLoopThread{{Thread: implement, Node: "implement.agent_loop"}}}
	svc := routedService(repo, temporal)

	resp, err := svc.SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId: fx.chatID, ThreadId: fx.rootThreadID, Message: "queued from the main view",
	}))
	require.NoError(t, err)
	require.True(t, resp.Msg.Success, resp.Msg.Message)
	assert.Equal(t, []string{"queued from the main view"}, queuedBodies(t, ctx, repo, implement))
	assert.Empty(t, queuedBodies(t, ctx, repo, fx.rootThreadID))

	review := addNodeThread(t, ctx, repo, fx.chatID, "review")
	temporal.listeners = append(temporal.listeners, v2.AgentLoopThread{Thread: review, Node: "review.agent_loop"})
	resp, err = svc.SendAgentMessage(ctx, connect.NewRequest(&reliantv1.SendAgentMessageRequest{
		ChatId: fx.chatID, ThreadId: fx.rootThreadID, Message: "which one?",
	}))
	require.NoError(t, err)
	assert.False(t, resp.Msg.Success)
	assert.Contains(t, resp.Msg.Message, review)
	assert.Contains(t, resp.Msg.Message, implement)
}

// addSpawnedThread is a sub-agent's thread and its run, spawned by the run
// parentWorkflowID; status is the thread's (and so the sub-agent's) state.
func addSpawnedThread(t *testing.T, ctx context.Context, repo *db.Repo, chatID, name, parentWorkflowID string, status db.ThreadStatus) string {
	t.Helper()
	id := chatID + "-" + name
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := repo.CreateThread(ctx, &db.Thread{
		ID: id, ChatID: chatID, Origin: db.ThreadOriginSpawn, Status: status, CreatedAt: at,
	})
	require.NoError(t, err)
	wfStatus := db.Active()
	if core.ThreadStatusIsTerminal(status) {
		wfStatus = db.Completed()
	}
	require.NoError(t, repo.CreateWorkflow(ctx, &db.Workflow{
		ID: id + "-wf", ParentID: &parentWorkflowID, ChatID: chatID, WorkflowName: "builtin://agent",
		Thread: id, Status: wfStatus, CreatedAt: at,
	}))
	return id
}

// A target that is a sub-agent spawned in this run reaches it; one left over
// from an earlier run of the same chat (they share the root workflow id) has
// finished, nothing reads its mailbox, and the send is refused rather than
// stranded there.
func TestSendMessage_TargetSubAgentMustBeRunningInThisRun(t *testing.T) {
	t.Parallel()
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)
	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Active())
	implement := addNodeThread(t, ctx, repo, fx.chatID, "implement")
	live := addSpawnedThread(t, ctx, repo, fx.chatID, "live-helper", fx.rootThreadID, db.ThreadStatusRunning)
	stale := addSpawnedThread(t, ctx, repo, fx.chatID, "earlier-run-helper", fx.rootThreadID, db.ThreadStatusCompleted)
	temporal := &agentLoopTemporal{listeners: []v2.AgentLoopThread{{Thread: implement, Node: "implement.agent_loop"}}}
	svc := routedService(repo, temporal)

	toLive := sendMessageRequest(t, fx.chatID, "for the helper")
	toLive.Msg.TargetThread = &live
	_, err := svc.SendMessage(ctx, toLive)
	require.NoError(t, err)
	assert.Equal(t, []string{"for the helper"}, queuedBodies(t, ctx, repo, live))

	toStale := sendMessageRequest(t, fx.chatID, "for a finished helper")
	toStale.Msg.TargetThread = &stale
	_, err = svc.SendMessage(ctx, toStale)
	require.Error(t, err)
	assert.True(t, errors.Is(err, core.ErrRunNoListeningAgent))
	assert.Contains(t, err.Error(), implement, "the refusal names the agent that does read")
	assert.Empty(t, queuedBodies(t, ctx, repo, stale), "nothing is stranded on a sub-agent of an earlier run")
	assert.Empty(t, queuedBodies(t, ctx, repo, implement))
}
