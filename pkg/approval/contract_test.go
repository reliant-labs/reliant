package approval

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// fakeClock is a settable clock shared by a store and its test.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock { return &fakeClock{t: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// runContract runs the behaviour every Approvals implementation shares, so
// the in-memory store is proven to match Postgres.
func runContract(t *testing.T, newStore func(t *testing.T, clock *fakeClock) Approvals) {
	ctx := context.Background()
	propose := func(t *testing.T, s Approvals, clock *fakeClock, subject string) Item {
		t.Helper()
		it, err := s.Propose(ctx, Item{
			Scope: "acct", Kind: "trade", SubjectRef: subject, RunID: "run-1",
			Payload: []byte(`{"side":"buy"}`), Summary: "BUY 10 AAPL",
			ExpiresAt: Within(clock.Now(), time.Hour),
		})
		if err != nil {
			t.Fatalf("Propose(%s): %v", subject, err)
		}
		return it
	}
	ok := func(context.Context, Item, Decision) (string, error) { return "filled", nil }

	t.Run("propose records a pending item", func(t *testing.T) {
		clock := newClock()
		s := newStore(t, clock)
		it := propose(t, s, clock, "decisions/1")
		if it.ID == "" || it.State != StatePending || it.HandedOff || it.DecidedAt != nil {
			t.Fatalf("unexpected item: %+v", it)
		}
		if it.Scope != "acct" || it.Kind != "trade" || it.RunID != "run-1" || it.Summary != "BUY 10 AAPL" {
			t.Errorf("fields lost: %+v", it)
		}
		if !it.ExpiresAt.Equal(clock.Now().Add(time.Hour)) || !it.CreatedAt.Equal(clock.Now()) {
			t.Errorf("times: expires %v created %v", it.ExpiresAt, it.CreatedAt)
		}
		got, err := s.Get(ctx, it.ID)
		if err != nil || got.ID != it.ID || string(got.Payload) != `{"side": "buy"}` && string(got.Payload) != `{"side":"buy"}` {
			t.Errorf("Get = %+v, %v", got, err)
		}
	})

	t.Run("propose is idempotent on scope, kind and subject", func(t *testing.T) {
		clock := newClock()
		s := newStore(t, clock)
		first := propose(t, s, clock, "decisions/1")
		clock.Advance(time.Minute)
		again, err := s.Propose(ctx, Item{Scope: "acct", Kind: "trade", SubjectRef: "decisions/1",
			Summary: "changed", ExpiresAt: Within(clock.Now(), 5*time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		if again.ID != first.ID || again.Summary != first.Summary || !again.ExpiresAt.Equal(first.ExpiresAt) {
			t.Errorf("repeat Propose changed the item: %+v vs %+v", again, first)
		}
		// Still the same once decided.
		if _, err := s.Decide(ctx, Decision{ID: first.ID, Approve: false, By: "sean"}, nil); err != nil {
			t.Fatal(err)
		}
		after, err := s.Propose(ctx, Item{Scope: "acct", Kind: "trade", SubjectRef: "decisions/1", ExpiresAt: Within(clock.Now(), time.Hour)})
		if err != nil || after.ID != first.ID || after.State != StateRejected {
			t.Errorf("Propose after decide = %+v, %v; want the rejected item", after, err)
		}
		// Another scope or kind is a different subject.
		other, err := s.Propose(ctx, Item{Scope: "other", Kind: "trade", SubjectRef: "decisions/1", ExpiresAt: Within(clock.Now(), time.Hour)})
		if err != nil || other.ID == first.ID {
			t.Errorf("other scope = %+v, %v", other, err)
		}
	})

	t.Run("propose validates", func(t *testing.T) {
		clock := newClock()
		s := newStore(t, clock)
		for name, it := range map[string]Item{
			"no kind":     {SubjectRef: "x", ExpiresAt: clock.Now()},
			"no subject":  {Kind: "k", ExpiresAt: clock.Now()},
			"no deadline": {Kind: "k", SubjectRef: "x"},
			"bad payload": {Kind: "k", SubjectRef: "x", ExpiresAt: clock.Now(), Payload: []byte("{nope")},
		} {
			if _, err := s.Propose(ctx, it); !svcerr.IsInvalidArgument(err) {
				t.Errorf("%s: err = %v, want InvalidArgument", name, err)
			}
		}
	})

	t.Run("approve runs the follow-up once and records the outcome", func(t *testing.T) {
		clock := newClock()
		s := newStore(t, clock)
		it := propose(t, s, clock, "decisions/1")
		var calls int
		var seen Item
		got, err := s.Decide(ctx, Decision{ID: it.ID, Approve: true, By: "sean", Note: "ok"},
			func(_ context.Context, it Item, d Decision) (string, error) {
				calls++
				seen = it
				if d.By != "sean" {
					t.Errorf("follow-up decision = %+v", d)
				}
				return "filled", nil
			})
		if err != nil {
			t.Fatal(err)
		}
		if calls != 1 || seen.State != StateApproved || seen.DecidedBy != "sean" {
			t.Errorf("follow-up calls %d saw %+v", calls, seen)
		}
		if got.State != StateApproved || got.Outcome != "filled" || !got.HandedOff || got.DecidedBy != "sean" || got.Note != "ok" || got.DecidedAt == nil {
			t.Errorf("decided item = %+v", got)
		}
		// Deciding again is refused and does not run the follow-up.
		_, err = s.Decide(ctx, Decision{ID: it.ID, Approve: true, By: "sean"}, func(context.Context, Item, Decision) (string, error) {
			calls++
			return "", nil
		})
		if !svcerr.IsFailedPrecondition(err) || calls != 1 {
			t.Errorf("second Decide: err = %v, calls = %d", err, calls)
		}
	})

	t.Run("reject runs no follow-up", func(t *testing.T) {
		clock := newClock()
		s := newStore(t, clock)
		it := propose(t, s, clock, "decisions/1")
		got, err := s.Decide(ctx, Decision{ID: it.ID, Approve: false, By: "sean", Note: "no"},
			func(context.Context, Item, Decision) (string, error) {
				t.Error("follow-up ran for a rejection")
				return "", nil
			})
		if err != nil || got.State != StateRejected || got.DecidedBy != "sean" || got.Note != "no" || got.Outcome != "" {
			t.Errorf("rejected = %+v, %v", got, err)
		}
	})

	t.Run("approve with no follow-up has nothing to hand off", func(t *testing.T) {
		clock := newClock()
		s := newStore(t, clock)
		it := propose(t, s, clock, "decisions/1")
		got, err := s.Decide(ctx, Decision{ID: it.ID, Approve: true, By: "sean"}, nil)
		if err != nil || got.State != StateApproved || !got.HandedOff {
			t.Errorf("= %+v, %v", got, err)
		}
	})

	t.Run("a late decide expires the item and runs nothing", func(t *testing.T) {
		clock := newClock()
		s := newStore(t, clock)
		it := propose(t, s, clock, "decisions/1")
		clock.Advance(time.Hour) // exactly at the deadline: expired
		got, err := s.Decide(ctx, Decision{ID: it.ID, Approve: true, By: "sean", Note: "late"},
			func(context.Context, Item, Decision) (string, error) {
				t.Error("follow-up ran on an expired item")
				return "", nil
			})
		if err != nil || got.State != StateExpired || got.DecidedBy != "sean" || got.Note != "late" {
			t.Fatalf("late decide = %+v, %v", got, err)
		}
		if again, err := s.Get(ctx, it.ID); err != nil || again.State != StateExpired {
			t.Errorf("Get = %+v, %v", again, err)
		}
		// A second late decider sees the same expired item.
		got2, err := s.Decide(ctx, Decision{ID: it.ID, Approve: true, By: "ana"}, nil)
		if err != nil || got2.State != StateExpired || got2.DecidedBy != "sean" {
			t.Errorf("second late decide = %+v, %v", got2, err)
		}
	})

	t.Run("a decide after a sweeper expired the item records who tried", func(t *testing.T) {
		clock := newClock()
		s := newStore(t, clock)
		it := propose(t, s, clock, "decisions/1")
		clock.Advance(2 * time.Hour)
		swept, err := s.ExpireDue(ctx, "")
		if err != nil || len(swept) != 1 || swept[0].DecidedBy != "" {
			t.Fatalf("ExpireDue = %+v, %v", swept, err)
		}
		got, err := s.Decide(ctx, Decision{ID: it.ID, Approve: true, By: "sean"}, ok)
		if err != nil || got.State != StateExpired || got.DecidedBy != "sean" {
			t.Errorf("= %+v, %v", got, err)
		}
	})

	t.Run("just before the deadline can still be approved", func(t *testing.T) {
		clock := newClock()
		s := newStore(t, clock)
		it := propose(t, s, clock, "decisions/1")
		clock.Advance(time.Hour - time.Millisecond)
		got, err := s.Decide(ctx, Decision{ID: it.ID, Approve: true, By: "sean"}, ok)
		if err != nil || got.State != StateApproved {
			t.Errorf("= %+v, %v", got, err)
		}
	})

	t.Run("ExpireDue expires on or past the deadline, by scope", func(t *testing.T) {
		clock := newClock()
		s := newStore(t, clock)
		a := propose(t, s, clock, "decisions/a")
		b, err := s.Propose(ctx, Item{Scope: "other", Kind: "trade", SubjectRef: "decisions/b", ExpiresAt: Within(clock.Now(), time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		late, err := s.Propose(ctx, Item{Scope: "acct", Kind: "trade", SubjectRef: "decisions/c", ExpiresAt: Within(clock.Now(), 3*time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		if got, err := s.ExpireDue(ctx, ""); err != nil || len(got) != 0 {
			t.Fatalf("before the deadline: %+v, %v", got, err)
		}
		clock.Advance(time.Hour) // == ExpiresAt
		got, err := s.ExpireDue(ctx, "acct")
		if err != nil || len(got) != 1 || got[0].ID != a.ID || got[0].State != StateExpired || got[0].DecidedAt == nil {
			t.Fatalf("ExpireDue(acct) = %+v, %v", got, err)
		}
		if got, err := s.ExpireDue(ctx, "acct"); err != nil || len(got) != 0 {
			t.Errorf("ExpireDue is not repeatable: %+v, %v", got, err)
		}
		got, err = s.ExpireDue(ctx, "")
		if err != nil || len(got) != 1 || got[0].ID != b.ID {
			t.Fatalf("ExpireDue(all) = %+v, %v", got, err)
		}
		if it, _ := s.Get(ctx, late.ID); it.State != StatePending {
			t.Errorf("a later deadline expired: %+v", it)
		}
	})

	t.Run("a follow-up error leaves the item pending", func(t *testing.T) {
		clock := newClock()
		s := newStore(t, clock)
		it := propose(t, s, clock, "decisions/1")
		boom := errors.New("broker down")
		_, err := s.Decide(ctx, Decision{ID: it.ID, Approve: true, By: "sean"},
			func(context.Context, Item, Decision) (string, error) { return "", boom })
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want the follow-up's error", err)
		}
		got, err := s.Get(ctx, it.ID)
		if err != nil || got.State != StatePending || got.DecidedBy != "" || got.DecidedAt != nil || got.HandedOff {
			t.Fatalf("after failed follow-up: %+v, %v", got, err)
		}
		// And it can be approved afterwards.
		if got, err := s.Decide(ctx, Decision{ID: it.ID, Approve: true, By: "sean"}, ok); err != nil || got.State != StateApproved {
			t.Errorf("retry = %+v, %v", got, err)
		}
	})

	t.Run("a Rejection records a rejection with the reason", func(t *testing.T) {
		clock := newClock()
		s := newStore(t, clock)
		it := propose(t, s, clock, "decisions/1")
		got, err := s.Decide(ctx, Decision{ID: it.ID, Approve: true, By: "sean"},
			func(context.Context, Item, Decision) (string, error) {
				return "", &Rejection{Reason: "price moved 3%"}
			})
		if err != nil {
			t.Fatalf("a Rejection is not an error: %v", err)
		}
		if got.State != StateRejected || got.Outcome != "price moved 3%" || got.DecidedBy != "sean" || !got.HandedOff {
			t.Errorf("= %+v", got)
		}
	})

	t.Run("Redrive re-runs only approved items not handed off", func(t *testing.T) {
		clock := newClock()
		s := newStore(t, clock)
		stuck := propose(t, s, clock, "decisions/stuck")
		done := propose(t, s, clock, "decisions/done")
		pending := propose(t, s, clock, "decisions/pending")
		rejected := propose(t, s, clock, "decisions/rejected")

		got, err := s.Decide(ctx, Decision{ID: stuck.ID, Approve: true, By: "sean"},
			func(context.Context, Item, Decision) (string, error) {
				return "order sent, no ack", HandoffIncomplete(errors.New("timeout"))
			})
		if err != nil || got.State != StateApproved || got.HandedOff || got.Outcome != "order sent, no ack" {
			t.Fatalf("incomplete hand-off = %+v, %v", got, err)
		}
		if _, err := s.Decide(ctx, Decision{ID: done.ID, Approve: true, By: "sean"}, ok); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Decide(ctx, Decision{ID: rejected.ID, Approve: false, By: "sean"}, nil); err != nil {
			t.Fatal(err)
		}

		var ran []string
		fn := func(_ context.Context, it Item, d Decision) (string, error) {
			ran = append(ran, it.ID)
			if d.By != "sean" || !d.Approve {
				t.Errorf("redrive decision = %+v", d)
			}
			return "filled", nil
		}
		redone, err := s.Redrive(ctx, Filter{}, fn)
		if err != nil || len(redone) != 1 || redone[0].ID != stuck.ID || !redone[0].HandedOff || redone[0].Outcome != "filled" {
			t.Fatalf("Redrive = %+v, %v", redone, err)
		}
		if len(ran) != 1 || ran[0] != stuck.ID {
			t.Errorf("follow-up ran for %v, want only %s", ran, stuck.ID)
		}
		if again, err := s.Redrive(ctx, Filter{}, fn); err != nil || len(again) != 0 || len(ran) != 1 {
			t.Errorf("second Redrive = %+v, %v (ran %d)", again, err, len(ran))
		}
		if it, _ := s.Get(ctx, pending.ID); it.State != StatePending {
			t.Errorf("pending item touched: %+v", it)
		}
	})

	t.Run("Redrive keeps a failing item for the next call", func(t *testing.T) {
		clock := newClock()
		s := newStore(t, clock)
		it := propose(t, s, clock, "decisions/1")
		if _, err := s.Decide(ctx, Decision{ID: it.ID, Approve: true, By: "sean"},
			func(context.Context, Item, Decision) (string, error) { return "", HandoffIncomplete(nil) }); err != nil {
			t.Fatal(err)
		}
		boom := errors.New("still down")
		redone, err := s.Redrive(ctx, Filter{}, func(context.Context, Item, Decision) (string, error) { return "", boom })
		if !errors.Is(err, boom) || len(redone) != 0 {
			t.Fatalf("Redrive = %+v, %v", redone, err)
		}
		if got, _ := s.Get(ctx, it.ID); got.State != StateApproved || got.HandedOff {
			t.Fatalf("failed redrive changed the item: %+v", got)
		}
		redone, err = s.Redrive(ctx, Filter{}, ok)
		if err != nil || len(redone) != 1 {
			t.Errorf("next Redrive = %+v, %v", redone, err)
		}
	})

	t.Run("Redrive can turn an approval into a rejection", func(t *testing.T) {
		clock := newClock()
		s := newStore(t, clock)
		it := propose(t, s, clock, "decisions/1")
		if _, err := s.Decide(ctx, Decision{ID: it.ID, Approve: true, By: "sean"},
			func(context.Context, Item, Decision) (string, error) { return "", HandoffIncomplete(nil) }); err != nil {
			t.Fatal(err)
		}
		redone, err := s.Redrive(ctx, Filter{}, func(context.Context, Item, Decision) (string, error) {
			return "", &Rejection{Reason: "account closed"}
		})
		if err != nil || len(redone) != 1 || redone[0].State != StateRejected || redone[0].Outcome != "account closed" {
			t.Errorf("= %+v, %v", redone, err)
		}
	})

	t.Run("Cancel withdraws pending items only", func(t *testing.T) {
		clock := newClock()
		s := newStore(t, clock)
		a := propose(t, s, clock, "decisions/a")
		b := propose(t, s, clock, "decisions/b")
		other, err := s.Propose(ctx, Item{Scope: "other", Kind: "trade", SubjectRef: "decisions/z", ExpiresAt: Within(clock.Now(), time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Decide(ctx, Decision{ID: b.ID, Approve: true, By: "sean"}, ok); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Cancel(ctx, Filter{}, "all"); !svcerr.IsInvalidArgument(err) {
			t.Errorf("Cancel with no filter: err = %v, want InvalidArgument", err)
		}
		n, err := s.Cancel(ctx, Filter{Scope: "acct"}, "account retired")
		if err != nil || n != 1 {
			t.Fatalf("Cancel = %d, %v; want 1", n, err)
		}
		got, _ := s.Get(ctx, a.ID)
		if got.State != StateCancelled || got.Note != "account retired" {
			t.Errorf("cancelled = %+v", got)
		}
		if got, _ := s.Get(ctx, b.ID); got.State != StateApproved {
			t.Errorf("approved item cancelled: %+v", got)
		}
		if got, _ := s.Get(ctx, other.ID); got.State != StatePending {
			t.Errorf("other scope cancelled: %+v", got)
		}
		if _, err := s.Decide(ctx, Decision{ID: a.ID, Approve: true, By: "sean"}, ok); !svcerr.IsFailedPrecondition(err) {
			t.Errorf("Decide on cancelled: err = %v, want FailedPrecondition", err)
		}
		if n, err := s.Cancel(ctx, Filter{IDs: []string{a.ID}}, ""); err != nil || n != 0 {
			t.Errorf("Cancel again = %d, %v", n, err)
		}
	})

	t.Run("Decide validates and finds", func(t *testing.T) {
		clock := newClock()
		s := newStore(t, clock)
		it := propose(t, s, clock, "decisions/1")
		if _, err := s.Decide(ctx, Decision{ID: it.ID, Approve: true}, ok); !svcerr.IsInvalidArgument(err) {
			t.Errorf("no By: err = %v, want InvalidArgument", err)
		}
		if _, err := s.Decide(ctx, Decision{Approve: true, By: "x"}, ok); !svcerr.IsInvalidArgument(err) {
			t.Errorf("no id: err = %v, want InvalidArgument", err)
		}
		for _, id := range []string{"00000000-0000-4000-8000-000000000000", "not-a-uuid"} {
			if _, err := s.Decide(ctx, Decision{ID: id, Approve: true, By: "x"}, ok); !svcerr.IsNotFound(err) {
				t.Errorf("Decide(%s): err = %v, want NotFound", id, err)
			}
			if _, err := s.Get(ctx, id); !svcerr.IsNotFound(err) {
				t.Errorf("Get(%s): err = %v, want NotFound", id, err)
			}
		}
		if got, _ := s.Get(ctx, it.ID); got.State != StatePending {
			t.Errorf("a refused decide changed the item: %+v", got)
		}
	})

	t.Run("List filters and pages newest first", func(t *testing.T) {
		clock := newClock()
		s := newStore(t, clock)
		var ids []string
		for i, sub := range []string{"decisions/1", "decisions/2", "decisions/3"} {
			clock.Advance(time.Second)
			it := propose(t, s, clock, sub)
			ids = append(ids, it.ID)
			_ = i
		}
		if _, err := s.Decide(ctx, Decision{ID: ids[0], Approve: false, By: "sean"}, nil); err != nil {
			t.Fatal(err)
		}
		other, err := s.Propose(ctx, Item{Scope: "other", Kind: "post", SubjectRef: "p/1", RunID: "run-2", ExpiresAt: Within(clock.Now(), time.Hour)})
		if err != nil {
			t.Fatal(err)
		}

		for name, tc := range map[string]struct {
			f    Filter
			want int
		}{
			"all":        {Filter{}, 4},
			"scope":      {Filter{Scope: "acct"}, 3},
			"kind":       {Filter{Kind: "post"}, 1},
			"run":        {Filter{RunID: "run-2"}, 1},
			"subject":    {Filter{SubjectRef: "decisions/2"}, 1},
			"pending":    {Filter{Scope: "acct", States: []State{StatePending}}, 2},
			"rejected":   {Filter{States: []State{StateRejected}}, 1},
			"either":     {Filter{Scope: "acct", States: []State{StatePending, StateRejected}}, 3},
			"ids":        {Filter{IDs: []string{ids[1], other.ID}}, 2},
			"unknown id": {Filter{IDs: []string{"00000000-0000-4000-8000-000000000000"}}, 0},
		} {
			page, err := s.List(ctx, tc.f)
			if err != nil || len(page.Items) != tc.want {
				t.Errorf("%s: %d items, %v; want %d", name, len(page.Items), err, tc.want)
			}
		}

		var seen []string
		f := Filter{Scope: "acct", PageSize: 2}
		for {
			page, err := s.List(ctx, f)
			if err != nil {
				t.Fatal(err)
			}
			for _, it := range page.Items {
				seen = append(seen, it.ID)
			}
			if page.NextPageToken == "" {
				break
			}
			f.PageToken = page.NextPageToken
		}
		if len(seen) != 3 || seen[0] != ids[2] || seen[2] != ids[0] {
			t.Errorf("paged = %v, want newest first %v", seen, []string{ids[2], ids[1], ids[0]})
		}
		if _, err := s.List(ctx, Filter{PageToken: "garbage"}); !errors.Is(err, ErrBadPageToken) {
			t.Errorf("bad token: err = %v", err)
		}
	})

	t.Run("concurrent Decide has one winner", func(t *testing.T) {
		clock := newClock()
		s := newStore(t, clock)
		it := propose(t, s, clock, "decisions/1")
		const n = 16
		var followUps, winners, refused atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, err := s.Decide(ctx, Decision{ID: it.ID, Approve: true, By: "sean"},
					func(context.Context, Item, Decision) (string, error) {
						followUps.Add(1)
						time.Sleep(20 * time.Millisecond)
						return "filled", nil
					})
				switch {
				case err == nil:
					winners.Add(1)
				case svcerr.IsFailedPrecondition(err):
					refused.Add(1)
				default:
					t.Errorf("Decide: %v", err)
				}
			}()
		}
		close(start)
		wg.Wait()
		if followUps.Load() != 1 || winners.Load() != 1 || refused.Load() != n-1 {
			t.Fatalf("follow-ups %d, winners %d, refused %d; want 1, 1, %d", followUps.Load(), winners.Load(), refused.Load(), n-1)
		}
		if got, _ := s.Get(ctx, it.ID); got.State != StateApproved || !got.HandedOff {
			t.Errorf("final = %+v", got)
		}
	})

	t.Run("expiry racing approval ends in exactly one state", func(t *testing.T) {
		clock := newClock()
		s := newStore(t, clock)
		it := propose(t, s, clock, "decisions/1")
		clock.Advance(time.Hour) // the deadline passes while both are in flight
		var followUps atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			if _, err := s.ExpireDue(ctx, ""); err != nil {
				t.Errorf("ExpireDue: %v", err)
			}
		}()
		var decided Item
		go func() {
			defer wg.Done()
			<-start
			var err error
			decided, err = s.Decide(ctx, Decision{ID: it.ID, Approve: true, By: "sean"},
				func(context.Context, Item, Decision) (string, error) { followUps.Add(1); return "", nil })
			if err != nil {
				t.Errorf("Decide: %v", err)
			}
		}()
		close(start)
		wg.Wait()
		got, _ := s.Get(ctx, it.ID)
		if got.State != StateExpired || decided.State != StateExpired || followUps.Load() != 0 {
			t.Errorf("final %+v, Decide returned %+v, follow-ups %d; want expired and none", got, decided, followUps.Load())
		}
	})
}
