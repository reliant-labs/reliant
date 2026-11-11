// Copyright (c) 2026 Reliant Labs
// SPDX-License-Identifier: Apache-2.0

package reliant

import (
	"context"

	"connectrpc.com/connect"

	reliantv1 "github.com/reliant-labs/reliant/sdk/go/gen/reliant/v1"
)

// runScope is one run inside its session: the run's own workflow execution and
// everything that ran under it.
//
// A run id IS its workflow-execution id, and a session (chat) holds every run
// started in it: several roots, newest first, each with its tree of child
// workflows. Everything the session reports (its update feed, its pending
// questions and approvals, its execution tree) spans all of them, so a call
// about ONE run has to select that run's subtree, or it reports another run's
// verdict or gates under this run's id. StatusSession and PendingInSession
// deliberately span the session; FollowSession follows the session's current
// run, so it pins one (see newSessionScope).
type runScope struct {
	// runID is empty while a session scope is unpinned: the session had no run
	// yet when the follow attached, and the first one it shows is pinned.
	runID string
	// ids are the workflow executions of the subtree: the run's and every child
	// workflow under it, including ones learned from the feed after the last
	// tree read.
	ids map[string]bool
	// threads are the threads those executions run on, inline forks included.
	threads map[string]bool
	// gates are gate ids already attributed to the run, so a gate's later
	// updates (its resolution) are attributed the same way.
	gates map[string]bool
	// newest is true when the run is in the session's newest root tree. A gate
	// that names neither a workflow nor a thread is the session's current
	// run's, as the session-wide view has always treated it.
	newest bool
}

func newRunScope(runID string) *runScope {
	s := newSessionScope()
	s.pin(runID)
	return s
}

// newSessionScope is the scope of a session's current run, not yet known: the
// first tree read that shows a root pins the newest one, so an older run's
// rows (its green terminal above all) never stand in for the current run's.
func newSessionScope() *runScope {
	return &runScope{
		ids:     map[string]bool{},
		threads: map[string]bool{},
		gates:   map[string]bool{},
	}
}

func (s *runScope) pin(runID string) {
	s.runID = runID
	s.ids[runID] = true
}

func (s *runScope) pinned() bool { return s.runID != "" }

// load finds the run in the session's roots and adds its subtree to the scope.
// It returns the run's execution, or nil when the tree does not hold it (yet).
// What was learned earlier is kept: a tree read never shrinks the scope. An
// unpinned scope pins the newest root.
func (s *runScope) load(roots []*reliantv1.WorkflowExecution) *reliantv1.WorkflowExecution {
	if !s.pinned() {
		if len(roots) == 0 || roots[0].GetId() == "" {
			return nil
		}
		s.pin(roots[0].GetId())
	}
	for i, root := range roots {
		if ex := findExecution(root, s.runID); ex != nil {
			s.addTree(ex)
			s.newest = i == 0
			return ex
		}
	}
	return nil
}

func (s *runScope) addTree(ex *reliantv1.WorkflowExecution) {
	if ex.GetId() != "" {
		s.ids[ex.GetId()] = true
	}
	if ex.GetThread() != "" {
		s.threads[ex.GetThread()] = true
	}
	for _, child := range ex.GetChildren() {
		s.addTree(child)
	}
}

// findExecution returns the execution with this id in the tree, or nil.
func findExecution(ex *reliantv1.WorkflowExecution, id string) *reliantv1.WorkflowExecution {
	if ex == nil {
		return nil
	}
	if ex.GetId() == id {
		return ex
	}
	for _, child := range ex.GetChildren() {
		if found := findExecution(child, id); found != nil {
			return found
		}
	}
	return nil
}

// owns reports whether a gate belongs to the run: by the workflow that raised
// it, by its thread, or (naming neither) as the session's current run's.
func (s *runScope) owns(g Gate) bool {
	switch {
	case s.gates[g.ID]:
		return true
	case g.RunID != "" && s.ids[g.RunID]:
		return true
	case g.ThreadID != "" && s.threads[g.ThreadID]:
		return true
	case g.RunID == "" && g.ThreadID == "":
		return s.newest
	}
	return false
}

// filter keeps the run's gates, and remembers them.
func (s *runScope) filter(gates []Gate) []Gate {
	out := gates[:0:0]
	for _, g := range gates {
		if s.owns(g) {
			s.gates[g.ID] = true
			out = append(out, g)
		}
	}
	return out
}

// sessionTree reads a session's root executions, newest first.
func (r *RunsClient) sessionTree(ctx context.Context, sessionID string, view reliantv1.WorkflowExecutionView) ([]*reliantv1.WorkflowExecution, error) {
	resp, err := r.chat.GetWorkflowExecutions(ctx, connect.NewRequest(&reliantv1.GetWorkflowExecutionsRequest{
		ChatId: sessionID,
		View:   view,
	}))
	if err != nil {
		return nil, err
	}
	return resp.Msg.GetAllRootWorkflows(), nil
}
