// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/toolexec"
	activitytypes "github.com/reliant-labs/reliant/internal/workflow/runtime/activities/types"
)

// A run step whose machine blips. cp-merge, 2026-10-30: the managed machine's
// VM restarted under a `gate` command; every send in the next minutes found no
// daemon, and the step's 15-second re-send ladder ran out long before the
// machine was back, so the run paused (and, being an automation, eventually
// parked holding its capacity slot). The step now holds the command for the
// machine — for minutes, inside the one attempt — and sends it once the
// machine is attached again.
//
// The machine here is a fake daemon on a fake clock: unreachable until
// backAt, then answering. Nothing waits in real time.

const blipDaemonID = "e164d47d-0f2c-4f43-9a51-6c0f1a2b3c4d"

// fakeClock is a clock whose Sleep advances it instantly.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 30, 7, 57, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
	return nil
}

// blippingDaemon is a machine that is unreachable until backAt and then runs
// commands. It is both the run executor (the transport) and the machine
// observer (the registry), as the real router is both.
type blippingDaemon struct {
	clock  *fakeClock
	backAt time.Time
	// removed reports the machine deleted instead of coming back.
	removed bool
	// undeliveredDelay is how long an undelivered send takes before it
	// fails: the router rides out a disconnect inside it (up to
	// DaemonReconnectGrace, 90s).
	undeliveredDelay time.Duration

	mu    sync.Mutex
	sends int
	ran   int
	looks int
	// timeouts is the command timeout each send carried.
	timeouts []int
}

func (d *blippingDaemon) up() bool { return !d.removed && !d.clock.Now().Before(d.backAt) }

func (d *blippingDaemon) ExecuteCommand(_ context.Context, command, _ string, timeoutMs int, _ map[string]string) (string, string, int, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sends++
	d.timeouts = append(d.timeouts, timeoutMs)
	if !d.up() {
		_ = d.clock.Sleep(context.Background(), d.undeliveredDelay)
		return "", "", -1, false, &toolexec.TransportError{
			Code: toolexec.ErrorCodeDaemonUnreached, Command: command,
			Detail:        "tool request via NATS failed: no daemon connected for user",
			NotDispatched: true, DaemonID: blipDaemonID,
		}
	}
	d.ran++
	return "gate passed", "", 0, false, nil
}

func (d *blippingDaemon) MachineStatus(context.Context, string) (tools.MachineStatus, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.looks++
	if d.removed {
		return tools.MachineStatus{Removed: true}, nil
	}
	return tools.MachineStatus{Connected: d.up()}, nil
}

func (d *blippingDaemon) counts() (sends, ran int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sends, d.ran
}

// The machine is away for twelve minutes — far past the old 15s ladder — and
// comes back. The step waits for it, sends the command once it is attached,
// and returns the command's verdict from the same attempt: no node retry, no
// Temporal retry, no pause.
func TestRunStep_MachineBackAfterMinutes_CommandRunsOnce(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	start := clock.Now()
	daemon := &blippingDaemon{clock: clock, backAt: start.Add(12 * time.Minute)}

	out, err := runStepActivity(t, daemon, daemon, clock)

	require.NoError(t, err, "a machine back within the wait is not a failure")
	assert.Equal(t, 0, out.ExitCode)
	assert.Equal(t, "gate passed", out.Stdout)
	sends, ran := daemon.counts()
	assert.Equal(t, 1, ran, "the command ran exactly once")
	assert.Equal(t, 2, sends, "sent once, held while the machine was away (not re-sent blind), sent again when it was back")
	assert.GreaterOrEqual(t, clock.Now().Sub(start), 12*time.Minute)
	assert.Less(t, clock.Now().Sub(start), 13*time.Minute, "sent again promptly once the machine was attached")
}

// The node's 30s timeout bounds the command on the machine, every time it is
// sent; the wait for an unreachable machine is extra, and outlasts it. A
// healthy machine (backAt now) gets the command once, bounded by 30s.
func TestRunStep_CommandTimeoutBoundsTheCommandNotTheWait(t *testing.T) {
	t.Parallel()
	for name, away := range map[string]time.Duration{"healthy machine": 0, "machine away 12m": 12 * time.Minute} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			clock := newFakeClock()
			daemon := &blippingDaemon{clock: clock, backAt: clock.Now().Add(away)}

			_, err := runStepActivityTimed(t, daemon, daemon, clock, 30_000)

			require.NoError(t, err, "a wait longer than the command timeout is not a command timeout")
			daemon.mu.Lock()
			defer daemon.mu.Unlock()
			require.NotEmpty(t, daemon.timeouts)
			for _, got := range daemon.timeouts {
				assert.Equal(t, 30_000, got, "every send carries the node's command timeout, not the 5m default")
			}
			assert.Equal(t, 1, daemon.ran)
		})
	}
}

