package hostslot

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Lanes, driven through the same real flock path as hostslot_test.go.

type acquired struct {
	slot *Slot
	err  error
}

// startAcquire runs Acquire in the background and waits until its ticket is
// filed in the lane's queue (wantQueued tickets there in all), so the caller
// knows it is waiting rather than racing to the fast path.
func startAcquire(t *testing.T, opt Options, wantQueued int) <-chan acquired {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	ch := make(chan acquired, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s, err := Acquire(ctx, Heavy, opt)
		ch <- acquired{s, err}
	}()
	qdir := filepath.Join(opt.Dir, Heavy, "queue")
	if l, _ := opt.lane(); l == LaneExpress {
		qdir = filepath.Join(opt.Dir, Heavy, expressQueueDir)
	}
	waitFor(t, func() bool { return queueLen(qdir) == wantQueued })
	t.Cleanup(func() {
		cancel()
		<-done
		select {
		case r := <-ch: // never received by the test
			if r.slot != nil {
				_ = r.slot.Release()
			}
		default:
		}
	})
	return ch
}

// watchDeferral reports each time a bulk waiter defers to a live express
// ticket. A bulk waiter that last looked before the express ticket existed
// may be descheduled until after a slot frees, and then rightly take it:
// waiting for a deferral proves it has seen the express waiter, which keeps
// the priority tests deterministic. Call it before starting any waiter.
func watchDeferral(t *testing.T) <-chan struct{} {
	t.Helper()
	ch := make(chan struct{}, 1)
	deferredToExpress = func() {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	t.Cleanup(func() { deferredToExpress = nil }) // runs after the waiters' cleanups
	return ch
}

func withLane(opt Options, lane string) Options {
	opt.Lane = lane
	return opt
}

// receive waits for a background acquisition, failing the test if it errs.
func receive(t *testing.T, ch <-chan acquired) *Slot {
	t.Helper()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("Acquire: %v", r.err)
		}
		return r.slot
	case <-time.After(10 * time.Second):
		t.Fatal("acquisition did not complete within 10s")
		return nil
	}
}

func waitSignal(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("the bulk waiter never deferred to the express waiter")
	}
}

