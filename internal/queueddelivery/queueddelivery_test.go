// Copyright (c) 2025 Reliant Labs
package queueddelivery

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/db"
	"github.com/reliant-labs/reliant/internal/db/core"
	"github.com/reliant-labs/reliant/internal/toolexec"
	"github.com/reliant-labs/reliant/internal/workflow/machinewait"
)

type fakeStore struct {
	queued  []db.QueuedForMachineChat
	waiting []db.WaitingForMachineRun
	paused  []db.PausedForMachineChat
	users   []string

	attempts []string
	gaveUp   []string
	revived  []string
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

func (s *fakeStore) ListChatsQueuedForMachine(_ context.Context, userID string, _ int, includeGivenUp bool) ([]db.QueuedForMachineChat, error) {
	s.users = append(s.users, userID)
	var out []db.QueuedForMachineChat
	for _, c := range s.queued {
		if (userID == "" || c.UserID == userID) && (includeGivenUp || c.GaveUpAt == nil) {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *fakeStore) RecordQueuedDeliveryAttempt(_ context.Context, chatID string, _ time.Time) error {
	s.attempts = append(s.attempts, chatID)
	return nil
}

func (s *fakeStore) GiveUpQueuedDelivery(_ context.Context, chatID string) (bool, error) {
	s.gaveUp = append(s.gaveUp, chatID)
	return true, nil
}

func (s *fakeStore) ReviveQueuedDelivery(_ context.Context, chatID string) (bool, error) {
	s.revived = append(s.revived, chatID)
	return true, nil
}

func (s *fakeStore) ListChatsWaitingForMachine(context.Context, string) ([]db.WaitingForMachineRun, error) {
	return s.waiting, nil
}

// fakeMachines reports online exactly the machines named in up: a selector
// answers by its id, else by its name, else it is the user's default machine
// ("").
type fakeMachines struct {
	up map[string]bool
	// attached is when each machine's current session began; a machine in up
	// without an entry attached at the zero time.
	attached map[string]time.Time
}

func machineKey(selector *toolexec.DaemonSelector) string {
	switch {
	case selector == nil:
	case selector.ID != "":
		return selector.ID
	case selector.Name != "":
		return "name:" + selector.Name
	}
	return ""
}

func (m fakeMachines) IsDaemonOnline(_ context.Context, _ string, selector *toolexec.DaemonSelector) (bool, error) {
	return m.up[machineKey(selector)], nil
}

func (m fakeMachines) AttachedSince(_ context.Context, _ string, selector *toolexec.DaemonSelector) (time.Time, bool, error) {
	key := machineKey(selector)
	return m.attached[key], m.up[key], nil
}

type fakeDeliverer struct {
	continued []string
	resumed   []string
	stopped   []db.QueuedForMachineChat
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

func (d *fakeDeliverer) NotifyQueuedDeliveryStopped(_ context.Context, chat db.QueuedForMachineChat) error {
	d.stopped = append(d.stopped, chat)
	return nil
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

// ── Delivery waits for the machine the run waited on ─────────────────────

// The 2026-10-27 loop, as a state: the chat's own machine ("default") is
// connected, but the run that queued the message waited on the workflow's
// pinned machine ("cicd"), which is not. Delivery must wait for cicd, or it
// starts a run that fails at the same step and queues the message again.
func TestDeliver_WaitsForTheMachineTheRunWaitedOn_NotTheChatsOwn(t *testing.T) {
	store := &fakeStore{queued: []db.QueuedForMachineChat{{
		ChatID: "c1", UserID: "u", ActiveDaemonID: ptr("default"), QueuedAt: time.Now(),
		Target: db.QueuedMachineTarget{DaemonName: "cicd"},
	}}}
	deliverer := &fakeDeliverer{}
	svc := New(Deps{Store: store, Machines: fakeMachines{up: map[string]bool{"default": true}}, Deliverer: deliverer, Signaler: &fakeSignaler{}})

	assert.Zero(t, svc.MachineConnected(context.Background(), "u"), "the chat's own machine connecting is not the news")
	swept, err := svc.Sweep(context.Background())
	require.NoError(t, err)
	assert.Zero(t, swept)
	assert.Empty(t, deliverer.continued)
	assert.Empty(t, store.attempts, "nothing was started, so nothing was counted")
}

// The machine the run waited on connects: it is delivered, and the attempt is
// counted before the run starts.
func TestDeliver_StartsTheRunOnceTheAwaitedMachineIsUp(t *testing.T) {
	store := &fakeStore{queued: []db.QueuedForMachineChat{{
		ChatID: "c1", UserID: "u", ActiveDaemonID: ptr("default"),
		Target: db.QueuedMachineTarget{DaemonName: "cicd"},
	}}}
	deliverer := &fakeDeliverer{}
	svc := New(Deps{Store: store, Machines: fakeMachines{up: map[string]bool{"default": true, "name:cicd": true}}, Deliverer: deliverer, Signaler: &fakeSignaler{}})

	assert.Equal(t, 1, svc.MachineConnected(context.Background(), "u"))
	assert.Equal(t, []string{"c1"}, deliverer.continued)
	assert.Equal(t, []string{"c1"}, store.attempts)
}

// ── The loop guard ───────────────────────────────────────────────────────

// loopWorld is the whole loop with a clock: delivery's decision (this
// package), and the run it starts, which fails preflight on the pinned machine
// and queues the message again — exactly what ChatService.ContinueQueued plus
// PreflightDaemonCheckActivity.terminal do. The store keeps the delivery
// record the way the real one does: attempts counted, backoff scheduled,
// queueing again keeps the count.
type loopWorld struct {
	t    *testing.T
	now  time.Time
	chat db.QueuedForMachineChat
	// gaveUpAt is when delivery stopped retrying (nil: still retrying).
	gaveUpAt *time.Time
	up       map[string]bool
	attached map[string]time.Time
	// unreachable makes every run fail preflight even though its machine
	// reads as connected (a flapping machine, a gateway that has not caught up).
	unreachable bool
	runs        int
	stopped     int
	svc         Service
	store       *loopStore
}

// connect brings a machine up with a new session beginning now.
func (w *loopWorld) connect(key string) {
	w.up[key] = true
	w.attached[key] = w.now
}

type loopStore struct {
	w *loopWorld
}

// ListChatsQueuedForMachine mirrors the real listing: a parked chat is listed
// only when asked for and only once its hold has passed.
func (s *loopStore) ListChatsQueuedForMachine(_ context.Context, _ string, _ int, includeGivenUp bool) ([]db.QueuedForMachineChat, error) {
	w := s.w
	if w.chat.ChatID == "" {
		return nil, nil
	}
	chat := w.chat
	if w.gaveUpAt != nil {
		held := chat.NextAttemptAt != nil && w.now.Before(*chat.NextAttemptAt)
		if !includeGivenUp || held {
			return nil, nil
		}
		at := *w.gaveUpAt
		chat.GaveUpAt = &at
	}
	return []db.QueuedForMachineChat{chat}, nil
}

func (s *loopStore) RecordQueuedDeliveryAttempt(_ context.Context, _ string, next time.Time) error {
	s.w.chat.Attempts++
	s.w.chat.NextAttemptAt = &next
	return nil
}

// GiveUpQueuedDelivery mirrors the real UPDATE: only a queued chat that has
// spent its attempts, and only once.
func (s *loopStore) GiveUpQueuedDelivery(context.Context, string) (bool, error) {
	w := s.w
	if w.chat.ChatID == "" || w.gaveUpAt != nil || w.chat.Attempts < core.MaxQueuedDeliveryAttempts {
		return false, nil
	}
	at := w.now
	w.gaveUpAt = &at
	held := w.now.Add(core.QueuedDeliveryGiveUpHold)
	w.chat.NextAttemptAt = &held
	return true, nil
}

func (s *loopStore) ReviveQueuedDelivery(context.Context, string) (bool, error) {
	if s.w.gaveUpAt == nil {
		return false, nil
	}
	s.w.gaveUpAt = nil
	s.w.chat.Attempts = core.MaxQueuedDeliveryAttempts - 1
	s.w.chat.NextAttemptAt = nil
	return true, nil
}

func (s *loopStore) ListChatsWaitingForMachine(context.Context, string) ([]db.WaitingForMachineRun, error) {
	return nil, nil
}

func (s *loopStore) ListChatsPausedForMachine(context.Context, string, int) ([]db.PausedForMachineChat, error) {
	return nil, nil
}

// loopDeliverer is the run delivery starts. The pinned machine is down, so the
// run's preflight fails and queues the message again (the attempt count stays).
type loopDeliverer struct{ w *loopWorld }

func (d *loopDeliverer) ContinueQueued(context.Context, string) (bool, error) {
	w := d.w
	w.runs++
	if w.up["name:cicd"] && !w.unreachable {
		// The run reaches its machine: the message is delivered, and the
		// preflight settles the record.
		w.chat = db.QueuedForMachineChat{}
		return true, nil
	}
	return true, nil // preflight fails; the message stays queued.
}

func (d *loopDeliverer) ResumePausedForMachine(context.Context, string) (bool, error) {
	return false, nil
}

func (d *loopDeliverer) NotifyQueuedDeliveryStopped(context.Context, db.QueuedForMachineChat) error {
	d.w.stopped++
	return nil
}

// newLoopWorld is the 2026-10-27 state: the chat's own machine ("default") is
// connected, the machine its workflow pins ("cicd") is not. recorded says
// whether the queued message remembers it waits for cicd; without that
// (a message queued before records existed, or a run whose failing step
// delivery's liveness check cannot see) delivery can only look at the chat's
// own machine — the case the attempt limit exists for.
func newLoopWorld(t *testing.T, recorded bool) *loopWorld {
	t.Helper()
	w := &loopWorld{
		t:   t,
		now: time.Date(2026, 10, 27, 15, 35, 0, 0, time.UTC),
		// The chat's own machine ("default") is connected; the workflow-pinned
		// one ("cicd") is not.
		up:       map[string]bool{"default": true},
		attached: map[string]time.Time{},
	}
	w.chat = db.QueuedForMachineChat{ChatID: "c1", UserID: "u", ActiveDaemonID: ptr("default"), QueuedAt: w.now, Since: w.now}
	if recorded {
		w.chat.Target = db.QueuedMachineTarget{DaemonName: "cicd"}
	}
	w.store = &loopStore{w: w}
	svc := New(Deps{Store: w.store, Machines: &loopMachines{w: w}, Deliverer: &loopDeliverer{w: w}, Signaler: &fakeSignaler{}})
	svc.(*service).now = func() time.Time { return w.now }
	w.svc = svc
	return w
}

type loopMachines struct{ w *loopWorld }

func (m *loopMachines) IsDaemonOnline(ctx context.Context, userID string, selector *toolexec.DaemonSelector) (bool, error) {
	return fakeMachines{up: m.w.up, attached: m.w.attached}.IsDaemonOnline(ctx, userID, selector)
}

func (m *loopMachines) AttachedSince(ctx context.Context, userID string, selector *toolexec.DaemonSelector) (time.Time, bool, error) {
	return fakeMachines{up: m.w.up, attached: m.w.attached}.AttachedSince(ctx, userID, selector)
}

// The regression for the 8,928 failed runs, for a message that does not know
// which machine it waits for. The reconciler's sweep fires every 30s; the
// chat's own machine is connected, the workflow-pinned machine is not. Before
// the fix this started a run on every pass for ever. Now the sweep stops after
// core.MaxQueuedDeliveryAttempts runs, tells the owner once, and starts
// nothing more however long the outage lasts — and delivery resumes the moment
// the pinned machine connects.
func TestSweep_StopsAfterNAttemptsWhenThePinnedMachineStaysDown_AndResumesWhenItConnects(t *testing.T) {
	w := newLoopWorld(t, false)
	ctx := context.Background()

	// 27 hours of 30-second reconciler passes.
	for pass := 0; pass < 27*60*2; pass++ {
		_, err := w.svc.Sweep(ctx)
		require.NoError(t, err)
		w.now = w.now.Add(30 * time.Second)
	}

	assert.Equal(t, core.MaxQueuedDeliveryAttempts, w.runs, "bounded: N runs over 27 hours, not 3,000")
	assert.Equal(t, 1, w.stopped, "the owner is told once, not once per pass")
	assert.NotNil(t, w.gaveUpAt, "delivery stopped retrying on its own")

	// With no recorded target the machine the message waits for is the chat's
	// own ("default"); it reconnecting, with the pinned machine now up too, is
	// the news that delivers it.
	w.connect("name:cicd")
	w.connect("default")
	assert.Equal(t, 1, w.svc.MachineConnected(ctx, "u"), "its connect delivers the message")
	assert.Equal(t, core.MaxQueuedDeliveryAttempts+1, w.runs)
	assert.Empty(t, w.chat.ChatID, "delivered: nothing is queued any more")
}

// The backoff spaces the attempts out instead of firing one per pass.
func TestSweep_BacksOffBetweenAttempts(t *testing.T) {
	w := newLoopWorld(t, false)
	ctx := context.Background()

	for pass := 0; pass < 6; pass++ { // three minutes of 30s passes
		_, err := w.svc.Sweep(ctx)
		require.NoError(t, err)
		w.now = w.now.Add(30 * time.Second)
	}

	// Attempt 1 at 0s; the next is not due for 30s (pass 2), then 60s, then 120s.
	assert.Equal(t, 3, w.runs, "attempts at 0s, 30s and 90s — not six")
}

// stopWorld runs the world until delivery has given up: the message waits for
// cicd, which reads as connected but whose runs keep failing at preflight.
func stopWorld(t *testing.T) *loopWorld {
	t.Helper()
	w := newLoopWorld(t, true)
	w.connect("name:cicd")
	w.unreachable = true
	// Stops at the give-up, so a test can place events relative to it.
	for pass := 0; pass < 600 && w.gaveUpAt == nil; pass++ {
		_, err := w.svc.Sweep(context.Background())
		require.NoError(t, err)
		if w.gaveUpAt == nil {
			w.now = w.now.Add(30 * time.Second)
		}
	}
	require.NotNil(t, w.gaveUpAt)
	require.Equal(t, 1, w.stopped)
	require.Equal(t, core.MaxQueuedDeliveryAttempts, w.runs)
	return w
}

// sweepFor runs the reconciler's 30-second passes for d.
func sweepFor(t *testing.T, w *loopWorld, d time.Duration) {
	t.Helper()
	for end := w.now.Add(d); w.now.Before(end); w.now = w.now.Add(30 * time.Second) {
		_, err := w.svc.Sweep(context.Background())
		require.NoError(t, err)
	}
}

// A machine that reconnects INSIDE the hold after a give-up, and then stays
// connected, still gets the promised retry: nothing connects again to trigger
// one, so the sweep revives the message once the hold has passed — because the
// machine connected after delivery gave up. It is one retry, not a loop: the
// retry fails, delivery gives up again, and the same session is not news twice.
func TestSweep_ARetryIsOwedToAMachineThatReconnectedInsideTheHold(t *testing.T) {
	w := stopWorld(t)
	ctx := context.Background()
	runsBefore, stoppedBefore := w.runs, w.stopped

	w.now = w.now.Add(2 * time.Minute)
	w.connect("name:cicd") // a new session, two minutes after the give-up
	assert.Zero(t, w.svc.MachineConnected(ctx, "u"), "inside the hold: the connect event itself starts nothing")
	assert.Equal(t, runsBefore, w.runs)

	sweepFor(t, w, 24*time.Hour)

	assert.Equal(t, 1, w.runs-runsBefore, "exactly one retry in a day of sweeps")
	assert.Equal(t, 1, w.stopped-stoppedBefore, "it gave up again, and said so once more")
}

// The same machine, still on the session that was up when delivery gave up, is
// not news: no connect since, no retry, however long the sweep waits.
func TestSweep_DoesNotRetryAMachineThatHasNotConnectedSinceTheGiveUp(t *testing.T) {
	w := stopWorld(t) // cicd's session began before the give-up and has stayed up
	require.True(t, w.attached["name:cicd"].Before(*w.gaveUpAt))
	runsBefore := w.runs

	sweepFor(t, w, 24*time.Hour)

	assert.Zero(t, w.runs-runsBefore)
}

// A flapping machine must not put the loop back at the rate of its reconnects:
// a connect after delivery gave up buys ONE more attempt, not a fresh budget,
// and at most one per hold.
func TestMachineConnected_FlappingMachineGetsOneAttemptPerHoldNotAFreshBudget(t *testing.T) {
	w := stopWorld(t)
	ctx := context.Background()
	runsBefore, stoppedBefore := w.runs, w.stopped

	// Four flaps in the first two minutes, then it stays up for a day.
	for range 4 {
		w.now = w.now.Add(20 * time.Second)
		w.connect("name:cicd")
		_ = w.svc.MachineConnected(ctx, "u")
		w.up["name:cicd"] = false
	}
	assert.Zero(t, w.runs-runsBefore, "inside the hold, reconnects start nothing")
	w.connect("name:cicd")
	sweepFor(t, w, 24*time.Hour)

	assert.Equal(t, 1, w.runs-runsBefore, "one retry, however often it flapped")
	assert.Equal(t, 1, w.stopped-stoppedBefore, "the owner is told once more, not on every flap")
}

// A message delivery has not tried yet, or has tried fewer times than the
// limit, is delivered at once on the connect it was waiting for, whatever its
// backoff says: the machine connecting is news the backoff did not know.
func TestMachineConnected_DeliversAMessageNotYetGivenUp(t *testing.T) {
	now := time.Now()
	later := now.Add(10 * time.Minute)
	store := &fakeStore{queued: []db.QueuedForMachineChat{{
		ChatID: "c1", UserID: "u", Attempts: 2, NextAttemptAt: &later,
		Target: db.QueuedMachineTarget{DaemonName: "cicd"},
	}}}
	deliverer := &fakeDeliverer{}
	svc := New(Deps{Store: store, Machines: fakeMachines{up: map[string]bool{"name:cicd": true}}, Deliverer: deliverer, Signaler: &fakeSignaler{}})
	svc.(*service).now = func() time.Time { return now }

	assert.Equal(t, 1, svc.MachineConnected(context.Background(), "u"))
	assert.Equal(t, []string{"c1"}, deliverer.continued)
}

// The same state, for a message that DOES remember its machine: delivery waits
// for cicd itself, so the loop never starts — not one run in 27 hours — and
// the connect of cicd delivers the message.
func TestSweep_RecordedTargetNeverStartsARunWhileThePinnedMachineIsDown(t *testing.T) {
	w := newLoopWorld(t, true)
	ctx := context.Background()

	for pass := 0; pass < 27*60*2; pass++ {
		_, err := w.svc.Sweep(ctx)
		require.NoError(t, err)
		w.now = w.now.Add(30 * time.Second)
	}
	assert.Zero(t, w.runs, "the chat's machine being up says nothing about the machine the run waited on")
	assert.Zero(t, w.stopped, "nothing failed, so nothing to report")

	w.connect("name:cicd")
	assert.Equal(t, 1, w.svc.MachineConnected(ctx, "u"))
	assert.Equal(t, 1, w.runs)
	assert.Empty(t, w.chat.ChatID, "delivered")
}