// The first send itself can spend 90s riding out the disconnect before it
// fails. That time is part of the wait, not added to it: the activity's
// deadline (command timeout + RunStepMachineWaitBudget + RunStepSendOverhead)
// is measured from the same start, so a short node timeout leaves no room for
// a wait that started late — a step killed by its deadline would be refused on
// retry and pause the run instead of failing clearly.
func TestRunStep_SlowFirstSendCountsAgainstTheWait(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	start := clock.Now()
	daemon := &blippingDaemon{clock: clock, backAt: start.Add(24 * time.Hour), undeliveredDelay: 90 * time.Second}

	_, err := runStepActivityTimed(t, daemon, daemon, clock, 30_000)

	requireNonRetryable(t, err, activitytypes.RunStepMachineUnavailableErrType)
	assert.Contains(t, err.Error(), "machine e164d47d unavailable for 30m")
	elapsed := clock.Now().Sub(start)
	assert.GreaterOrEqual(t, elapsed, activitytypes.RunStepMachineWaitBudget)
	assert.LessOrEqual(t, elapsed, activitytypes.RunStepMachineWaitBudget+10*time.Second,
		"the wait is measured from the first send's start, not from its failure 90s later")
}

// A machine back late in the wait, behind a slow first send, still gets its
// command with the node's full timeout, inside the activity's deadline.
func TestRunStep_SlowFirstSendThenMachineBackLate(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	start := clock.Now()
	daemon := &blippingDaemon{clock: clock, backAt: start.Add(29 * time.Minute), undeliveredDelay: 90 * time.Second}

	out, err := runStepActivityTimed(t, daemon, daemon, clock, 30_000)

	require.NoError(t, err)
	assert.Equal(t, "gate passed", out.Stdout)
	daemon.mu.Lock()
	defer daemon.mu.Unlock()
	assert.Equal(t, 30_000, daemon.timeouts[len(daemon.timeouts)-1], "the send that ran carried the full command timeout")
	assert.Less(t, clock.Now().Sub(start), 30*time.Second+activitytypes.RunStepMachineWaitBudget+activitytypes.RunStepSendOverhead)
}

// The machine never comes back. The step fails once the 30-minute wait is
// spent, naming the machine and the wait, saying the command never ran. It is
// not retryable: the wait was the retry.
func TestRunStep_MachineGonePastTheWait_FailsClearly(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	start := clock.Now()
	daemon := &blippingDaemon{clock: clock, backAt: start.Add(24 * time.Hour)}

	_, err := runStepActivity(t, daemon, daemon, clock)

	requireNonRetryable(t, err, activitytypes.RunStepMachineUnavailableErrType)
	assert.Contains(t, err.Error(), "machine e164d47d unavailable for 30m")
	assert.Contains(t, err.Error(), "the command never ran")
	sends, ran := daemon.counts()
	assert.Zero(t, ran)
	assert.Equal(t, 1, sends, "a machine that reads disconnected is watched, not hammered")
	waited := clock.Now().Sub(start)
	assert.GreaterOrEqual(t, waited, activitytypes.RunStepMachineWaitBudget)
	assert.Less(t, waited, activitytypes.RunStepMachineWaitBudget+time.Minute, "and not much longer")
}

// A machine that was removed will not come back: the step fails at once
// instead of waiting out the budget for it.
func TestRunStep_RemovedMachine_FailsAtOnce(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	start := clock.Now()
	daemon := &blippingDaemon{clock: clock, removed: true}

	_, err := runStepActivity(t, daemon, daemon, clock)

	requireNonRetryable(t, err, activitytypes.RunStepMachineUnavailableErrType)
	assert.Contains(t, err.Error(), "machine e164d47d no longer exists")
	assert.Contains(t, err.Error(), "the command never ran")
	assert.Less(t, clock.Now().Sub(start), time.Minute)
}

// With nothing to observe the machine through, the step still waits — by
// re-sending on a capped backoff — and goes through when the machine is back.
func TestRunStep_UnobservableMachineBack_CommandRunsOnce(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	daemon := &blippingDaemon{clock: clock, backAt: clock.Now().Add(5 * time.Minute)}

	out, err := runStepActivity(t, daemon, nil, clock)

	require.NoError(t, err)
	assert.Equal(t, "gate passed", out.Stdout)
	sends, ran := daemon.counts()
	assert.Equal(t, 1, ran)
	assert.Less(t, sends, 20, "re-sent on a backoff capped at 30s, not in a tight loop")
}

func TestFormatMachineWait(t *testing.T) {
	t.Parallel()
	for d, want := range map[time.Duration]string{
		30 * time.Minute:                      "30m",
		30*time.Minute + 20*time.Second:       "30m",
		time.Hour:                             "1h",
		time.Hour + 5*time.Minute:             "1h5m",
		45 * time.Second:                      "45s",
		45*time.Second + 400*time.Millisecond: "45s",
	} {
		assert.Equal(t, want, formatMachineWait(d), "%s", d)
	}
}
