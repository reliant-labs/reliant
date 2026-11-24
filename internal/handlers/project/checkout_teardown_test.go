package project_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"
	"github.com/reliant-labs/forge/pkg/testkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/reliant-labs/reliant/gen/reliant/v1"
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

func teardownStore(t *testing.T) (*stubRepo, map[string]*db.CheckoutTeardown, *int) {
	t.Helper()
	store := onDaemon(t)
	book := withTeardowns(store)
	deleted := 0
	store.DeleteProjectDaemonFunc = func(context.Context, string, string) error { deleted++; return nil }
	store.UpsertProjectDaemonFunc = func(context.Context, string, string, string, *string) error { return nil }
	return store, book, &deleted
}

// A caller who does not own the project can neither remove nor reclone its
// checkout: nothing is recorded, nothing is sent to the machine, and no row
// goes. The project reads as missing, so the answer never says it exists.
func TestTeardown_AnotherUsersProjectIsNeverTornDown(t *testing.T) {
	t.Parallel()
	store, book, deleted := teardownStore(t)
	d := &scriptedDaemon{}
	svc := newService(t, store, fakeDaemon(d.answer))
	stranger := project.AuthedContext(t, testkit.WithUserID("someone-else"))

	_, err := svc.RemoveProjectFromDaemon(stranger,
		connect.NewRequest(&pb.RemoveProjectFromDaemonRequest{ProjectId: "p-1", DaemonId: "d-1"}))
	require.NoError(t, err, "a project the caller cannot see is already gone, as far as they can tell")
	_, err = svc.RecloneProjectOnDaemon(stranger,
		connect.NewRequest(&pb.RecloneProjectOnDaemonRequest{ProjectId: "p-1", DaemonId: "d-1"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, svcerr.Code(err))

	assert.Empty(t, d.sent, "nothing reaches the owner's machine")
	assert.Empty(t, book, "no teardown is recorded, so the owner's workspace creation is not held")
	assert.Zero(t, *deleted)
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

// guardRefusal is what the daemon's workspace guard replies with when $HOME
// changed under a command — possibly AFTER the handler started a detached
// teardown (daemonruntime handleDaemonCommand).
func guardRefusal(command string) func(string, map[string]string) ([]byte, error) {
	return fail(toolexec.DaemonReportedFailure(command, "the workspace directory changed while this ran"))
}

// A daemon too old to know op ids cannot say how a teardown ended
// (git.teardown_status is an unknown command there): the remove is refused
// with the fix, and the op holds until the machine is updated.
func TestRemove_AnOutdatedDaemonIsRefusedAndTheOpHolds(t *testing.T) {
	t.Parallel()
	for name, answers := range map[string][]func(string, map[string]string) ([]byte, error){
		"a reported failure": {
			fail(toolexec.DaemonReportedFailure("git.remove", "refusing to remove non-clone path (no .git): /clone")),
			fail(outdated),
		},
		"a reply that acknowledges no op": {reply(`{"success":true,"removed":true}`)},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store, book, deleted := teardownStore(t)
			d := &scriptedDaemon{answers: answers}
			err := remove(t, newService(t, store, fakeDaemon(d.answer)))
			require.Error(t, err)
			assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
			assert.Contains(t, svcerr.ToConnect(err).Message(), "update Reliant on that machine")
			assert.Equal(t, db.CheckoutTeardownPending, book["op-1"].State)
			assert.Empty(t, d.answers, "every scripted answer was consumed")
			assert.Zero(t, *deleted)
		})
	}
}

// The regression: a daemon that knows ids replies with a guard refusal while
// the detached teardown it started is still deleting. The reported failure
// is not the op's outcome; the op's status says it is running, so the op
// stays pending and workspace creation stays held.
func TestRemove_AGuardRefusalWhileTheTeardownRunsKeepsItPending(t *testing.T) {
	t.Parallel()
	store, book, deleted := teardownStore(t)
	d := &scriptedDaemon{answers: []func(string, map[string]string) ([]byte, error){
		guardRefusal("git.remove"),
		reply(`{"op_id":"%s","op_state":"running"}`),
	}}
	err := remove(t, newService(t, store, fakeDaemon(d.answer)))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnavailable, svcerr.Code(err))
	assert.Contains(t, svcerr.ToConnect(err).Message(), "has not said how the operation itself ended")
	assert.Equal(t, db.CheckoutTeardownPending, book["op-1"].State, "creation stays held while the teardown may be deleting")
	assert.Equal(t, []string{"op-1", "op-1"}, d.opIDs, "the status asked is the op's own")
	assert.Zero(t, *deleted)
}

