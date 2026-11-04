package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/store"
	"github.com/reliant-labs/reliant/internal/store/storetest"
	"github.com/stretchr/testify/require"
)

// The held child lock is the barrier. PostgreSQL lock_timeout proves conflict
// without goroutine sleeps or polling; a missing child lock commits and fails.
func assertChildAdmissionLocks(t *testing.T, env *storetest.Env, id string, admit func(*sql.Tx) error) {
	t.Helper()
	ctx := context.Background()
	archive, err := env.SQL.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer archive.Rollback()
	_, err = archive.ExecContext(ctx, `SELECT id FROM worktrees WHERE id=$1 FOR UPDATE`, id)
	require.NoError(t, err)
	acts, err := store.WorktreeRunActivities(ctx, env.ORM, []string{id})
	require.NoError(t, err)
	require.False(t, acts[id].Running)
	tx, err := env.SQL.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `SET LOCAL lock_timeout='100ms'`)
	require.NoError(t, err)
	err = admit(tx)
	require.Error(t, err, "admission bypassed child worktree archive lock")
	var pg *pgconn.PgError
	require.True(t, errors.As(err, &pg), "expected PostgreSQL lock conflict: %v", err)
	require.Equal(t, "55P03", pg.Code)
}
func TestThreadWorkflowAdmissionSerializesWithChildArchive(t *testing.T) {
	for _, tc := range []struct {
		name        string
		state, stop int
	}{{"pending", 1, 0}, {"active", 2, 0}, {"paused", 3, 3}} {
		t.Run(tc.name, func(t *testing.T) {
			env := storetest.New(t)
			ctx := context.Background()
			chat := landedFixture(t, env, "parent-a")
			child := &core.Worktree{ID: "child-b", ProjectID: storetest.SeedProjectID, Name: "child-b", Path: "/tmp/child-b", Branch: "child"}
			require.NoError(t, env.Repo.CreateWorktree(ctx, child))
			mustExecConv(t, env, `INSERT INTO threads (id,chat_id,created_at,origin,status,worktree_id) VALUES ('child',$1,NOW(),'spawn',2,$2)`, chat.ID, child.ID)
			assertChildAdmissionLocks(t, env, child.ID, func(tx *sql.Tx) error {
				_, err := tx.ExecContext(ctx, `INSERT INTO workflows(id,chat_id,workflow_name,thread,created_at,state,stop_reason) VALUES ('wf',$1,'wf','child',NOW(),$2,$3)`, chat.ID, tc.state, tc.stop)
				return err
			})
			// Once archival releases the lock, the same run is admitted and the next
			// activity snapshot sees B, not merely its parent chat's workspace A.
			_, err := env.SQL.ExecContext(ctx, `INSERT INTO workflows(id,chat_id,workflow_name,thread,created_at,state,stop_reason) VALUES ('wf-after',$1,'wf','child',NOW(),$2,$3)`, chat.ID, tc.state, tc.stop)
			require.NoError(t, err)
			acts, err := store.WorktreeRunActivities(ctx, env.ORM, []string{child.ID})
			require.NoError(t, err)
			require.True(t, acts[child.ID].Running)
		})
	}
}
func TestLiveWorkflowTransitionsSerializeWithChildArchive(t *testing.T) {
	for _, state := range []int{1, 2, 3} {
		t.Run(fmt.Sprint(state), func(t *testing.T) {
			env := storetest.New(t)
			ctx := context.Background()
			chat := landedFixture(t, env, "transition-a")
			child := &core.Worktree{ID: "transition-b", ProjectID: storetest.SeedProjectID, Name: "transition-b", Path: "/tmp/b", Branch: "b"}
			require.NoError(t, env.Repo.CreateWorktree(ctx, child))
			mustExecConv(t, env, `INSERT INTO threads(id,chat_id,created_at,origin,status,worktree_id) VALUES ('child',$1,NOW(),'spawn',2,$2)`, chat.ID, child.ID)
			mustExecConv(t, env, `INSERT INTO workflows(id,chat_id,workflow_name,thread,created_at,state,stop_reason) VALUES ('stopped',$1,'wf','child',NOW(),3,1)`, chat.ID)
			assertChildAdmissionLocks(t, env, child.ID, func(tx *sql.Tx) error {
				stop := 0
				if state == 3 {
					stop = 3
				}
				_, err := tx.ExecContext(ctx, `UPDATE workflows SET state=$1,stop_reason=$2 WHERE id='stopped'`, state, stop)
				return err
			})
		})
	}
}

func TestThreadBindingAdmissionSerializesWithChildArchive(t *testing.T) {
	for _, rebind := range []bool{false, true} {
		t.Run(map[bool]string{false: "insert", true: "rebind"}[rebind], func(t *testing.T) {
			env := storetest.New(t)
			ctx := context.Background()
			chat := landedFixture(t, env, "parent-a")
			child := &core.Worktree{ID: "child-b", ProjectID: storetest.SeedProjectID, Name: "child-b", Path: "/tmp/child-b", Branch: "child"}
			require.NoError(t, env.Repo.CreateWorktree(ctx, child))
			// Existing pending/active/paused parent executions are conservative
			// scope too; adding their child binding must join B's archive lock.
			mustExecConv(t, env, `INSERT INTO workflows(id,chat_id,workflow_name,thread,created_at,state,stop_reason) VALUES ('parent-wf',$1,'wf','parent',NOW(),2,0)`, chat.ID)
			if rebind {
				mustExecConv(t, env, `INSERT INTO threads(id,chat_id,created_at,origin,status) VALUES ('child',$1,NOW(),'spawn',2)`, chat.ID)
			}
			assertChildAdmissionLocks(t, env, child.ID, func(tx *sql.Tx) error {
				if rebind {
					_, err := tx.ExecContext(ctx, `UPDATE threads SET worktree_id=$1 WHERE id='child'`, child.ID)
					return err
				}
				_, err := tx.ExecContext(ctx, `INSERT INTO threads(id,chat_id,created_at,origin,status,worktree_id) VALUES ('child',$1,NOW(),'spawn',2,$2)`, chat.ID, child.ID)
				return err
			})
		})
	}
}
