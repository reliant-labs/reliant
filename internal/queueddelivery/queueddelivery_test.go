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
	paused  []db.PausedForMachineChat
	users   []string
}

func (s *fakeStore) ListChatsPausedForMachine(_ context.Context, userID string, _ int) ([]db.PausedForMachineChat, error) {
	var out []db.PausedForMachineChat
	for _, c := range s.paused {
		if userID == "" || c.UserID == userID {
			out = append(out, c)
		}
	}
	return out, nil
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
// user's default machine, "name:<n>" = a selector by name).
type fakeMachines struct{ up map[string]bool }

func (m fakeMachines) IsDaemonOnline(_ context.Context, _ string, selector *toolexec.DaemonSelector) (bool, error) {
	id := ""
	if selector != nil {
		id = selector.ID
		if id == "" && selector.Name != "" {
			id = "name:" + selector.Name
		}
	}
	return m.up[id], nil
}

type fakeDeliverer struct {
	continued []string
	resumed   []string
	// resumable is the chats whose paused-for-machine mark is still set; a
	// resume claims (removes) it, as the real one does, so a second resume of
	// the same chat reports false.
	resumable map[string]bool
}

func (d *fakeDeliverer) ResumePausedForMachine(_ context.Context, chatID string) (bool, error) {
	d.resumed = append(d.resumed, chatID)
	if !d.resumable[chatID] {
		return false, nil
	}
	delete(d.resumable, chatID)
	return true, nil
}

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
	svc := New(Deps{Store: store, Machines: fakeMachines{up: map[string]bool{"": true, "d-up": true}}, Deliverer: deliverer, Signaler: signaler})

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
	svc := New(Deps{Store: store, Machines: fakeMachines{up: map[string]bool{"d2": true}}, Deliverer: deliverer, Signaler: &fakeSignaler{}})

	delivered, err := svc.Sweep(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 1, delivered)
	assert.Equal(t, []string{"c2"}, deliverer.continued)
	assert.Equal(t, []string{""}, store.users, "the sweep reads every user's")
}

func pausedFor(chatID, userID string, daemonID *string) db.PausedForMachineChat {
	return db.PausedForMachineChat{ChatID: chatID, UserID: userID, WorkflowID: "wf-" + chatID, ActiveDaemonID: daemonID, PausedAt: time.Now()}
}

// A connect resumes the chats the offline breaker paused whose machine is back.
// Only the breaker's pauses are listed, so a chat the user paused is never
// offered to the deliverer — and one whose machine is still down stays paused.
func TestMachineConnected_ResumesChatsTheBreakerPaused(t *testing.T) {
	store := &fakeStore{paused: []db.PausedForMachineChat{
		pausedFor("c-default", "u", nil),
		pausedFor("c-pinned-up", "u", ptr("d-up")),
		pausedFor("c-pinned-down", "u", ptr("d-down")),
		pausedFor("c-other-user", "someone-else", nil),
	}}
	deliverer := &fakeDeliverer{resumable: map[string]bool{"c-default": true, "c-pinned-up": true, "c-pinned-down": true, "c-other-user": true}}
	svc := New(Deps{Store: store, Machines: fakeMachines{up: map[string]bool{"": true, "d-up": true}}, Deliverer: deliverer, Signaler: &fakeSignaler{}})

	n := svc.MachineConnected(context.Background(), "u")

	assert.Equal(t, 2, n)
	assert.Equal(t, []string{"c-default", "c-pinned-up"}, deliverer.resumed,
		"only this user's chats whose own machine is connected")
	assert.Empty(t, deliverer.continued)
}

// A chat the user paused carries no mark, so the store never lists it.
func TestMachineConnected_DoesNotResumeAUserPausedChat(t *testing.T) {
	deliverer := &fakeDeliverer{}
	svc := New(Deps{Store: &fakeStore{}, Machines: fakeMachines{up: map[string]bool{"": true}}, Deliverer: deliverer, Signaler: &fakeSignaler{}})

	assert.Zero(t, svc.MachineConnected(context.Background(), "u"))
	assert.Empty(t, deliverer.resumed)
}

// A connect for a machine other than the chat's pinned one does nothing.
func TestMachineConnected_OtherMachineConnectingResumesNothing(t *testing.T) {
	store := &fakeStore{paused: []db.PausedForMachineChat{pausedFor("c1", "u", ptr("d-down"))}}
	deliverer := &fakeDeliverer{resumable: map[string]bool{"c1": true}}
	svc := New(Deps{Store: store, Machines: fakeMachines{up: map[string]bool{"d-other": true}}, Deliverer: deliverer, Signaler: &fakeSignaler{}})

	assert.Zero(t, svc.MachineConnected(context.Background(), "u"))
	assert.Empty(t, deliverer.resumed)
}

