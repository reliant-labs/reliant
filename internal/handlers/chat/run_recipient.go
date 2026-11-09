package chat

import (
	"context"
	"fmt"
	"strings"

	"github.com/reliant-labs/forge/pkg/svcerr"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/errreason"
	"github.com/reliant-labs/reliant/internal/logging"
	rtemporal "github.com/reliant-labs/reliant/internal/temporal"

	v2 "github.com/reliant-labs/reliant/internal/workflow/runtime"
)

// Input addressed to a live RUN lands on a thread an agent of the run reads.
//
// A message is read only by CallLLM, which drains its own thread's mailbox. A
// graph run (pr-fix) runs its agents on threads of their own and none on its
// root, so a message queued on the root was never read, and the run then
// restarted at graph entry for it (reliant #2394). The run says which threads
// its agents read (runtime.AgentLoopThreadsQuery), and this decides:
//
//   - No target, or the run's root thread (what the composer sends from the
//     run's main view): the root, when an agent reads it (an agent chat); else
//     the run's one agent, when exactly one is running; else, with several
//     running in parallel, a refusal that lists them so the sender names one —
//     never a guess; else, with none running, "not delivered: no agent is
//     running in this run", and nothing is queued.
//   - A thread id of the run: that thread, when an agent reads it or it is a
//     sub-agent spawned in this run that is still running (whose mailbox,
//     stop and resume work as before); otherwise refused, listing the
//     threads that do read. A sub-agent left over from an earlier run of the
//     chat is refused: nothing would read its mailbox.
//   - A node name ("implement", or a full node path such as
//     "implement.agent_loop"): the one running agent at that node, or a
//     refusal when none or several match.
//
// When the run cannot say — a worker from before the query, Temporal
// unreachable — the message goes where it always went: the target, else the
// root thread.

// agentLoopThreadsQueryTimeout bounds asking a run which threads its agents
// read. The same bound as the params query (paramsChangeQueryTimeout), for
// the same reason: the query is answered by the worker, a busy worker held
// sends 5+ seconds in prod, and a send holds the chat's run-control lock. An
// idle, cached run answers in milliseconds; one that does not answer in time
// gets the pre-routing default.
const agentLoopThreadsQueryTimeout = paramsChangeQueryTimeout

// runRecipient is the thread input to a run goes to.
type runRecipient struct {
	thread string
	// routed: the run said which threads its agents read. False when it
	// could not, and the recipient is the pre-routing default.
	routed bool
	// rootReads: an agent loop runs on the run's root thread (an agent
	// chat). Only then may a message the run ended before reading start a
	// fresh run for it; a graph run is never restarted for a message.
	rootReads bool
}

// resolveRunRecipient decides where input to the run rooted at workflowID
// goes; requested is the sender's target ("" for none). rootThread is the
// root workflow's thread.
func (s *Service) resolveRunRecipient(ctx context.Context, chat *db.Chat, workflowID, rootThread, requested string) (runRecipient, error) {
	requested = strings.TrimSpace(requested)
	listeners, err := s.agentLoopThreads(ctx, workflowID)
	if err != nil {
		target := rootThread
		if requested != "" {
			target = requested
		}
		logging.InfoContext(ctx, "Run could not say which threads its agents read; delivering where input to a run always went",
			"chatID", chat.ID, "workflowID", workflowID, "thread", target, "error", err)
		return runRecipient{thread: target}, nil
	}
	rec := runRecipient{routed: true}
	for _, l := range listeners {
		if l.Thread == rootThread {
			rec.rootReads = true
		}
	}

	if requested == "" || requested == rootThread || requested == chat.MainThreadID() {
		switch {
		case rec.rootReads:
			rec.thread = rootThread
		case len(listeners) == 1:
			rec.thread = listeners[0].Thread
		case len(listeners) == 0:
			return rec, errNoListeningAgent()
		default:
			return rec, s.errRecipientAmbiguous(ctx, listeners)
		}
		return rec, nil
	}

	// A target that is no thread id may be a node name, below.
	thread, err := s.deps.Repo.GetThread(ctx, requested)
	if err != nil && !svcerr.IsNotFound(err) {
		return rec, svcerr.WrapOr(err, svcerr.Internal("failed to get thread"))
	}
	if thread != nil {
		if thread.ChatID != chat.ID {
			return rec, svcerr.NotFound("thread")
		}
		if hasListener(listeners, requested) {
			rec.thread = requested
			return rec, nil
		}
		ofRun, err := s.isLiveSubAgentOfRun(ctx, chat, thread, workflowID)
		if err != nil {
			return rec, err
		}
		if ofRun {
			rec.thread = requested
			return rec, nil
		}
		if len(listeners) == 0 {
			return rec, errNoListeningAgent()
		}
		return rec, svcerr.WithReason(svcerr.WithCause(svcerr.FailedPrecondition(fmt.Sprintf(
			"not delivered: no agent is running on thread %s right now. Agents running in this run: %s",
			requested, s.describeListeners(ctx, listeners))), core.ErrRunNoListeningAgent), errreason.RunNoListeningAgent)
	}

	var matches []v2.AgentLoopThread
	for _, l := range listeners {
		if nodeMatches(l.Node, requested) {
			matches = append(matches, l)
		}
	}
	switch len(matches) {
	case 1:
		rec.thread = matches[0].Thread
		return rec, nil
	case 0:
		running := "none"
		if len(listeners) > 0 {
			running = s.describeListeners(ctx, listeners)
		}
		return rec, svcerr.InvalidArgument(fmt.Sprintf(
			"%q is neither a thread of this run nor the node of an agent running in it. Agents running in this run: %s",
			requested, running))
	default:
		return rec, s.errRecipientAmbiguous(ctx, matches)
	}
}

