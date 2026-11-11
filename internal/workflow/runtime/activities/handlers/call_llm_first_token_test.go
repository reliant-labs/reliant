// Copyright (c) 2025 Reliant Labs
package handlers

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/reliant-labs/reliant/internal/llm"
	"github.com/reliant-labs/reliant/internal/llm/drivers"
	"github.com/reliant-labs/reliant/internal/llm/models"
	"github.com/reliant-labs/reliant/internal/llm/tools"
	"github.com/reliant-labs/reliant/internal/models/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tricklingDriver keeps its stream visibly alive with events that carry no
// output — the shape that held two gpt-6-luna turns open for 15 minutes with
// zero tokens while every inter-event guard kept resetting.
type tricklingDriver struct {
	mockLLMDriverForIdempotency
	every   time.Duration
	stopped chan struct{}
	// keepaliveOnce marks the stream live once, as a transport does for a
	// keepalive read inside an open block, and then never again.
	keepaliveOnce bool
	// prefill > 0 hands CallLLM a channel already holding that many framing
	// events and then sends nothing more: until they are consumed, the drain
	// loop always has an event to take and never reaches its select.
	prefill int
	events  chan llm.DriverEvent
}

func (d *tricklingDriver) StreamResponse(ctx context.Context, _ []string, _ []message.Message, _ []tools.Tool) <-chan llm.DriverEvent {
	events := make(chan llm.DriverEvent, d.prefill)
	for range d.prefill {
		events <- llm.DriverEvent{Type: llm.EventThinkingStart}
	}
	d.events = events
	if d.keepaliveOnce {
		llm.StreamLivenessFrom(ctx).Touch()
	}
	go func() {
		defer close(d.stopped)
		if d.prefill > 0 {
			// Left open, not closed: a close would end the stream before
			// the guard is tested.
			<-ctx.Done()
			return
		}
		defer close(events)
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(d.every):
			}
			select {
			case events <- llm.DriverEvent{Type: llm.EventThinkingStart}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return events
}

// steppingClock advances by step on every reading, so a test decides exactly
// how many guard checks fit before the deadline, whatever the host's speed.
type steppingClock struct {
	mu   sync.Mutex
	t    time.Time
	step time.Duration
}

func (c *steppingClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(c.step)
	return c.t
}

func TestCallLLM_NoFirstTokenFailsForRetry(t *testing.T) {
	t.Run("no keepalive", func(t *testing.T) {
		testCallLLMNoFirstTokenFailsForRetry(t, &tricklingDriver{every: 10 * time.Millisecond},
			llm.StreamTimeouts{FirstToken: 150 * time.Millisecond}, nil)
	})
	t.Run("a keepalive that went stale", func(t *testing.T) {
		// One liveness mark at the start, older than the hold window when
		// the deadline passes, must not hold the stream.
		testCallLLMNoFirstTokenFailsForRetry(t, &tricklingDriver{every: 10 * time.Millisecond, keepaliveOnce: true},
			llm.StreamTimeouts{FirstToken: 150 * time.Millisecond, Idle: 20 * time.Millisecond}, nil)
	})
	t.Run("a continuously ready event channel", func(t *testing.T) {
		// Framing events that are always ready must not starve the
		// first-token deadline: it is decided per event, so the stream is
		// cut with most of the backlog still unread. The guard's clock moves
		// one second per reading against a one-minute deadline, so the cut
		// lands about 60 events in, and the real timer never fires.
		driver := &tricklingDriver{prefill: 1 << 16}
		clock := &steppingClock{t: time.Now(), step: time.Second}
		testCallLLMNoFirstTokenFailsForRetry(t, driver, llm.StreamTimeouts{FirstToken: time.Minute}, clock.Now)
		assert.Greater(t, len(driver.events), 1<<15, "the deadline was decided only after the ready backlog was drained")
	})
}

func testCallLLMNoFirstTokenFailsForRetry(t *testing.T, driver *tricklingDriver, timeouts llm.StreamTimeouts, now func() time.Time) {
	// The progress guard is far away and keeps being reset: only the
	// first-token deadline can end this stream.
	timeouts.Progress = time.Minute
	llm.SetStreamTimeoutsForTest(t, timeouts)
	driver.stopped = make(chan struct{})
	resolver := drivers.DriverResolver(func(context.Context, string, models.Preferences, ...llm.DriverOption) (llm.Driver, error) {
		return driver, nil
	})
	h := NewIdempotencyTestHelper(t)
	defer h.Cleanup()
	ctx := context.Background()
	project := h.CreateTestProject(ctx, "project-"+uuid.NewString(), "user-"+uuid.NewString())
	chat := h.CreateTestChat(ctx, "chat-"+uuid.NewString(), project.ID, project.UserID)
	h.CreateTestUserMessage(ctx, chat.ID, chat.ID)
	activityInstance := NewCallLLMActivity(h.Repo(), &captureHub{}, nil, &staticConfigProvider{}, resolver, nil)
	activityInstance.firstTokenNow = now

	run := func(activityCtx context.Context, input ActivityInput) (*CallLLMOutput, error) {
		boundedCtx, cancel := context.WithTimeout(activityCtx, 5*time.Second)
		defer cancel()
		return activityInstance.Execute(boundedCtx, input)
	}
	h.env.RegisterActivity(run)
	_, err := h.env.ExecuteActivity(run, callLLMInput(chat.ID, chat.ID, "mock-model"))
	require.Error(t, err, "a stream with no output token must fail so the turn is retried")
	assert.Contains(t, err.Error(), "first-token timeout")
	select {
	case <-driver.stopped:
	case <-time.After(30 * time.Second):
		t.Fatal("first-token timeout did not cancel the provider stream")
	}
}

// guardClock is read by the guard and moved only by the test.
type guardClock struct{ t time.Time }

func (c *guardClock) Now() time.Time          { return c.t }
func (c *guardClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// fakeKeepalives is the transport's keepalive report, set by the test.
type fakeKeepalives struct {
	last time.Time
	ok   bool
}

func (k *fakeKeepalives) Last() (time.Time, bool) { return k.last, k.ok }
func (k *fakeKeepalives) touch(at time.Time)      { k.last, k.ok = at, true }

var (
	framing = llm.DriverEvent{Type: llm.EventThinkingStart}
	output  = llm.DriverEvent{Type: llm.EventContentDelta, Content: "hi"}
)

const (
	testFirstToken = time.Minute
	testHold       = 10 * time.Second
	// Past the 20 hold windows the longest hold test spans.
	testHoldCap = 10 * time.Minute
)

// newTestGuard returns a guard on a fake clock with no keepalive yet. Its
// real timer is a minute long and is never waited on.
func newTestGuard(t *testing.T) (*firstTokenGuard, *guardClock, *fakeKeepalives) {
	t.Helper()
	clock := &guardClock{t: time.Unix(1_000_000, 0)}
	g := newFirstTokenGuard(testFirstToken, testHold, testHoldCap, clock.Now)
	t.Cleanup(g.stop)
	return g, clock, &fakeKeepalives{}
}

// TestFirstTokenGuard_OwnerRule pins the owner's rule (2026-11-04, reliant
// #2145) at the production defaults: cut after 3 minutes with no output token
// and no keepalive; while keepalives arrive, hold up to a 10-minute cap.
func TestFirstTokenGuard_OwnerRule(t *testing.T) {
	t.Parallel()
	const keepaliveEvery = 15 * time.Second
	newProdGuard := func(t *testing.T) (*firstTokenGuard, *guardClock, *fakeKeepalives) {
		clock := &guardClock{t: time.Unix(1_000_000, 0)}
		g := newFirstTokenGuard(llm.DefaultStreamFirstTokenTimeout, llm.DefaultStreamIdleTimeout, llm.DefaultStreamFirstTokenHoldCap, clock.Now)
		t.Cleanup(g.stop)
		return g, clock, &fakeKeepalives{}
	}

	t.Run("keepalives only for 5 minutes, then a token: not cut", func(t *testing.T) {
		t.Parallel()
		g, clock, keepalives := newProdGuard(t)
		for range int(5 * time.Minute / keepaliveEvery) {
			clock.advance(keepaliveEvery)
			keepalives.touch(clock.Now())
			require.False(t, g.fire(keepalives), "cut a stream that is still sending keepalives")
		}
		require.False(t, g.admit(output, keepalives), "the first token after 5 minutes of keepalives must be accepted")
		assert.Nil(t, g.C(), "the token disarms the guard")
	})

	t.Run("no events for 3 minutes: cut", func(t *testing.T) {
		t.Parallel()
		g, clock, keepalives := newProdGuard(t)
		clock.advance(3*time.Minute - time.Nanosecond)
		require.False(t, g.fire(keepalives))
		clock.advance(time.Nanosecond)
		assert.True(t, g.fire(keepalives))
	})

	t.Run("keepalives past the 10-minute cap: cut", func(t *testing.T) {
		t.Parallel()
		g, clock, keepalives := newProdGuard(t)
		for range int(10*time.Minute/keepaliveEvery) - 1 {
			clock.advance(keepaliveEvery)
			keepalives.touch(clock.Now())
			require.False(t, g.fire(keepalives))
		}
		clock.advance(keepaliveEvery)
		keepalives.touch(clock.Now())
		assert.True(t, g.fire(keepalives), "keepalives must not hold a stream past the cap")
	})

	t.Run("a provider retry backoff restarts the cap", func(t *testing.T) {
		t.Parallel()
		g, clock, keepalives := newProdGuard(t)
		clock.advance(9 * time.Minute)
		keepalives.touch(clock.Now())
		require.False(t, g.admit(llm.DriverEvent{Type: llm.EventRetryWait, Retry: &llm.RetryWait{Delay: 30 * time.Second}}, keepalives))
		for range int((30*time.Second + 9*time.Minute) / keepaliveEvery) {
			clock.advance(keepaliveEvery)
			keepalives.touch(clock.Now())
			require.False(t, g.fire(keepalives), "the retried request is charged the first request's wait")
		}
	})
}

func TestFirstTokenGuard(t *testing.T) {
	t.Parallel()

	t.Run("output before the deadline disarms it for good", func(t *testing.T) {
		t.Parallel()
		g, clock, keepalives := newTestGuard(t)
		require.False(t, g.admit(framing, keepalives))
		require.NotNil(t, g.C(), "framing events are not output")
		clock.advance(testFirstToken - time.Nanosecond)
		require.False(t, g.admit(output, keepalives))
		assert.Nil(t, g.C())
		clock.advance(time.Hour)
		assert.False(t, g.admit(framing, keepalives), "a disarmed guard never cuts")
		g.observe(llm.DriverEvent{Type: llm.EventRetryWait, Retry: &llm.RetryWait{Delay: time.Second}})
		assert.Nil(t, g.C(), "a disarmed guard stays disarmed")
	})

	t.Run("with no keepalive the deadline cuts", func(t *testing.T) {
		t.Parallel()
		g, clock, keepalives := newTestGuard(t)
		clock.advance(testFirstToken - time.Nanosecond)
		require.False(t, g.admit(framing, keepalives))
		clock.advance(time.Nanosecond)
		assert.True(t, g.admit(framing, keepalives))
		assert.True(t, g.fire(keepalives))
	})

	t.Run("a keepalive before the deadline does not move it", func(t *testing.T) {
		t.Parallel()
		g, clock, keepalives := newTestGuard(t)
		// Keepalives all through the first half, then none: by the
		// deadline the last one is older than the hold window.
		for range 30 {
			clock.advance(time.Second)
			keepalives.touch(clock.Now())
		}
		clock.advance(testFirstToken - 30*time.Second)
		assert.True(t, g.fire(keepalives), "keepalives earlier in the wait extended the deadline")
		assert.False(t, g.held)
	})

	t.Run("output after the deadline with a stale keepalive is late", func(t *testing.T) {
		t.Parallel()
		g, clock, keepalives := newTestGuard(t)
		clock.advance(testFirstToken - testHold)
		keepalives.touch(clock.Now())
		clock.advance(testHold)
		assert.True(t, g.admit(output, keepalives), "a token queued behind the deadline must cut the stream")
		assert.NotNil(t, g.C(), "the late token must not disarm the guard")
	})

	t.Run("output after the deadline with no keepalive is late", func(t *testing.T) {
		t.Parallel()
		g, clock, keepalives := newTestGuard(t)
		clock.advance(testFirstToken)
		assert.True(t, g.admit(output, keepalives))
	})

	t.Run("keepalives that continue past the deadline hold it until they stop", func(t *testing.T) {
		t.Parallel()
		g, clock, keepalives := newTestGuard(t)
		clock.advance(testFirstToken - time.Second)
		keepalives.touch(clock.Now())
		clock.advance(time.Second)
		require.False(t, g.fire(keepalives), "mid-block on keepalives at the deadline: held")
		require.True(t, g.held)
		// Keepalives keep coming, each within the hold window of the last,
		// for longer than the first-token timeout itself.
		for range 20 {
			clock.advance(testHold - time.Second)
			keepalives.touch(clock.Now())
			require.False(t, g.admit(framing, keepalives), "held while keepalives continue")
		}
		// They stop: the first gap of a hold window cuts the stream, and
		// output arriving then is late, not the held block's.
		clock.advance(testHold)
		assert.True(t, g.fire(keepalives))
		assert.True(t, g.admit(output, keepalives))
	})

	t.Run("output while held is accepted", func(t *testing.T) {
		t.Parallel()
		// The documented exception: the output of the block the stream was
		// held for, e.g. the first text after a redacted thinking block.
		g, clock, keepalives := newTestGuard(t)
		clock.advance(testFirstToken - time.Second)
		keepalives.touch(clock.Now())
		clock.advance(time.Second + testHold/2)
		require.False(t, g.admit(output, keepalives))
		assert.True(t, g.held)
		assert.Nil(t, g.C())
	})

	t.Run("a retry wait restarts the deadline after the backoff", func(t *testing.T) {
		t.Parallel()
		g, clock, keepalives := newTestGuard(t)
		clock.advance(testFirstToken - time.Second)
		require.False(t, g.admit(llm.DriverEvent{Type: llm.EventRetryWait, Retry: &llm.RetryWait{Delay: 30 * time.Second}}, keepalives))
		clock.advance(30*time.Second + testFirstToken - time.Nanosecond)
		require.False(t, g.admit(framing, keepalives), "fired during the driver's backoff or the new request's wait")
		clock.advance(time.Nanosecond)
		assert.True(t, g.admit(framing, keepalives))
	})

	t.Run("while held its timer wakes when the keepalive goes stale", func(t *testing.T) {
		t.Parallel()
		g, clock, keepalives := newTestGuard(t)
		// The keepalive goes stale 1ms of real time after the hold begins.
		clock.advance(testFirstToken - testHold + time.Millisecond)
		keepalives.touch(clock.Now())
		clock.advance(testHold - time.Millisecond)
		require.False(t, g.fire(keepalives))
		require.True(t, g.held)
		select {
		case <-g.C():
		case <-time.After(5 * time.Second):
			t.Fatal("the held guard never woke")
		}
		clock.advance(time.Millisecond)
		assert.True(t, g.fire(keepalives))
	})

	t.Run("the hold is capped however fresh the keepalives", func(t *testing.T) {
		t.Parallel()
		g, clock, keepalives := newTestGuard(t)
		for clock.t.Before(time.Unix(1_000_000, 0).Add(testHoldCap - time.Second)) {
			clock.advance(time.Second)
			keepalives.touch(clock.Now())
			require.False(t, g.admit(framing, keepalives), "held while keepalives continue, inside the cap")
		}
		clock.advance(time.Second)
		keepalives.touch(clock.Now())
		assert.True(t, g.fire(keepalives), "past the cap a fresh keepalive must not hold the stream")
		assert.True(t, g.admit(output, keepalives), "output past the cap is late")
	})

	t.Run("its timer fires at the deadline", func(t *testing.T) {
		t.Parallel()
		g := newFirstTokenGuard(10*time.Millisecond, testHold, testHoldCap, nil)
		defer g.stop()
		select {
		case <-g.C():
		case <-time.After(5 * time.Second):
			t.Fatal("guard never fired")
		}
		assert.True(t, g.fire(llm.NewStreamLiveness()))
	})
}