// A refusal before the op reached the journal: the op's status fences it
// ("not run", durably on the machine), which is an op-id-bearing answer, so
// the op settles not_run and nothing can run it later.
func TestRemove_ARefusalBeforeTheJournalSettlesNotRunViaTheFence(t *testing.T) {
	t.Parallel()
	store, book, deleted := teardownStore(t)
	d := &scriptedDaemon{answers: []func(string, map[string]string) ([]byte, error){
		guardRefusal("git.remove"),
		reply(`{"op_id":"%s","op_state":"fenced"}`),
	}}
	err := remove(t, newService(t, store, fakeDaemon(d.answer)))
	require.Error(t, err)
	assert.Contains(t, svcerr.ToConnect(err).Message(), "did not run")
	assert.Equal(t, db.CheckoutTeardownNotRun, book["op-1"].State)
	assert.Zero(t, *deleted)
}

// A retry refused by the guard settles nothing, whether the guard refuses
// the status question before any resend or the resend itself (and then the
// status question too): the first delivery may still be running.
func TestRemove_ARetryRefusedByTheGuardStaysPending(t *testing.T) {
	t.Parallel()
	for name, retry := range map[string][]func(string, map[string]string) ([]byte, error){
		"the status question is refused": {guardRefusal("git.teardown_status")},
		"the resend is refused": {
			reply(`{"op_id":"%s","op_state":"running"}`),
			guardRefusal("git.remove"),
			guardRefusal("git.teardown_status"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			store, book, deleted := teardownStore(t)
			d := &scriptedDaemon{answers: append([]func(string, map[string]string) ([]byte, error){
				fail(fmt.Errorf("send: %w", toolexec.ErrDaemonTimeout)),
			}, retry...)}
			svc := newService(t, store, fakeDaemon(d.answer))
			require.Error(t, remove(t, svc))
			require.Error(t, remove(t, svc))
			assert.Equal(t, db.CheckoutTeardownPending, book["op-1"].State, "a refused retry settles nothing")
			assert.Empty(t, d.answers, "every scripted answer was consumed")
			assert.Zero(t, *deleted)
		})
	}
}

// A machine whose reply to a reclone was lost, and which is too old to say
// how it ended: the next teardown sends nothing destructive until the machine
// is updated. The updated daemon has never seen the op, so it fences it, the
// op settles as not run, and the remove goes ahead as a new op.
func TestRemove_AnOutdatedMachinesLostOpSettlesOnceItIsUpdated(t *testing.T) {
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
	assert.Equal(t, connect.CodeFailedPrecondition, svcerr.Code(err))
	assert.Contains(t, svcerr.ToConnect(err).Message(), "update Reliant on that machine")
	assert.Equal(t, []string{"git.reclone", "git.teardown_status"}, d.sent, "nothing destructive is sent")
	assert.Equal(t, db.CheckoutTeardownPending, book["op-1"].State)

	// The machine is updated.
	d.answers = append(d.answers,
		reply(`{"op_id":"%s","op_state":"fenced"}`),
		reply(`{"success":true,"removed":true,"op_id":"%s","op_state":"done"}`))
	require.NoError(t, remove(t, svc))
	assert.Equal(t, db.CheckoutTeardownNotRun, book["op-1"].State)
	assert.Equal(t, db.CheckoutTeardownDone, book["op-2"].State)
	assert.Equal(t, []string{"git.reclone", "git.teardown_status", "git.teardown_status", "git.remove"}, d.sent)
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
