package project_test

import (
	"context"
	"encoding/json"
	"errors"
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
// workspace creation stays held — and the retry first asks the machine how it
// ended: still running there, so the SAME op id is sent again, which the
// machine answers from its journal instead of deleting again.
func TestRemove_ALostAckIsRetriedWithTheSameOp(t *testing.T) {
	t.Parallel()
	store, book, deleted := teardownStore(t)
	d := &scriptedDaemon{answers: []func(string, map[string]string) ([]byte, error){
		fail(fmt.Errorf("send: %w", toolexec.ErrDaemonTimeout)),
		reply(`{"op_id":"%s","op_state":"running"}`),
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
	assert.Equal(t, []string{"git.remove", "git.teardown_status", "git.remove"}, d.sent, "the retry asks before it sends")
	assert.Equal(t, []string{"op-1", "op-1", "op-1"}, d.opIDs, "the retry reuses the op")
	assert.Len(t, book, 1)
	assert.Equal(t, db.CheckoutTeardownDone, book["op-1"].State)
	assert.Equal(t, 1, *deleted)
}

// A retry of a remove the machine has meanwhile finished sends nothing: the
// status answer settles it, and the row goes with it.
func TestRemove_ARetryOfAFinishedRemoveSendsNothing(t *testing.T) {
	t.Parallel()
	store, book, deleted := teardownStore(t)
	d := &scriptedDaemon{answers: []func(string, map[string]string) ([]byte, error){
		fail(fmt.Errorf("send: %w", toolexec.ErrDaemonTimeout)),
		reply(`{"op_id":"%s","op_state":"done","removed":true}`),
	}}
	svc := newService(t, store, fakeDaemon(d.answer))
	require.Error(t, remove(t, svc))
	require.NoError(t, remove(t, svc))
	assert.Equal(t, []string{"git.remove", "git.teardown_status"}, d.sent)
	assert.Equal(t, db.CheckoutTeardownDone, book["op-1"].State)
	assert.Equal(t, 1, *deleted)
}

// Delivery succeeded but the transport failed on the way back (a dropped
// connection, not a timeout): that is no answer from the machine, which may be
// deleting, so the op stays pending. The machine, asked, says it is still
// running: still pending.
func TestRemove_ATransportFailureIsNoOutcome(t *testing.T) {
	t.Parallel()
	store, book, deleted := teardownStore(t)
	d := &scriptedDaemon{answers: []func(string, map[string]string) ([]byte, error){
		fail(errors.New("daemon command git.remove via NATS failed: nats: connection closed")),
		reply(`{"op_id":"%s","op_state":"running"}`),
		reply(`{"success":false,"op_id":"%s","op_state":"running"}`),
	}}
	svc := newService(t, store, fakeDaemon(d.answer))

	err := remove(t, svc)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, svcerr.Code(err))
	assert.Contains(t, svcerr.ToConnect(err).Message(), "answer was lost")
	assert.NotContains(t, svcerr.ToConnect(err).Message(), "nats", "the transport's words stay off the wire")
	assert.Equal(t, db.CheckoutTeardownPending, book["op-1"].State, "a lost answer releases nothing")

	err = remove(t, svc)
	require.Error(t, err)
	assert.Equal(t, db.CheckoutTeardownPending, book["op-1"].State, "the machine says running: still held")
	assert.Zero(t, *deleted)
}

// The daemon's own failure IS an outcome: it reported it after (an older
// daemon) or instead of (one that knows ids) running the delete.
func TestRemove_ADaemonReportedFailureSettlesTheOpFailed(t *testing.T) {
	t.Parallel()
	store, book, deleted := teardownStore(t)
	d := &scriptedDaemon{answers: []func(string, map[string]string) ([]byte, error){
		fail(toolexec.DaemonReportedFailure("git.remove", "refusing to remove non-clone path (no .git): /clone")),
	}}
	err := remove(t, newService(t, store, fakeDaemon(d.answer)))
	require.Error(t, err)
	assert.Contains(t, svcerr.ToConnect(err).Message(), "refusing to remove non-clone path")
	assert.Equal(t, db.CheckoutTeardownFailed, book["op-1"].State)
	assert.Zero(t, *deleted)
}

// An older daemon whose reply to a remove was lost: the retry asks first,
// learns the machine cannot say (git.teardown_status is unknown there), and
// sends nothing until the grace has passed — an older daemon does not dedupe,
// so a resend would be a second delete beside a first that may still run.
// Only after the grace does a single new remove go out.
func TestRemove_AnOlderDaemonsSameKindRetrySendsOneDeleteAtATime(t *testing.T) {
	t.Parallel()
	store, book, _ := teardownStore(t)
	d := &scriptedDaemon{answers: []func(string, map[string]string) ([]byte, error){
		fail(fmt.Errorf("send: %w", toolexec.ErrDaemonTimeout)), // the remove's reply is lost
		fail(outdated), // the retry asks how it ended: unknown command
	}}
	svc := newService(t, store, fakeDaemon(d.answer))
	require.Error(t, remove(t, svc))

	err := remove(t, svc)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	assert.Contains(t, svcerr.ToConnect(err).Message(), "too old to confirm")
	assert.Equal(t, []string{"git.remove", "git.teardown_status"}, d.sent, "one destructive command inside the grace")
	assert.Equal(t, db.CheckoutTeardownPending, book["op-1"].State)

	book["op-1"].CreatedAt = book["op-1"].CreatedAt.Add(-checkoutteardown.LegacyGrace - time.Minute)
	d.answers = append(d.answers, fail(outdated), reply(`{"success":true,"removed":true}`))
	require.NoError(t, remove(t, svc))
	assert.Equal(t, []string{"git.remove", "git.teardown_status", "git.teardown_status", "git.remove"}, d.sent)
	assert.Equal(t, db.CheckoutTeardownExpired, book["op-1"].State)
	assert.Equal(t, db.CheckoutTeardownDone, book["op-2"].State)
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
	assert.Contains(t, svcerr.ToConnect(err).Message(), "too old to confirm")
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
