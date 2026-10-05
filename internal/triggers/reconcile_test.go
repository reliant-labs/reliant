// Copyright (c) 2025 Reliant Labs
package triggers

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db/core"
)

// projectionRepo is the slice of the store the reconciler uses.
type projectionRepo struct {
	mu       sync.Mutex
	triggers map[string]*core.Trigger
	written  map[string]core.TriggerProjection
}

func (r *projectionRepo) ListAllWorkflowTriggerActivations(context.Context) ([]*core.Trigger, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*core.Trigger
	for _, t := range r.triggers {
		if t.WorkflowTrigger != nil {
			copied := *t
			out = append(out, &copied)
		}
	}
	return out, nil
}

func (r *projectionRepo) SetTriggerProjection(_ context.Context, id string, p core.TriggerProjection) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.triggers[id]
	if !ok || t.WorkflowTrigger == nil {
		return core.ErrTriggerNotFound
	}
	t.Config, t.Filter = p.Config, p.Filter
	if r.written == nil {
		r.written = map[string]core.TriggerProjection{}
	}
	r.written[id] = p
	return nil
}

type recordingSync struct {
	mu     sync.Mutex
	synced []string
}

func (s *recordingSync) Sync(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.synced = append(s.synced, id)
	return nil
}

// Editing a declaration's cron re-projects every activation of it and
// reconverges its schedule, so the Temporal schedule follows the YAML.
func TestReconcileReprojectsChangedDeclarations(t *testing.T) {
	name := "nightly"
	stale := &core.Trigger{
		ID: "sched", UserID: "u1", ProjectID: "p1", Workflow: "triage", WorkflowTrigger: &name,
		Kind: core.TriggerKindSchedule, Config: json.RawMessage(`{"cron":["0 6 * * *"]}`),
	}
	issue := "new-issue"
	current := &core.Trigger{
		ID: "int", UserID: "u1", ProjectID: "p1", Workflow: "triage", WorkflowTrigger: &issue,
		Kind: core.TriggerKindIntegration, Filter: "trigger.payload.data.issue.number > 0",
		Config: json.RawMessage(`{"integration":"github","events":["issues.opened"]}`),
	}
	adhoc := &core.Trigger{ID: "adhoc", UserID: "u1", Kind: core.TriggerKindWebhook}
	repo := &projectionRepo{triggers: map[string]*core.Trigger{"sched": stale, "int": current, "adhoc": adhoc}}
	syncer := &recordingSync{}

	workflows := fakeWorkflows{yaml: map[string]string{"triage": declaringWorkflow}}
	report, err := NewReconciler(repo, workflows, syncer).ReconcileAll(context.Background())
	require.NoError(t, err)

	assert.Equal(t, 1, report.Updated, "only the stale projection is rewritten")
	assert.Equal(t, 2, report.Checked)
	assert.Equal(t, 0, report.Broken)
	require.Contains(t, repo.written, "sched")
	var cfg core.ScheduleConfig
	require.NoError(t, json.Unmarshal(repo.written["sched"].Config, &cfg))
	assert.Equal(t, []string{"0 9 * * *"}, cfg.Cron)
	assert.Equal(t, []string{"sched"}, syncer.synced, "a changed schedule is reconverged; an unchanged one is not")
}

// A broken activation is left as it is: re-projecting from nothing would
// erase the last schedule it was converged with, and its health and its
// firings already say it is broken.
func TestReconcileLeavesBrokenActivationsAlone(t *testing.T) {
	name := "gone"
	broken := &core.Trigger{
		ID: "b", UserID: "u1", ProjectID: "p1", Workflow: "triage", WorkflowTrigger: &name,
		Kind: core.TriggerKindSchedule, Config: json.RawMessage(`{"cron":["0 6 * * *"]}`),
	}
	repo := &projectionRepo{triggers: map[string]*core.Trigger{"b": broken}}
	syncer := &recordingSync{}
	report, err := NewReconciler(repo, fakeWorkflows{yaml: map[string]string{"triage": declaringWorkflow}}, syncer).ReconcileAll(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, report.Broken)
	assert.Empty(t, repo.written)
	assert.Empty(t, syncer.synced)
}

// A failure to READ a workflow is not a verdict: it is reported for the
// next pass and changes nothing.
func TestReconcileReportsLookupFailures(t *testing.T) {
	name := "nightly"
	trig := &core.Trigger{ID: "s", UserID: "u1", Workflow: "triage", WorkflowTrigger: &name, Kind: core.TriggerKindSchedule}
	repo := &projectionRepo{triggers: map[string]*core.Trigger{"s": trig}}
	_, err := NewReconciler(repo, fakeWorkflows{err: &lookupFailure{}}, &recordingSync{}).ReconcileAll(context.Background())
	require.Error(t, err)
	assert.Empty(t, repo.written)
}