// notYet asserts a background acquisition is still waiting.
func notYet(t *testing.T, ch <-chan acquired, who string) {
	t.Helper()
	select {
	case r := <-ch:
		t.Fatalf("%s acquired (slot %v, err %v) while it should still wait", who, r.slot, r.err)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestLaneOrder_BulkNeverLocksAReservedIndex(t *testing.T) {
	c := Capacity{Class: Heavy, Slots: 8, Express: 2}
	for k := 0; k < 100; k++ {
		bulk := c.laneOrder(LaneBulk)
		if len(bulk) != 6 {
			t.Fatalf("bulk order %v: want the 6 unreserved slots", bulk)
		}
		for _, i := range bulk {
			if i < 0 || i >= 6 {
				t.Fatalf("bulk order %v includes reserved slot %d", bulk, i)
			}
		}
		express := c.laneOrder(LaneExpress)
		if len(express) != 8 {
			t.Fatalf("express order %v: want every slot", express)
		}
		if express[0] < 6 || express[1] < 6 {
			t.Fatalf("express order %v: want the reserved slots 6 and 7 tried first", express)
		}
	}
	// Through Acquire: with N=3 and R=1, a third bulk holder waits although
	// slot 2 is free.
	opt := testOpts(t, 3)
	for k := 0; k < 2; k++ {
		s := acquire(t, opt)
		defer func() { _ = s.Release() }()
		if s.Index() > 1 {
			t.Fatalf("bulk holder took reserved slot %d", s.Index())
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if _, err := Acquire(ctx, Heavy, opt); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("third bulk Acquire = %v, want it to wait: the free slot is reserved for express", err)
	}
	s := acquire(t, withLane(opt, LaneExpress))
	defer func() { _ = s.Release() }()
	if s.Index() != 2 || s.Lane() != LaneExpress || !strings.Contains(s.String(), "(express)") {
		t.Fatalf("express took %s (index %d), want reserved slot 2", s, s.Index())
	}
}

// The 2026-11-18 incident: every bulk slot held by long jobs and bulk
// waiters queued behind them. An express acquisition must not wait.
func TestAcquire_ExpressRunsWhileEveryBulkSlotIsHeld(t *testing.T) {
	opt := testOpts(t, 8)
	if c, _ := opt.capacity(Heavy); c.Express != 2 {
		t.Fatalf("N=8 reserves %d slots for express, want 2", c.Express)
	}
	for k := 0; k < 6; k++ {
		s := acquire(t, opt)
		defer func() { _ = s.Release() }()
	}
	for k := 1; k <= 3; k++ {
		startAcquire(t, opt, k) // bulk waiters, queued
	}
	express := withLane(opt, LaneExpress)
	for k := 0; k < 2; k++ {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		s, err := Acquire(ctx, Heavy, express)
		cancel()
		if err != nil {
			t.Fatalf("express Acquire #%d with 6 bulk holders and 3 bulk waiters: %v", k+1, err)
		}
		defer func() { _ = s.Release() }()
		// Waited is zero only on the path that never filed a ticket.
		if s.Index() < 6 || s.Waited() != 0 {
			t.Fatalf("express #%d: slot %d after waiting %s, want a reserved slot at once", k+1, s.Index(), s.Waited())
		}
	}
}

func TestAcquire_ExpressWaiterBeatsAnOlderBulkWaiter(t *testing.T) {
	opt := testOpts(t, 3) // R=1: bulk 0-1, reserved 2
	express := withLane(opt, LaneExpress)
	b1, b2 := acquire(t, opt), acquire(t, opt)
	e := acquire(t, express)
	defer func() { _ = b2.Release(); _ = e.Release() }()

	deferred := watchDeferral(t)
	bulkWaiter := startAcquire(t, opt, 1)
	expressWaiter := startAcquire(t, express, 1)
	waitSignal(t, deferred)

	_ = b1.Release()
	got := receive(t, expressWaiter)
	defer func() { _ = got.Release() }()
	if got.Index() > 1 {
		t.Fatalf("express waiter took slot %d, want the freed bulk slot", got.Index())
	}
	notYet(t, bulkWaiter, "the older bulk waiter")
	_ = got.Release()
	if s := receive(t, bulkWaiter); s.Index() > 1 {
		t.Fatalf("bulk waiter took reserved slot %d", s.Index())
	} else {
		_ = s.Release()
	}
}

// The check-to-lock race: a bulk waiter finds no express ticket, and before
// it locks the slot an express waiter files one and the slot frees. The bulk
// waiter must give the slot back to the express waiter, not keep it.
func TestAcquire_ExpressTicketFiledBetweenBulkCheckAndLockGoesFirst(t *testing.T) {
	opt := testOpts(t, 1) // R=0: one slot, both lanes contend for it
	held := acquire(t, opt)
	defer func() { _ = held.Release() }()

	reached, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	bulkAdmitting = func() {
		once.Do(func() {
			close(reached)
			<-resume
		})
	}
	t.Cleanup(func() { bulkAdmitting = nil }) // runs after the waiters' cleanups

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	bulkWaiter := make(chan acquired, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s, err := Acquire(ctx, Heavy, opt)
		bulkWaiter <- acquired{s, err}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		select {
		case r := <-bulkWaiter:
			if r.slot != nil {
				_ = r.slot.Release()
			}
		default:
		}
	})

	select {
	case <-reached:
	case <-time.After(10 * time.Second):
		t.Fatal("the bulk waiter never reached its slot lock")
	}
	// The bulk waiter has passed its express check: file an express ticket
	// and free the slot before it locks.
	expressWaiter := startAcquire(t, withLane(opt, LaneExpress), 1)
	_ = held.Release()
	close(resume)

	s := receive(t, expressWaiter)
	notYet(t, bulkWaiter, "the bulk waiter that checked before the express ticket was filed")
	_ = s.Release()
	_ = receive(t, bulkWaiter).Release()
}

func TestAcquire_SingleSlotReservesNoneButExpressGoesFirst(t *testing.T) {
	opt := testOpts(t, 1)
	if c, _ := opt.capacity(Heavy); c.Express != 0 {
		t.Fatalf("N=1 reserves %d slots, want 0: bulk would have none", c.Express)
	}
	held := acquire(t, opt)
	deferred := watchDeferral(t)
	bulkWaiter := startAcquire(t, opt, 1)
	expressWaiter := startAcquire(t, withLane(opt, LaneExpress), 1)
	waitSignal(t, deferred)
	_ = held.Release()
	s := receive(t, expressWaiter)
	notYet(t, bulkWaiter, "the older bulk waiter")
	_ = s.Release()
	_ = receive(t, bulkWaiter).Release()
}

func TestLane_Selection(t *testing.T) {
	for _, tc := range []struct {
		lane    string
		env     string
		want    string
		wantErr bool
	}{
		{want: LaneBulk},
		{env: "express", want: LaneExpress},
		{env: " Express ", want: LaneExpress},
		{env: "bulk", want: LaneBulk},
		{env: "fast", want: LaneBulk}, // a stray value never fails a command
		{lane: "bulk", env: "express", want: LaneBulk},
		{lane: "express", want: LaneExpress},
		{lane: "EXPRESS", env: "bulk", want: LaneExpress},
		{lane: "speedy", wantErr: true},
	} {
		opt := Options{Lane: tc.lane, Env: []string{EnvLane + "=" + tc.env}}
		got, err := opt.lane()
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("Lane=%q %s=%q: lane() = %q, %v; want %q (err %v)", tc.lane, EnvLane, tc.env, got, err, tc.want, tc.wantErr)
		}
	}
	opt := testOpts(t, 2)
	opt.Env = []string{EnvLane + "=express"}
	s := acquire(t, opt)
	if s.Lane() != LaneExpress || s.Index() != 1 {
		t.Fatalf("FORGE_SLOT_LANE=express: lane %q slot %d, want express in reserved slot 1", s.Lane(), s.Index())
	}
	_ = s.Release()
	if _, err := Acquire(context.Background(), Heavy, withLane(testOpts(t, 2), "speedy")); err == nil {
		t.Fatal("Acquire accepted Options.Lane naming no lane")
	}
}

func TestExpressSlots_DefaultOverrideAndClamp(t *testing.T) {
	for n, want := range map[int]int{1: 0, 2: 1, 3: 1, 4: 1, 7: 1, 8: 2, 16: 4} {
		if got := DefaultExpressSlots(n); got != want {
			t.Errorf("DefaultExpressSlots(%d) = %d, want %d", n, got, want)
		}
	}
	for _, tc := range []struct {
		slots, val string
		want       int
		clamped    bool
		wantErr    bool
	}{
		{slots: "8", want: 2},
		{slots: "8", val: "3", want: 3},
		{slots: "8", val: "0", want: 0},
		{slots: "8", val: "off", want: 0},
		{slots: "8", val: "8", want: 7, clamped: true},
		{slots: "8", val: "20", want: 7, clamped: true},
		{slots: "1", val: "1", want: 0, clamped: true},
		{slots: "8", val: "-1", wantErr: true},
		{slots: "8", val: "lots", wantErr: true},
		{slots: "off", val: "3", want: 0}, // no limit, nothing to reserve
	} {
		env := []string{SlotsEnv(Heavy) + "=" + tc.slots, ExpressSlotsEnv(Heavy) + "=" + tc.val}
		c, err := CapacityFor(Heavy, env)
		name := fmt.Sprintf("SLOTS=%s EXPRESS=%s", tc.slots, tc.val)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", name, err, tc.wantErr)
			continue
		}
		if err != nil {
			continue
		}
		if c.Express != tc.want || strings.Contains(c.ExpressSource, "clamped") != tc.clamped {
			t.Errorf("%s: Express=%d (%s), want %d clamped=%v", name, c.Express, c.ExpressSource, tc.want, tc.clamped)
		}
	}
	if got := ExpressSlotsEnv("long-tests"); got != "FORGE_LONG_TESTS_EXPRESS_SLOTS" {
		t.Errorf("ExpressSlotsEnv(long-tests) = %q", got)
	}
	// Options.Slots re-derives the default from the requested count, and
	// still honors the override.
	opt := testOpts(t, 12)
	if c, _ := opt.capacity(Heavy); c.Express != 3 {
		t.Errorf("Slots=12: Express=%d, want 3", c.Express)
	}
	opt.Env = []string{ExpressSlotsEnv(Heavy) + "=5"}
	if c, _ := opt.capacity(Heavy); c.Express != 5 {
		t.Errorf("Slots=12 with the override 5: Express=%d", c.Express)
	}
}