// A connect handled twice (redelivered, or raced by the sweep) resumes once:
// the second finds the chat already claimed.
func TestMachineConnected_DoubleConnectResumesOnce(t *testing.T) {
	store := &fakeStore{paused: []db.PausedForMachineChat{pausedFor("c1", "u", nil)}}
	deliverer := &fakeDeliverer{resumable: map[string]bool{"c1": true}}
	svc := New(Deps{Store: store, Machines: fakeMachines{up: map[string]bool{"": true}}, Deliverer: deliverer, Signaler: &fakeSignaler{}})

	first := svc.MachineConnected(context.Background(), "u")
	second := svc.MachineConnected(context.Background(), "u")
	swept, err := svc.Sweep(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 1, first)
	assert.Zero(t, second)
	assert.Zero(t, swept)
}

// The sweep is the backstop for a missed connect.
func TestSweep_ResumesBreakerPausedChatsWhoseMachineIsBack(t *testing.T) {
	store := &fakeStore{paused: []db.PausedForMachineChat{
		pausedFor("c1", "u1", ptr("d1")),
		pausedFor("c2", "u2", ptr("d2")),
	}}
	deliverer := &fakeDeliverer{resumable: map[string]bool{"c1": true, "c2": true}}
	svc := New(Deps{Store: store, Machines: fakeMachines{up: map[string]bool{"d2": true}}, Deliverer: deliverer, Signaler: &fakeSignaler{}})

	n, err := svc.Sweep(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, []string{"c2"}, deliverer.resumed)
}

// The machine the queuing run waited for decides delivery, not the chat's pin
// or the user's default machine. Prod, 2026-10-26: a workflow named a machine
// that never connected while the user's default one was up; every sweep
// delivered, the run failed its preflight and queued again — ~10k runs.
func TestDeliver_WaitsForTheMachineTheRunWaitedFor(t *testing.T) {
	store := &fakeStore{queued: []db.QueuedForMachineChat{
		{ChatID: "c-named-down", UserID: "u", Machine: &db.QueuedMachine{Name: "cicd"}},
		{ChatID: "c-named-up", UserID: "u", Machine: &db.QueuedMachine{Name: "laptop"}},
		{ChatID: "c-run-machine-over-pin", UserID: "u", ActiveDaemonID: ptr("d-down"), Machine: &db.QueuedMachine{ID: "d-up"}},
		{ChatID: "c-pin-only", UserID: "u", ActiveDaemonID: ptr("d-down")},
	}}
	deliverer := &fakeDeliverer{}
	svc := New(Deps{Store: store, Machines: fakeMachines{up: map[string]bool{"": true, "d-up": true, "name:laptop": true}},
		Deliverer: deliverer, Signaler: &fakeSignaler{}})

	_, err := svc.Sweep(context.Background())

	require.NoError(t, err)
	assert.Equal(t, []string{"c-named-up", "c-run-machine-over-pin"}, deliverer.continued)
}

// A chat whose delivered run queued its message again is not delivered again
// at once: each redelivery waits twice as long as the last, so a liveness
// disagreement between the deliverer and the run costs a few runs an hour,
// not one every sweep. The message stays queued throughout.
func TestDeliver_BacksOffAChatItKeepsRedelivering(t *testing.T) {
	store := &fakeStore{queued: []db.QueuedForMachineChat{{ChatID: "c1", UserID: "u"}}}
	deliverer := &fakeDeliverer{}
	svc := New(Deps{Store: store, Machines: fakeMachines{up: map[string]bool{"": true}}, Deliverer: deliverer, Signaler: &fakeSignaler{}}).(*service)
	now := time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	sweep := func(after time.Duration) int {
		now = now.Add(after)
		n, err := svc.Sweep(context.Background())
		require.NoError(t, err)
		return n
	}

	assert.Equal(t, 1, sweep(0), "the first delivery is never held")
	assert.Zero(t, sweep(30*time.Second), "queued again at once: held")
	assert.Equal(t, 1, sweep(31*time.Second), "one minute after the first")
	assert.Zero(t, sweep(90*time.Second), "the second hold is two minutes")
	assert.Equal(t, 1, sweep(31*time.Second))
	assert.Equal(t, []string{"c1", "c1", "c1"}, deliverer.continued)

	// A chat queued again long after its last hold is a new outage.
	assert.Equal(t, 1, sweep(redeliveryMax+redeliveryForget+time.Minute))
}

func TestRedeliveryHold_DoublesUpToTheCap(t *testing.T) {
	assert.Equal(t, time.Minute, redeliveryHold(1))
	assert.Equal(t, 2*time.Minute, redeliveryHold(2))
	assert.Equal(t, 16*time.Minute, redeliveryHold(5))
	assert.Equal(t, redeliveryMax, redeliveryHold(6))
	assert.Equal(t, redeliveryMax, redeliveryHold(50))
}
