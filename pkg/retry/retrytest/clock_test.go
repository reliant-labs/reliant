package retrytest

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/retry"
)

var start = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func TestClockNowStartsAtStart(t *testing.T) {
	c := NewClock(start)
	if got := c.Now(); !got.Equal(start) {
		t.Errorf("Now() = %v, want %v", got, start)
	}
}

func TestClockSleepRecordsAndAdvancesWithoutWaiting(t *testing.T) {
	c := NewClock(start)
	waits := []time.Duration{time.Hour, 0, 90 * time.Second}

	began := time.Now()
	for _, d := range waits {
		if err := c.Sleep(context.Background(), d); err != nil {
			t.Fatalf("Sleep(%v): %v", d, err)
		}
	}
	if elapsed := time.Since(began); elapsed > 30*time.Second {
		t.Errorf("Sleep really waited: %v elapsed", elapsed)
	}

	got := c.Sleeps()
	if len(got) != len(waits) {
		t.Fatalf("Sleeps() = %v, want %v", got, waits)
	}
	for i := range waits {
		if got[i] != waits[i] {
			t.Errorf("Sleeps()[%d] = %v, want %v", i, got[i], waits[i])
		}
	}
	want := start.Add(time.Hour + 90*time.Second)
	if now := c.Now(); !now.Equal(want) {
		t.Errorf("Now() after sleeps = %v, want %v", now, want)
	}
}

func TestClockSleepOnEndedContextRecordsNothing(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()

	tests := []struct {
		name string
		ctx  context.Context
		want error
	}{
		{"canceled", canceled, context.Canceled},
		{"deadline exceeded", expired, context.DeadlineExceeded},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewClock(start)
			err := c.Sleep(tt.ctx, time.Minute)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Sleep error = %v, want %v", err, tt.want)
			}
			if got := c.Sleeps(); len(got) != 0 {
				t.Errorf("Sleeps() = %v, want none recorded", got)
			}
			if now := c.Now(); !now.Equal(start) {
				t.Errorf("Now() = %v, clock must not advance when the sleep is refused", now)
			}
		})
	}
}

func TestClockAdvanceMovesTimeButIsNotASleep(t *testing.T) {
	c := NewClock(start)
	c.Advance(5 * time.Second)
	c.Advance(2 * time.Second)

	if now, want := c.Now(), start.Add(7*time.Second); !now.Equal(want) {
		t.Errorf("Now() = %v, want %v", now, want)
	}
	if got := c.Sleeps(); len(got) != 0 {
		t.Errorf("Advance must not record a sleep, got %v", got)
	}
}

func TestClockSleepsReturnsACopy(t *testing.T) {
	c := NewClock(start)
	if err := c.Sleep(context.Background(), time.Second); err != nil {
		t.Fatal(err)
	}
	got := c.Sleeps()
	got[0] = time.Hour
	if again := c.Sleeps(); again[0] != time.Second {
		t.Errorf("mutating the returned slice changed the clock's record: %v", again)
	}
}

func TestClockIsSafeForConcurrentUse(t *testing.T) {
	c := NewClock(start)
	const workers, each = 8, 50
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				_ = c.Sleep(context.Background(), time.Millisecond)
				c.Advance(time.Millisecond)
				_ = c.Now()
				_ = c.Sleeps()
			}
		}()
	}
	wg.Wait()

	if got := len(c.Sleeps()); got != workers*each {
		t.Errorf("recorded %d sleeps, want %d", got, workers*each)
	}
	want := start.Add(2 * workers * each * time.Millisecond)
	if now := c.Now(); !now.Equal(want) {
		t.Errorf("Now() = %v, want %v", now, want)
	}
}

// TestClockDrivesARetryLoop is the double's reason to exist: retry.Do waits
// through it, so a loop with long backoffs finishes instantly and the exact
// waits are assertable.
func TestClockDrivesARetryLoop(t *testing.T) {
	c := NewClock(start)
	policy := retry.Policy{
		MaxAttempts: 4,
		Backoff:     retry.Backoff{Initial: 10 * time.Second, Multiplier: 1},
		Clock:       c,
	}

	calls := 0
	err := retry.Do(context.Background(), policy, func(context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
	got := c.Sleeps()
	if len(got) != 2 || got[0] != 10*time.Second || got[1] != 10*time.Second {
		t.Errorf("Sleeps() = %v, want two 10s waits", got)
	}
}
