// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"database/sql"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/stretchr/testify/require"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/questions"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/triggers/runevents"
)

// One assistant turn that makes N tool calls needing approval (or asks N
// questions) runs N ApprovalCreate (QuestionCreate) activities at once on one
// chat, each writing the chats row and the chat's update counters. In prod a
// turn of 13 approvals lost 5 of them to 40001: under SERIALIZABLE every
// commit on the chats row aborted each writer waiting on it, and 7 attempts
// were not enough. These tests pin that a create WAITS OUT a concurrent
// writer of the chat row in one transaction, instead of being aborted and
// retried.
//
// They are deterministic: a rival transaction holds the chats row before the
// activity starts, the test waits until the activity is blocked on it, and
// only then commits. At SERIALIZABLE that commit always aborts the waiter
// (its snapshot predates the rival's row version), so the activity's
// transaction body runs twice; at READ COMMITTED it runs once.

// attemptCountingRepo counts how many times the activity's own transaction
// body runs. The Repo's helpers open (and join) transactions on the embedded
// Repo directly, so only the activity's outermost RunTx is counted.
type attemptCountingRepo struct {
	*db.Repo
	attempts atomic.Int32
}

func (r *attemptCountingRepo) RunTx(ctx context.Context, policy orm.TxPolicy, fn func(context.Context) error) error {
	return r.Repo.RunTx(ctx, policy, func(ctx context.Context) error {
		r.attempts.Add(1)
		return fn(ctx)
	})
}

func TestApprovalCreate_WaitsOutAConcurrentChatWriterInOneTransaction(t *testing.T) {
	t.Parallel()

	f := newRunEventFixture(t, true)
	repo := &attemptCountingRepo{Repo: f.h.Repo()}
	var out ApprovalCreateOutput
	assertCreateWaitsOutChatWriter(t, f, repo, func() error {
		return f.h.ExecuteActivity(NewApprovalCreateActivity(approvalCreateDeps(repo)).Execute, ApprovalCreateInput{
			ChatID: f.chatID, WorkflowID: f.chatID, TemporalWorkflowID: f.chatID, StepID: "gate", Title: "Turn on the lane?",
		}, &out)
	})

	approval, err := f.h.Repo().GetApproval(context.Background(), out.ApprovalID)
	require.NoError(t, err)
	require.Equal(t, int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING), approval.Status)
}

func TestQuestionCreate_WaitsOutAConcurrentChatWriterInOneTransaction(t *testing.T) {
	t.Parallel()

	f := newRunEventFixture(t, true)
	repo := &attemptCountingRepo{Repo: f.h.Repo()}
	metadata := `{"type":"ask_user","questions":[{"question":"Which region?","options":[]}]}`
	var out QuestionCreateOutput
	assertCreateWaitsOutChatWriter(t, f, repo, func() error {
		return f.h.ExecuteActivity(NewQuestionCreateActivity(repo, questions.NewFromRepo(repo.Repo), store.NewQuestionExtStore(repo.ORM()), runevents.NewStore(repo.ORM())).Execute, QuestionCreateInput{
			ChatID: f.chatID, WorkflowID: f.chatID, TemporalWorkflowID: f.chatID, ThreadID: f.chatID, StepID: "ask", Metadata: &metadata,
		}, &out)
	})

	question, err := f.h.Repo().GetQuestionByID(context.Background(), out.QuestionID)
	require.NoError(t, err)
	require.Equal(t, db.QuestionStatusPending, question.Status)
}

// assertCreateWaitsOutChatWriter runs create while a rival transaction, as a
// sibling create of the same turn would, holds a pending approval and an
// update of the chat row. It commits the rival once create is blocked on that
// row, then requires create to succeed in the transaction that waited, and
// to see the rival's committed approval as already blocking the run, so it
// records no second "blocked" event.
func assertCreateWaitsOutChatWriter(t *testing.T, f *runEventFixture, repo *attemptCountingRepo, create func() error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rawDB := f.h.DB()

	rival, err := rawDB.BeginTx(ctx, nil) // READ COMMITTED
	require.NoError(t, err)
	defer func() { _ = rival.Rollback() }()
	_, err = rival.ExecContext(ctx, `
		INSERT INTO approvals (id, chat_id, approval_type, entity_id, status, title, created_at)
		VALUES ($1, $2, $3, $4, $5, 'rival', now())`,
		uuid.NewString(), f.chatID, int32(reliantv1.ApprovalType_APPROVAL_TYPE_TOOL_CALL),
		"rival:"+uuid.NewString(), int32(reliantv1.ApprovalStatus_APPROVAL_STATUS_PENDING))
	require.NoError(t, err)
	_, err = rival.ExecContext(ctx, `UPDATE chats SET unread = 1 WHERE id = $1`, f.chatID)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() { done <- create() }()

	// The database belongs to this test and the rival only holds its locks,
	// so the activity is the only backend that can wait on one.
	if !awaitLockWaiter(ctx, t, rawDB, done) {
		t.Fatal("the create finished without waiting on the rival's chat row, so this run proved nothing")
	}
	require.NoError(t, rival.Commit())

	select {
	case err = <-done:
	case <-ctx.Done():
		t.Fatalf("the create did not finish once the rival committed: %v", ctx.Err())
	}
	require.NoError(t, err, "the create failed after waiting on a concurrent writer of its chat")
	require.Equal(t, int32(1), repo.attempts.Load(),
		"the create's transaction ran %d times: a concurrent commit on the chat row aborted it "+
			"(40001) instead of being waited out, which is how a turn's parallel approvals fail",
		repo.attempts.Load())
	require.Empty(t, f.events(t),
		"the rival's pending approval already blocked the run; the create must see it after taking the chat lock")
}

// awaitLockWaiter polls until a backend of this test's database waits on a
// lock (true), or create returns first (false, with its result put back).
func awaitLockWaiter(ctx context.Context, t *testing.T, rawDB *sql.DB, done chan error) bool {
	t.Helper()
	for {
		select {
		case err := <-done:
			done <- err
			return false
		case <-ctx.Done():
			t.Fatalf("the create neither blocked on the rival's chat row nor finished: %v", ctx.Err())
		default:
		}
		var waiting int
		require.NoError(t, rawDB.QueryRowContext(ctx, `
			SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database()
			  AND wait_event_type = 'Lock'
			  AND pid <> pg_backend_pid()`).Scan(&waiting))
		if waiting > 0 {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
}
