package project_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/checkoutteardown"
	"github.com/reliant-labs/reliant/internal/db"
	project "github.com/reliant-labs/reliant/internal/handlers/project"
	"github.com/reliant-labs/reliant/internal/toolexec"
)

// scriptedDaemon answers each command in turn from answers, recording what it
// was sent.
type scriptedDaemon struct {
	mu      sync.Mutex
	answers []func(commandType string, payload map[string]string) ([]byte, error)
	sent    []string
	opIDs   []string
}

func (d *scriptedDaemon) answer(commandType string, raw []byte) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	var payload map[string]string
	_ = json.Unmarshal(raw, &payload)
	d.sent = append(d.sent, commandType)
	d.opIDs = append(d.opIDs, payload["op_id"])
	if len(d.answers) == 0 {
		return nil, fmt.Errorf("unexpected daemon command %s", commandType)
	}
	next := d.answers[0]
	d.answers = d.answers[1:]
	return next(commandType, payload)
}

func reply(body string) func(string, map[string]string) ([]byte, error) {
	return func(_ string, p map[string]string) ([]byte, error) {
		return []byte(strings.ReplaceAll(body, "%s", p["op_id"])), nil
	}
}

func fail(err error) func(string, map[string]string) ([]byte, error) {
	return func(string, map[string]string) ([]byte, error) { return nil, err }
}

var outdated = svcerr.FailedPrecondition(`this machine is running an older version of Reliant (v1.0.0) that doesn't support "git.teardown_status" — update it to continue`)

func remove(t *testing.T, svc *project.Service) error {
	t.Helper()
	_, err := svc.RemoveProjectFromDaemon(project.AuthedContext(t),
		connect.NewRequest(&pb.RemoveProjectFromDaemonRequest{ProjectId: "p-1", DaemonId: "d-1"}))
	return err
}

func reclone(t *testing.T, svc *project.Service) error {
	t.Helper()
	_, err := svc.RecloneProjectOnDaemon(project.AuthedContext(t),
		connect.NewRequest(&pb.RecloneProjectOnDaemonRequest{ProjectId: "p-1", DaemonId: "d-1"}))
	return err
}

func teardownStore(t *testing.T) (*project.MockRepository, map[string]*db.CheckoutTeardown, *int) {
	t.Helper()
	store := onDaemon(t)
	book := withTeardowns(store)
	deleted := 0
	store.DeleteProjectDaemonFunc = func(context.Context, string, string) error { deleted++; return nil }
	store.UpsertProjectDaemonFunc = func(context.Context, string, string, string, *string) error { return nil }
	return store, book, &deleted
}

// The daemon acknowledges the op it was sent: done settles it and the
// checkout row goes with it.
func TestRemove_AnAcknowledgedRemoveSettlesTheOp(t *testing.T) {
	t.Parallel()
	store, book, deleted := teardownStore(t)
	d := &scriptedDaemon{answers: []func(string, map[string]string) ([]byte, error){
		reply(`{"success":true,"removed":true,"op_id":"%s","op_state":"done"}`),
	}}
	require.NoError(t, remove(t, newService(t, store, fakeDaemon(d.answer))))
	require.Len(t, book, 1)
	assert.Equal(t, db.CheckoutTeardownDone, book["op-1"].State)
	assert.Equal(t, []string{"op-1"}, d.opIDs, "the command carries the recorded op's id")
	assert.Equal(t, 1, *deleted)
}

// The answer is lost (the server stopped waiting). The op stays pending —
// workspace creation stays held — and the retry sends the SAME op id, which
// the machine answers from its journal instead of deleting again.
func TestRemove_ALostAckIsRetriedWithTheSameOp(t *testing.T) {
	t.Parallel()
	store, book, deleted := teardownStore(t)
	d := &scriptedDaemon{answers: []func(string, map[string]string) ([]byte, error){
		fail(fmt.Errorf("send: %w", toolexec.ErrDaemonTimeout)),
		reply(`{"success":true,"removed":true,"op_id":"%s","op_state":"done"}`),
	}}
	svc := newService(t, store, fakeDaemon(d.answer))

	err := remove(t, svc)
	require.Error(t, err)
	assert.Equal(t, connect.CodeDeadlineExceeded, svcerr.Code(err))
	assert.Contains(t, svcerr.ToConnect(err).Message(), "still running")
	assert.Equal(t, db.CheckoutTeardownPending, book["op-1"].State, "creation stays held while the outcome is unknown")
	assert.Zero(t, *deleted)

	require.NoError(t, remove(t, svc))
	assert.Equal(t, []string{"op-1", "op-1"}, d.opIDs, "the retry reuses the op")
	assert.Len(t, book, 1)
	assert.Equal(t, db.CheckoutTeardownDone, book["op-1"].State)
	assert.Equal(t, 1, *deleted)
}

