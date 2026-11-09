// Copyright (c) 2025 Reliant Labs
package runtime

import (
	"sort"

	"github.com/reliant-labs/reliant/internal/workflow/model"
)

// Input addressed to a RUN — send_to_run, SignalRun, the composer on a run's
// main view — has to land on a thread whose agent will read it. A message is
// read only by CallLLM, which drains its own thread's mailbox, so a thread no
// agent loop runs on never reads anything queued to it.
//
// That is the trap a graph run set (reliant #2394). pr-fix does its agent work
// in a `workflow` node (`implement`) on a thread of its own; its root thread
// runs no agent loop at all. Every send_to_run to a pr-fix run queued on the
// root thread, was never read, and at run end the late-wake check continued
// the run as a fresh one at graph entry, re-running the whole workflow for a
// message it still could not read.
//
// So the run says which of its threads an agent is reading right now: the
// agent-loop registry below, answered by AgentLoopThreadsQuery. Only loops the
// run's graph drives are listed — the root loop of an agent chat, and the loop
// of a `workflow` node's agent. A spawned sub-agent (spawn depth > 0) belongs
// to the agent that spawned it, which talks to it with spawn_send, and is
// addressed by its thread id rather than chosen by default.
//
// The registry is in-memory state that only workflow code writes, and it
// issues no command, so it replays identically and needs no version gate.

// AgentLoopThreadsQuery is the Temporal query that returns the threads of the
// run an agent loop is running on now ([]AgentLoopThread). A worker from
// before it answers "unknown queryType", which callers treat as "cannot say".
const AgentLoopThreadsQuery = "agent_loop_threads"

// AgentLoopThread is one thread of the run whose agent loop is running: the
// thread, and the graph path of the node that runs it ("agent_loop",
// "implement.agent_loop").
type AgentLoopThread struct {
	Thread string `json:"thread"`
	Node   string `json:"node,omitempty"`
}

// agentLoop is one registered loop's thread entry: nested or relaunched loops
// on one thread count up, and the first registration's node names it.
type agentLoop struct {
	node  string
	depth int
}

// registerAgentLoop records that an agent loop runs on thread, at node, and
// returns the release the loop calls when it exits.
func (t *ChildWorkflowTracker) registerAgentLoop(thread, node string) func() {
	if t == nil || thread == "" {
		return func() {}
	}
	if t.agentLoops == nil {
		t.agentLoops = make(map[string]*agentLoop)
	}
	entry := t.agentLoops[thread]
	if entry == nil {
		entry = &agentLoop{node: node}
		t.agentLoops[thread] = entry
	}
	entry.depth++
	if thread == t.rootThread {
		t.rootRanAgentLoop = true
	}
	return func() {
		entry.depth--
		if entry.depth <= 0 && t.agentLoops[thread] == entry {
			delete(t.agentLoops, thread)
		}
	}
}

// agentLoopThreads lists the threads an agent loop is running on now, by
// thread id, so the answer is stable.
func (t *ChildWorkflowTracker) agentLoopThreads() []AgentLoopThread {
	if t == nil {
		return nil
	}
	out := make([]AgentLoopThread, 0, len(t.agentLoops))
	for thread, entry := range t.agentLoops {
		out = append(out, AgentLoopThread{Thread: thread, Node: entry.node})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Thread < out[j].Thread })
	return out
}

// registerAgentLoop records this loop in the run's agent-loop registry when
// it is one a message to the run may be routed to: a loop whose body calls
// the LLM, driven by the run's graph rather than by a spawn. It returns the
// release to defer, or nil when the loop is not one.
func (e *InlineLoopExecutor) registerAgentLoop() func() {
	if e.childTracker == nil || e.subWorkflow == nil {
		return nil
	}
	if e.execContext != nil && e.execContext.SpawnDepth > 0 {
		return nil
	}
	thread := e.GetThread()
	if thread == "" {
		return nil
	}
	for _, node := range e.subWorkflow.GetNodes() {
		if model.NodeType(node) == model.NodeTypeCallLLM {
			return e.childTracker.registerAgentLoop(thread, e.nodePath())
		}
	}
	return nil
}
