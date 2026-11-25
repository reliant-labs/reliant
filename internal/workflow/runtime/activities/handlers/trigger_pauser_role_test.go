// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/store"
)

type recordingPauser struct{ calls []string }

func (p *recordingPauser) SystemPauseTrigger(_ context.Context, id, reason string) (bool, error) {
	p.calls = append(p.calls, id+":"+reason)
	return true, nil
}

// The activity pauses through its own role, not through whatever the store
// happens to implement: the repository facade has no pause, and the generated
// writes store is the pauser.
func TestWorkflowStatusPausesThroughItsPauserRole(t *testing.T) {
	var _ TriggerPauser = store.TriggerWritesExtStore(nil)
	var facade any = (*db.Repo)(nil)
	_, facadePauses := facade.(TriggerPauser)
	assert.False(t, facadePauses, "*db.Repo must not satisfy TriggerPauser")

	pauser := &recordingPauser{}
	a := NewWorkflowStatusActivity(WorkflowStatusDeps{Pauser: pauser})
	assert.Same(t, pauser, a.pauser)
}

// No pauser, no pause, and no panic: a refused run only records its failure.
func TestWorkflowStatusWithoutAPauserPausesNothing(t *testing.T) {
	a := NewWorkflowStatusActivity(WorkflowStatusDeps{})
	assert.Nil(t, a.pauser)
	a.pauseTriggerRefusedByPolicy(context.Background(), WorkflowStatusInput{LaunchRun: true, Status: "failed", Error: "org policy"})
}
