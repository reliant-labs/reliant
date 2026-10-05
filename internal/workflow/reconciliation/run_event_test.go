// Copyright (c) 2025 Reliant Labs
package reconciliation

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/triggers/runevents"
)

// A run Temporal killed outright never runs its completion handler, so the
// WorkflowStatus activity that normally writes the workflow-event outbox row
// never fires. The reconciler is the only component that sees the death, and
// its repair must emit the run's "failed" event — once, however many passes
// observe it.

// runEventRepo is a terminationRepo whose owner has a workflow_event trigger,
// recording the run events the reconciler writes.
type runEventRepo struct {
	*terminationRepo

	mu     sync.Mutex
	events map[string]*core.RunEvent // dedupe key -> event
}

func (r *runEventRepo) HasEnabledTriggerOfKind(_ context.Context, _ string, kind core.TriggerKind) (bool, error) {
	return kind == runevents.TriggerKind, nil
}

func (r *runEventRepo) CreateRunEvent(_ context.Context, ev *core.RunEvent) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.events[ev.DedupeKey]; ok {
		return false, nil
	}
	r.events[ev.DedupeKey] = ev
	return true, nil
}

func TestReconciler_RepairedRootTerminationEmitsOneFailedRunEvent(t *testing.T) {
	base := newTerminationRepo(terminatedChatWorkflow())
	base.chats["chat-1"] = &db.Chat{ID: "chat-1", UserID: "user-1", ProjectID: "project-1"}
	repo := &runEventRepo{terminationRepo: base, events: map[string]*core.RunEvent{}}

	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeTerminatedDescribeResp("run-1")},
		},
		historyEvents: makeTerminatedCloseEvent("Workflow history count exceeds limit."),
	}
	reconciler := NewReconciler(repo, tempClient, DefaultConfig())

	for pass := 0; pass < 3; pass++ {
		_, errs := reconciler.ReconcileRunningWorkflows(context.Background())
		require.Empty(t, errs)
	}

	require.Equal(t, db.Failed(), base.rows["wf-1"].Status)
	require.Len(t, repo.events, 1, "the repair must emit exactly one run event across passes")
	for key, ev := range repo.events {
		assert.Equal(t, "chat-1:run-1:failed", key)
		assert.Equal(t, core.RunEventFailed, ev.Outcome)
		assert.Equal(t, "user-1", ev.UserID)
		assert.NotEmpty(t, ev.Payload["error"])
	}
}

func TestReconciler_RepairedChildEmitsNoRunEvent(t *testing.T) {
	child := terminatedChatWorkflow()
	parent := "root-wf"
	child.ParentID = &parent
	base := newTerminationRepo(child)
	base.chats["chat-1"] = &db.Chat{ID: "chat-1", UserID: "user-1", ProjectID: "project-1"}
	repo := &runEventRepo{terminationRepo: base, events: map[string]*core.RunEvent{}}

	tempClient := &mockReconcilerTemporalClient{
		describeResponses: map[string]mockDescribeResponse{
			"wf-1": {resp: makeTerminatedDescribeResp("run-1")},
		},
		historyEvents: makeTerminatedCloseEvent("terminated"),
	}
	reconciler := NewReconciler(repo, tempClient, DefaultConfig())
	_, errs := reconciler.ReconcileRunningWorkflows(context.Background())
	require.Empty(t, errs)

	assert.Empty(t, repo.events, "only a ROOT run's transition is a workflow event")
}
