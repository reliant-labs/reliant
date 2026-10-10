// Copyright (c) 2025 Reliant Labs
package queueddelivery

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workflow/machinewait"
)

type fakeStore struct {
	queued  []db.QueuedForMachineChat
	waiting []db.WaitingForMachineRun
	users   []string
}

func (s *fakeStore) ListChatsQueuedForMachine(_ context.Context, userID string, _ int) ([]db.QueuedForMachineChat, error) {
	s.users = append(s.users, userID)
	var out []db.QueuedForMachineChat
	for _, c := range s.queued {
		if userID == "" || c.UserID == userID {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *fakeStore) ListChatsWaitingForMachine(context.Context, string) ([]db.WaitingForMachineRun, error) {
	return s.waiting, nil
}

// fakeMachines reports online exactly the daemons named in up ("" = the
// user's default machine).
type fakeMachines struct{ up map[string]bool }

func (m fakeMachines) IsDaemonOnline(_ context.Context, _ string, selector *toolexec.DaemonSelector) (bool, error) {
	id := ""
	if selector != nil {
		id = selector.ID
	}
	return m.up[id], nil
}

type fakeDeliverer struct{ continued []string }

func (d *fakeDeliverer) ContinueQueued(_ context.Context, chatID string) (bool, error) {
	d.continued = append(d.continued, chatID)
	return true, nil
}

type signal struct {
	workflowID string
	payload    machinewait.Signal
}

type fakeSignaler struct{ sent []signal }

func (s *fakeSignaler) SignalWorkflow(_ context.Context, workflowID, _, name string, arg interface{}) error {
	if name == machinewait.SignalName {
		s.sent = append(s.sent, signal{workflowID: workflowID, payload: arg.(machinewait.Signal)})
	}
	return nil
}

func ptr(s string) *string { return &s }

// A connect wakes every run of the user's waiting for a machine — so it checks
// now rather than at its next recheck — and delivers the messages queued for a
// machine that is now connected. One whose machine is still down stays queued.
func TestMachineConnected_WakesWaitingRunsAndDeliversWhatIsNowReachable(t *testing.T) {
	store := &fakeStore{
		waiting: []db.WaitingForMachineRun{{ChatID: "c-waiting", WorkflowID: "wf-waiting"}},
		queued: []db.QueuedForMachineChat{
			{ChatID: "c-default", UserID: "u", QueuedAt: time.Now()},
			{ChatID: "c-pinned-up", UserID: "u", ActiveDaemonID: ptr("d-up")},
			{ChatID: "c-pinned-down", UserID: "u", ActiveDaemonID: ptr("d-down")},
		},
	}
	deliverer := &fakeDeliverer{}
	signaler := &fakeSignaler{}
	svc := New(store, fakeMachines{up: map[string]bool{"": true, "d-up": true}}, deliverer, signaler)

	delivered := svc.MachineConnected(context.Background(), "u")

	require.Len(t, signaler.sent, 1)
	assert.Equal(t, "wf-waiting", signaler.sent[0].workflowID)
	assert.False(t, signaler.sent[0].payload.Abandon, "a connect asks the run to check, never to give up")
	assert.Equal(t, []string{"c-default", "c-pinned-up"}, deliverer.continued)
	assert.Equal(t, 2, delivered)
	assert.Equal(t, []string{"u"}, store.users, "a connect looks only at its own user's chats")
}

// The sweep is the backstop for a missed connect: every user's queued chats,
// delivered where the machine is reachable.
func TestSweep_DeliversQueuedMessagesWhoseMachineIsBack(t *testing.T) {
	store := &fakeStore{queued: []db.QueuedForMachineChat{
		{ChatID: "c1", UserID: "u1", ActiveDaemonID: ptr("d1")},
		{ChatID: "c2", UserID: "u2", ActiveDaemonID: ptr("d2")},
	}}
	deliverer := &fakeDeliverer{}
	svc := New(store, fakeMachines{up: map[string]bool{"d2": true}}, deliverer, &fakeSignaler{})

	delivered, err := svc.Sweep(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 1, delivered)
	assert.Equal(t, []string{"c2"}, deliverer.continued)
	assert.Equal(t, []string{""}, store.users, "the sweep reads every user's")
}