// The machine reports the op still running (it answered after the server's
// wait ended for a slow reclone): pending, and said so.
func TestReclone_StillRunningStaysPending(t *testing.T) {
	t.Parallel()
	store, book, _ := teardownStore(t)
	d := &scriptedDaemon{answers: []func(string, map[string]string) ([]byte, error){
		reply(`{"success":false,"op_id":"%s","op_state":"running"}`),
	}}
	err := reclone(t, newService(t, store, fakeDaemon(d.answer)))
	assert.Equal(t, connect.CodeDeadlineExceeded, svcerr.Code(err))
	assert.Equal(t, db.CheckoutTeardownPending, book["op-1"].State)
}

// A daemon from before op ids ignores the id and answers the old way: that
// reply is its synchronous outcome, so the op settles from it exactly as the
// remove behaved before.
func TestRemove_AnOlderDaemonsReplyIsItsOutcome(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		body      string
		wantState string
		wantErr   bool
	}{
		"removed":       {body: `{"success":true,"removed":true}`, wantState: db.CheckoutTeardownDone},
		"remove failed": {body: `{"success":false,"error":"permission denied"}`, wantState: db.CheckoutTeardownFailed, wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store, book, deleted := teardownStore(t)
			d := &scriptedDaemon{answers: []func(string, map[string]string) ([]byte, error){reply(tc.body)}}
			err := remove(t, newService(t, store, fakeDaemon(d.answer)))
			assert.Equal(t, tc.wantErr, err != nil, "err = %v", err)
			assert.Equal(t, tc.wantState, book["op-1"].State)
			if tc.wantErr {
				assert.Contains(t, svcerr.ToConnect(err).Message(), "permission denied")
				assert.Zero(t, *deleted)
			} else {
				assert.Equal(t, 1, *deleted)
			}
		})
	}
}

// An older daemon whose reply was lost cannot be asked how it ended
// (git.teardown_status is an unknown command there). The next teardown of the
// checkout is refused until LegacyGrace has passed — the timeout fallback —
// and then proceeds the old way.
func TestReclone_AnOlderDaemonsLostReplyFallsBackToTheGrace(t *testing.T) {
	t.Parallel()
	store, book, _ := teardownStore(t)
	d := &scriptedDaemon{answers: []func(string, map[string]string) ([]byte, error){
		fail(fmt.Errorf("send: %w", toolexec.ErrDaemonTimeout)), // the reclone's reply is lost
		fail(outdated), // the remove asks how the reclone ended: unknown command
	}}
	svc := newService(t, store, fakeDaemon(d.answer))
	require.Error(t, reclone(t, svc))
	require.Equal(t, db.CheckoutTeardownPending, book["op-1"].State)

	err := remove(t, svc)
	require.Error(t, err)
	assert.Contains(t, svcerr.ToConnect(err).Message(), "has not finished yet")
	assert.Equal(t, db.CheckoutTeardownPending, book["op-1"].State, "inside the grace an old machine's op still holds")

	// Past the grace the op expires, and the remove goes ahead.
	book["op-1"].CreatedAt = book["op-1"].CreatedAt.Add(-checkoutteardown.LegacyGrace - time.Minute)
	d.answers = append(d.answers, fail(outdated), reply(`{"success":true,"removed":true}`))
	require.NoError(t, remove(t, svc))
	assert.Equal(t, db.CheckoutTeardownExpired, book["op-1"].State)
	assert.Equal(t, db.CheckoutTeardownDone, book["op-2"].State)
	assert.Equal(t, []string{"git.reclone", "git.teardown_status", "git.teardown_status", "git.remove"}, d.sent)
}

// Workspaces of the checkout refuse the teardown before anything is recorded
// or sent.
func TestRemove_RefusedWhileWorkspacesLiveThere(t *testing.T) {
	t.Parallel()
	store, book, _ := teardownStore(t)
	daemonID := "d-1"
	store.ListWorktreesFunc = func(context.Context, db.WorktreeFilters) ([]*db.Worktree, error) {
		return []*db.Worktree{{ID: "w", DaemonID: &daemonID}}, nil
	}
	d := &scriptedDaemon{}
	err := remove(t, newService(t, store, fakeDaemon(d.answer)))
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	assert.Contains(t, svcerr.ToConnect(err).Message(), "archive them")
	assert.Empty(t, book)
	assert.Empty(t, d.sent)
}
