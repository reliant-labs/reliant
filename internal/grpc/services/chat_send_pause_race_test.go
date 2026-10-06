// Copyright (c) 2025 Reliant Labs
package services

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/runs"
	"github.com/reliant-labs/reliant/internal/threads"
)

// gatedPauseController behaves like workflow.PauseService in the one respect
// this test is about: PauseWorkflow puts the pause signal into Temporal FIRST
// and only then writes the paused status. The test holds the pause between
// those two steps, which is exactly where the real one spent ~140ms on chat
// 264b5697 (signal 20:57:00.721, paused row visible ~.865).
type gatedPauseController struct {
	repo *db.Repo

	signalled chan struct{} // closed once the pause signal is "in Temporal"
	release   chan struct{} // closed by the test to let the status write land

	mu    sync.Mutex
	calls []string
}

func newGatedPauseController(repo *db.Repo) *gatedPauseController {
	return &gatedPauseController{
		repo:      repo,
		signalled: make(chan struct{}),
		release:   make(chan struct{}),
	}
}

func (p *gatedPauseController) record(call string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, call)
}

func (p *gatedPauseController) recorded() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

func (p *gatedPauseController) PauseWorkflow(ctx context.Context, workflowID, _, _ string) error {
	p.record("pause")
	close(p.signalled)
	<-p.release
	return p.repo.UpdateWorkflowStatus(ctx, workflowID, db.Paused())
}

func (p *gatedPauseController) ResumeWorkflow(ctx context.Context, workflowID, _ string) error {
	p.record("resume")
	return p.repo.UpdateWorkflowStatus(ctx, workflowID, db.Active())
}

func (p *gatedPauseController) ResumeInterruptedWorkflow(context.Context, string, string) (string, error) {
	return "", errors.New("a paused run is live; nothing here should reset it")
}

func (p *gatedPauseController) SignalWithRecovery(context.Context, string, string, interface{}) error {
	return nil
}

// advisoryLockWaiter closes the returned channel once some session in this
// test's database is blocked waiting for an advisory lock, i.e. once a request
// has parked behind another one's run-control critical section.
func advisoryLockWaiter(t *testing.T, sqlDB *sql.DB) <-chan struct{} {
	t.Helper()
	parked := make(chan struct{})
	go func() {
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for range ticker.C {
			var waiting int
			err := sqlDB.QueryRow(`
				SELECT count(*) FROM pg_locks
				WHERE locktype = 'advisory' AND NOT granted
				  AND database = (SELECT oid FROM pg_database WHERE datname = current_database())`,
			).Scan(&waiting)
			if err != nil {
				return // the test database is gone; the test is over
			}
			if waiting > 0 {
				close(parked)
				return
			}
		}
	}()
	return parked
}

// TestSendMessage_RacingAnInFlightPauseResumesTheRun is the regression for
// chat 264b5697-4ef6-4bc3-a60b-ed47602ad4e9.
//
// The user pressed ESC and sent a message ~420ms later. The frontend does not
// wait for PauseChat before sending, so both requests were in flight together:
//
//	20:57:00.415  PauseChat starts (cancels tool calls first)
//	20:57:00.721  pause signal sent to Temporal
//	20:57:00.838  SendMessage starts and reads the workflow row: still Active
//	~20:57:00.86  PauseChat writes the row Paused
//	20:57:01.012  SendMessage saves the message on the "running" branch
//	20:57:04.761  ...and only rings the thread-wake doorbell
//
// A wake does not release the pause gate, so the run stayed parked with the
// user's message unread until a second message ("?") at 21:01:37 found the
// row Paused and resumed it.
//
// SendMessage routed on a status a pause that started BEFORE it had already
// made stale. The send must observe any pause the server received first.
func TestSendMessage_RacingAnInFlightPauseResumesTheRun(t *testing.T) {
	repo, cleanup := db.SetupTestDB(t)
	t.Cleanup(cleanup)

	ctx, fx := setupAbsorbFixture(t, repo, "test-user", db.Active())
	temporal := &wakeTestTemporalClient{
		absorbTestTemporalClient: absorbTestTemporalClient{
			exists: true, status: enums.WORKFLOW_EXECUTION_STATUS_RUNNING,
		},
	}
	pause := newGatedPauseController(repo)
	service := &ChatService{
		database:   repo,
		tempClient: temporal,
		runs:       runs.NewService(repo, temporal, pause),
		threads:    threads.NewService(repo, threads.WithToolCanceler(&fakeDaemonRouter{})),
	}

	pauseDone := make(chan error, 1)
	go func() {
		_, err := service.PauseChat(ctx, connect.NewRequest(&reliantv1.PauseChatRequest{ChatId: fx.chatID}))
		pauseDone <- err
	}()

	select {
	case <-pause.signalled:
	case <-time.After(10 * time.Second):
		t.Fatal("PauseChat never reached the pause signal")
	}

	// The pause is now in Temporal but its status write has not landed.
	sendReq := sendMessageRequest(t, fx.chatID, "also takes ~1-2 seconds for new chat to go from new chat screen to live screen")
	sendDone := make(chan error, 1)
	go func() {
		_, err := service.SendMessage(ctx, sendReq)
		sendDone <- err
	}()

	// Hold the pause in flight until the send has committed to a route: it
	// either finished (it routed on the stale row without waiting) or it is
	// parked behind the pause. The timeout only bounds an implementation that
	// serializes some other way; releasing then is still a valid interleaving.
	var sendErr error
	sendReturned := false
	select {
	case sendErr = <-sendDone:
		sendReturned = true
		t.Log("send finished while the pause was still in flight")
	case <-advisoryLockWaiter(t, repo.DB.SQLDB()):
		t.Log("send parked behind the in-flight pause")
	case <-time.After(5 * time.Second):
		t.Log("send neither finished nor parked on an advisory lock within 5s")
	}

	close(pause.release)
	require.NoError(t, <-pauseDone)
	if !sendReturned {
		sendErr = <-sendDone
	}
	require.NoError(t, sendErr)

	assert.Equal(t, []string{"pause", "resume"}, pause.recorded(),
		"a message sent after a pause the server had already received must resume the run; "+
			"routing it as 'running' only rings the thread-wake doorbell, which never releases the pause gate")

	wf, err := repo.GetWorkflow(ctx, fx.rootThreadID)
	require.NoError(t, err)
	assert.Equal(t, db.Active(), wf.Status,
		"the run the message resumed must read as running, not paused")
}