// maxSubAgentDepth bounds the walk from a sub-agent's run up to the run it
// was spawned in, so a malformed parent chain cannot loop.
const maxSubAgentDepth = 64

// isLiveSubAgentOfRun reports whether thread is a sub-agent spawned in the
// run rooted at workflowID that is still running, so its own loop reads its
// mailbox.
//
// A chat's successive runs share one root workflow id (the chat's main
// thread), so descending from it says only that the sub-agent was spawned in
// SOME run of the chat. What says it belongs to the live one is that it has
// not finished: when a run ends, the threads it owns take a terminal status
// (core.ThreadStatusForStopReason), so a sub-agent of an earlier run reads as
// completed, failed or cancelled, and a message queued there would never be
// read (reliant #2464 review).
func (s *Service) isLiveSubAgentOfRun(ctx context.Context, chat *db.Chat, thread *db.Thread, workflowID string) (bool, error) {
	if thread.Origin != db.ThreadOriginSpawn || core.ThreadStatusIsTerminal(thread.Status) {
		return false, nil
	}
	wf, err := s.deps.Repo.GetWorkflowByThread(ctx, chat.ID, thread.ID)
	for depth := 0; depth < maxSubAgentDepth; depth++ {
		if err != nil && !svcerr.IsNotFound(err) {
			return false, svcerr.WrapOr(err, svcerr.Internal("failed to get workflow"))
		}
		if wf == nil || wf.ParentID == nil || *wf.ParentID == "" {
			return false, nil
		}
		if *wf.ParentID == workflowID {
			return true, nil
		}
		wf, err = s.deps.Repo.GetWorkflow(ctx, *wf.ParentID)
	}
	return false, nil
}

// agentLoopThreads asks the run which of its threads an agent loop runs on.
func (s *Service) agentLoopThreads(ctx context.Context, workflowID string) ([]v2.AgentLoopThread, error) {
	if s.deps.Temporal == nil || workflowID == "" {
		return nil, fmt.Errorf("no run to ask")
	}
	// Bounded: a send must not wait on a worker that is slow to answer; one
	// that cannot answer in time gets the pre-routing default.
	ctx, cancel := context.WithTimeout(ctx, agentLoopThreadsQueryTimeout)
	defer cancel()
	value, err := rtemporal.QueryOpenRun(ctx, s.deps.Temporal, workflowID, v2.AgentLoopThreadsQuery)
	if err != nil {
		return nil, err
	}
	var listeners []v2.AgentLoopThread
	if err := value.Get(&listeners); err != nil {
		return nil, err
	}
	return listeners, nil
}

func hasListener(listeners []v2.AgentLoopThread, thread string) bool {
	for _, l := range listeners {
		if l.Thread == thread {
			return true
		}
	}
	return false
}

// nodeMatches reports whether name names the agent at node path: the whole
// path, its first segment (the graph's own node, "implement"), or its last
// (the loop, "agent_loop").
func nodeMatches(path, name string) bool {
	if path == "" || name == "" {
		return false
	}
	if path == name {
		return true
	}
	segments := strings.Split(path, ".")
	return segments[0] == name || segments[len(segments)-1] == name
}

func errNoListeningAgent() error {
	return svcerr.WithReason(svcerr.WithCause(svcerr.FailedPrecondition(
		"not delivered: no agent is running in this run right now, so nothing would read the message"),
		core.ErrRunNoListeningAgent), errreason.RunNoListeningAgent)
}

func (s *Service) errRecipientAmbiguous(ctx context.Context, listeners []v2.AgentLoopThread) error {
	return svcerr.WithReason(svcerr.WithCause(svcerr.FailedPrecondition(fmt.Sprintf(
		"not delivered: several agents are running in this run; name the one the message is for (target thread: its thread id or node name). Running: %s",
		s.describeListeners(ctx, listeners))), core.ErrRunRecipientAmbiguous), errreason.RunRecipientAmbiguous)
}

// describeListeners names each running agent for a refusal: thread id, node,
// and the thread's title when it has one.
func (s *Service) describeListeners(ctx context.Context, listeners []v2.AgentLoopThread) string {
	parts := make([]string, 0, len(listeners))
	for _, l := range listeners {
		part := "thread " + l.Thread
		if l.Node != "" {
			part += " (node " + l.Node
			if thread, err := s.deps.Repo.GetThread(ctx, l.Thread); err == nil && thread != nil && thread.Title != nil && *thread.Title != "" {
				part += fmt.Sprintf(", %q", *thread.Title)
			}
			part += ")"
		}
		parts = append(parts, part)
	}
	return strings.Join(parts, "; ")
}
