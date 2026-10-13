// Copyright (c) 2025 Reliant Labs
package workflow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Whoever resumes a chat — the user's message, or a machine connecting — ends
// the breaker's pause, so the mark must go with the resume. Left set, the
// connect handler would try to resume a run that is already running.
func TestResumeWorkflow_ClearsPausedForMachineMark(t *testing.T) {
	repo := newMockPauseRepo()
	repo.pausedForMachine = map[string]bool{"chat-1": true}
	ps := NewPauseService(&mockPauseTemporalClient{}, repo)

	require.NoError(t, ps.ResumeWorkflow(context.Background(), "wf-1", "chat-1", nil))

	assert.False(t, repo.pausedForMachine["chat-1"])
}

// A pause the user asks for is theirs to end. It must not carry the breaker's
// mark, or a machine connecting would undo it.
func TestPauseWorkflow_ClearsPausedForMachineMark(t *testing.T) {
	repo := newMockPauseRepo()
	repo.pausedForMachine = map[string]bool{"chat-1": true}
	ps := NewPauseService(&mockPauseTemporalClient{}, repo)

	require.NoError(t, ps.PauseWorkflow(context.Background(), "wf-1", "chat-1", "manual"))

	assert.False(t, repo.pausedForMachine["chat-1"])
}

// A failed resume signal leaves the mark alone: the chat is still paused, and
// the next connect or sweep retries.
func TestResumeWorkflow_FailedSignalKeepsPausedForMachineMark(t *testing.T) {
	repo := newMockPauseRepo()
	repo.pausedForMachine = map[string]bool{"chat-1": true}
	ps := NewPauseService(&mockPauseTemporalClient{signalErr: assert.AnError}, repo)

	require.Error(t, ps.ResumeWorkflow(context.Background(), "wf-1", "chat-1", nil))

	assert.True(t, repo.pausedForMachine["chat-1"])
}
