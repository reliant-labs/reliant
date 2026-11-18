package testkit

import (
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fatalRecorder is a testing.TB whose Fatalf records the message and stops
// the goroutine, as the real one does.
type fatalRecorder struct {
	testing.TB
	msg string
}

func (r *fatalRecorder) Helper() {}
func (r *fatalRecorder) Fatalf(format string, args ...any) {
	r.msg = fmt.Sprintf(format, args...)
	runtime.Goexit()
}

// run calls fn with a recorder on its own goroutine and returns the Fatalf
// message, "" when fn returned normally.
func run(t *testing.T, fn func(tb testing.TB)) string {
	t.Helper()
	r := &fatalRecorder{TB: t}
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn(r)
	}()
	<-done
	return r.msg
}

func TestEventuallyReturnsOnceTheConditionHolds(t *testing.T) {
	var calls atomic.Int32
	msg := run(t, func(tb testing.TB) {
		Eventually(tb, "the third check", func() bool { return calls.Add(1) >= 3 })
	})
	if msg != "" {
		t.Fatalf("Eventually failed: %s", msg)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("cond ran %d times, want 3: it must stop polling once it holds", got)
	}
}

func TestEventuallyFailsNamingTheEvent(t *testing.T) {
	msg := run(t, func(tb testing.TB) {
		Eventually(tb, "the queue drains", func() bool { return false }, Within(20*time.Millisecond))
	})
	if !strings.Contains(msg, "the queue drains did not happen within 20ms") {
		t.Fatalf("failure message = %q, want it to name the event and the deadline", msg)
	}
}

func TestReceiveReturnsTheValue(t *testing.T) {
	ch := make(chan int, 1)
	ch <- 7
	var got int
	if msg := run(t, func(tb testing.TB) { got = Receive(tb, "the reply", ch) }); msg != "" {
		t.Fatalf("Receive failed: %s", msg)
	}
	if got != 7 {
		t.Fatalf("Receive = %d, want 7", got)
	}
}

func TestReceiveTreatsCloseAsTheEvent(t *testing.T) {
	done := make(chan struct{})
	close(done)
	if msg := run(t, func(tb testing.TB) { Receive(tb, "done", done) }); msg != "" {
		t.Fatalf("Receive on a closed channel failed: %s", msg)
	}
}

func TestReceiveFailsNamingTheEvent(t *testing.T) {
	msg := run(t, func(tb testing.TB) {
		Receive(tb, "the reply", make(chan int), Within(10*time.Millisecond))
	})
	if !strings.Contains(msg, "the reply did not arrive within 10ms") {
		t.Fatalf("failure message = %q, want it to name the event and the deadline", msg)
	}
}

// deadlineTB reports a test-binary deadline.
type deadlineTB struct {
	testing.TB
	end time.Time
}

func (d deadlineTB) Deadline() (time.Time, bool) { return d.end, true }

func TestWaitEndsBeforeTheBinaryDeadline(t *testing.T) {
	near := deadlineTB{TB: t, end: time.Now().Add(deadlineGrace + 10*time.Second)}
	if got := waitTimeout(near, nil); got > 10*time.Second || got < 9*time.Second {
		t.Fatalf("timeout = %s, want about 10s: the binary's -timeout comes first", got)
	}
	far := deadlineTB{TB: t, end: time.Now().Add(time.Hour)}
	if got := waitTimeout(far, nil); got != DefaultWaitTimeout {
		t.Fatalf("timeout = %s, want DefaultWaitTimeout", got)
	}
	if got := waitTimeout(far, []WaitOption{Within(time.Minute)}); got != time.Minute {
		t.Fatalf("timeout = %s, want Within's 1m", got)
	}
}
