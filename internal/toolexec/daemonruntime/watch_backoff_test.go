package daemonruntime

import (
	"testing"
	"time"
)

func TestWatchBackoffDoublesToTheCapAndResetsOnRecovery(t *testing.T) {
	b := newWatchBackoff(2 * time.Second)

	if wait, changed := b.next(false); wait != 2*time.Second || changed {
		t.Fatalf("healthy poll = %v, %v; want the interval and no transition", wait, changed)
	}

	// The first failure starts a streak (log once); later ones do not.
	want := []time.Duration{4 * time.Second, 8 * time.Second, 16 * time.Second}
	for i, w := range want {
		wait, changed := b.next(true)
		if wait != w || changed != (i == 0) {
			t.Fatalf("failure %d = %v, %v; want %v, %v", i, wait, changed, w, i == 0)
		}
	}
	for range 20 {
		b.next(true)
	}
	if wait, changed := b.next(true); wait != watchFailureBackoffMax || changed {
		t.Fatalf("long failure streak = %v, %v; want the %v cap and no transition", wait, changed, watchFailureBackoffMax)
	}

	// Recovery ends the streak (log once) and returns to the healthy cadence.
	if wait, changed := b.next(false); wait != 2*time.Second || !changed {
		t.Fatalf("recovery = %v, %v; want the interval and a transition", wait, changed)
	}
	if wait, changed := b.next(true); wait != 4*time.Second || !changed {
		t.Fatalf("new streak = %v, %v; want it to start over", wait, changed)
	}
}
