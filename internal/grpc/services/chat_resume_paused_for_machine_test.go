// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/runs"
)

// countingPause records resumes and, like PauseService, marks the run running.
type countingPause struct {
	resetPause
	resumeErr error
	rmu       sync.Mutex
	resumes   int
}

func (p *countingPause) ResumeWorkflow(ctx context.Context, workflowID, _ string, _ map[string]interface{}) error {
	if p.resumeErr != nil {
		return p.resumeErr
	}
	p.rmu.Lock()
	p.resumes++
	p.rmu.Unlock()
	return p.repo.UpdateWorkflowStatus(ctx, workflowID, db.Active())
}

func (p *countingPause) resumeCount() int {
	p.rmu.Lock()
	defer p.rmu.Unlock()
	return p.resumes
}

type pausedFixture struct {
	continueFixture
	pause *countingPause
}

// newPausedFixture is a chat whose root run is PAUSED in the database and
// parked in Temporal.
func newPausedFixture(t *testing.T) pausedFixture {
	t.Helper()
	f := newContinueFixture(t, true)
	require.NoError(t, f.repo.UpdateWorkflowStatus(f.ctx, f.chatID, db.Paused()))
	pause := &countingPause{resetPause: resetPause{repo: f.repo, replays: true}}
	f.temporal.status = 1 // RUNNING
	f.svc = &ChatService{database: f.repo, tempClient: f.temporal, runs: runs.NewService(f.repo, f.temporal, pause)}
	return pausedFixture{continueFixture: f, pause: pause}
}

func (f pausedFixture) markBreakerPause(t *testing.T) {
	t.Helper()
	changed, err := f.repo.SetChatPausedForMachine(f.ctx, f.chatID, true)
	require.NoError(t, err)
	require.True(t, changed)
}

func (f pausedFixture) isMarked(t *testing.T) bool {
	t.Helper()
	paused, err := f.repo.ListChatsPausedForMachine(f.ctx, "", 100)
	require.NoError(t, err)
	for _, c := range paused {
		if c.ChatID == f.chatID {
			return true
		}
	}
	return false
}

func (f pausedFixture) systemNotes(t *testing.T) int {
	t.Helper()
	n := 0
	msgs, err := f.repo.ListMessages(f.ctx, f.chatID, db.MessageListOptions{})
	require.NoError(t, err)
	for _, m := range msgs {
		if m.Role != reliantv1.MessageRole_MESSAGE_ROLE_SYSTEM {
			continue
		}
		blocks, err := f.repo.ListContentBlocks(f.ctx, m.ID)
		require.NoError(t, err)
		for _, b := range blocks {
			if b.Content != nil && *b.Content == machineBackNote {
				n++
			}
		}
	}
	return n
}

// A chat the breaker paused is resumed when its machine is back, and the agent
// is told. However many deliverers race for it, it resumes once.
func TestResumePausedForMachine_ResumesOnceAndTellsTheAgent(t *testing.T) {
	f := newPausedFixture(t)
	f.markBreakerPause(t)

	var wg sync.WaitGroup
	var mu sync.Mutex
	resumed := 0
	for range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := f.svc.ResumePausedForMachine(f.ctx, f.chatID)
			assert.NoError(t, err)
			if ok {
				mu.Lock()
				resumed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, 1, resumed)
	assert.Equal(t, 1, f.pause.resumeCount())
	assert.Equal(t, 1, f.systemNotes(t), "the agent is told once that its machine is back")
	assert.False(t, f.isMarked(t))
}

// A chat the user paused carries no mark: a machine connecting leaves it alone.
func TestResumePausedForMachine_UserPausedChatStaysPaused(t *testing.T) {
	f := newPausedFixture(t)

	ok, err := f.svc.ResumePausedForMachine(f.ctx, f.chatID)

	require.NoError(t, err)
	assert.False(t, ok)
	assert.Zero(t, f.pause.resumeCount())
	assert.Zero(t, f.systemNotes(t))
	wf, err := f.repo.GetWorkflow(f.ctx, f.chatID)
	require.NoError(t, err)
	assert.Equal(t, db.Paused(), wf.Status)
}

// The user retried before the machine came back: the run is no longer paused,
// so a stale mark is dropped and nothing is resumed.
func TestResumePausedForMachine_AlreadyResumedByTheUserDropsTheMark(t *testing.T) {
	f := newPausedFixture(t)
	f.markBreakerPause(t)
	require.NoError(t, f.repo.UpdateWorkflowStatus(f.ctx, f.chatID, db.Active()))

	ok, err := f.svc.ResumePausedForMachine(f.ctx, f.chatID)

	require.NoError(t, err)
	assert.False(t, ok)
	assert.Zero(t, f.pause.resumeCount())
	assert.False(t, f.isMarked(t))
}

// A resume that fails leaves the chat marked, so the next connect or the sweep
// tries again rather than stranding it.
func TestResumePausedForMachine_FailedResumeKeepsTheMark(t *testing.T) {
	f := newPausedFixture(t)
	f.markBreakerPause(t)
	f.pause.resumeErr = errors.New("temporal unavailable")

	ok, err := f.svc.ResumePausedForMachine(f.ctx, f.chatID)

	require.Error(t, err)
	assert.False(t, ok)
	assert.True(t, f.isMarked(t))
}