func TestAcquire_DeadExpressWaiterDoesNotBlockBulk(t *testing.T) {
	opt := testOpts(t, 1)
	qdir := filepath.Join(opt.Dir, Heavy, expressQueueDir)
	if err := os.MkdirAll(qdir, 0o755); err != nil {
		t.Fatal(err)
	}
	// An unlocked express ticket past the grace window: its waiter died.
	dead := fmt.Sprintf("%019d-%d", time.Now().Add(-time.Minute).UnixNano(), 999999)
	if err := os.WriteFile(filepath.Join(qdir, dead), []byte("pid=999999\nlane=express\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s, err := Acquire(ctx, Heavy, opt)
	if err != nil {
		t.Fatalf("bulk Acquire behind a dead express waiter: %v", err)
	}
	_ = s.Release()
	if _, err := os.Stat(filepath.Join(qdir, dead)); !os.IsNotExist(err) {
		t.Fatalf("the dead express waiter's ticket was not reaped: %v", err)
	}
}

func TestStatus_ReportsLanes(t *testing.T) {
	opt := testOpts(t, 3)
	express := withLane(opt, LaneExpress)
	b1, b2, e := acquire(t, opt), acquire(t, opt), acquire(t, express)
	defer func() { _ = b1.Release(); _ = b2.Release(); _ = e.Release() }()
	startAcquire(t, opt, 1)
	startAcquire(t, express, 1)

	rep, err := Status(Heavy, opt)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Capacity.Express != 1 || len(rep.Holders) != 3 {
		t.Fatalf("Status: Express=%d holders=%+v", rep.Capacity.Express, rep.Holders)
	}
	if h := rep.Holders[2]; h.Slot != 2 || h.Lane != LaneExpress {
		t.Fatalf("reserved slot holder %+v, want the express holder", h)
	}
	for _, h := range rep.Holders[:2] {
		if h.Lane != LaneBulk {
			t.Fatalf("bulk holder reports lane %q", h.Lane)
		}
	}
	if len(rep.Waiting) != 2 || rep.Waiting[0].Lane != LaneExpress || rep.Waiting[1].Lane != LaneBulk {
		t.Fatalf("Waiting = %+v, want the express waiter first, then the bulk one", rep.Waiting)
	}

	old := Holder{Slot: 2, Lane: LaneExpress, Since: time.Now().Add(-ExpressOverdue - time.Minute)}
	if !old.Overdue() {
		t.Error("an express holder past ExpressOverdue is not flagged")
	}
	old.Lane = LaneBulk
	if old.Overdue() {
		t.Error("a long bulk holder is flagged: bulk work has no time bound")
	}
}

func TestWaitingLine_NamesTheLane(t *testing.T) {
	c := Capacity{Class: Heavy, Slots: 8, Express: 2, Source: "test"}
	bulk := waitingLine(c, LaneBulk, nil, 3, 1, time.Minute)
	for _, want := range []string{"waiting for a heavy slot (bulk lane", "all 6 bulk slots held", "3 queued ahead", "1 express waiter(s) go first", "2 reserved for express", EnvLane + "=express"} {
		if !strings.Contains(bulk, want) {
			t.Errorf("bulk waiting line lacks %q:\n%s", want, bulk)
		}
	}
	express := waitingLine(c, LaneExpress, nil, 0, 0, time.Second)
	if !strings.Contains(express, "(express lane") || !strings.Contains(express, "all 8 slots held") || strings.Contains(express, EnvLane) {
		t.Errorf("express waiting line:\n%s", express)
	}
}
