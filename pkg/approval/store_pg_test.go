package approval

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// newPG boots postgres, applies SchemaSQL and returns the orm client the
// way a forge project wires it.
func newPG(t *testing.T) *orm.Client {
	t.Helper()
	if testing.Short() {
		t.Skip("boots real postgres; skipped under -short")
	}
	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("test postgres: %v", err)
	}
	t.Cleanup(cleanup)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(32)
	if _, err := db.ExecContext(context.Background(), SchemaSQL); err != nil {
		t.Fatalf("apply SchemaSQL: %v", err)
	}
	client, err := orm.NewClientWithDB(db, "postgres")
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestStore_Contract(t *testing.T) {
	client := newPG(t)
	runContract(t, func(t *testing.T, clock *fakeClock) Approvals {
		// A fresh table per subtest: the contract assumes an empty store.
		if _, err := client.Exec(context.Background(), `TRUNCATE approvals`); err != nil {
			t.Fatal(err)
		}
		return NewStore(client, WithNow(clock.Now))
	})
}

func TestStore_SchemaRequiresADeadline(t *testing.T) {
	client := newPG(t)
	_, err := client.Exec(context.Background(), `INSERT INTO approvals (kind, subject_ref, expires_at) VALUES ('k', 's', NULL)`)
	if err == nil || !strings.Contains(err.Error(), "expires_at") {
		t.Errorf("insert with no deadline: err = %v, want a NOT NULL violation on expires_at", err)
	}
	_, err = client.Exec(context.Background(), `INSERT INTO approvals (kind, subject_ref, expires_at, state) VALUES ('k', 's', now(), 'bogus')`)
	if err == nil {
		t.Error("insert with an unknown state succeeded")
	}
}

// The follow-up's own writes, made through the same client with the ctx it
// receives, commit or roll back with the decision.
func TestStore_FollowUpWritesShareTheTransaction(t *testing.T) {
	client := newPG(t)
	ctx := context.Background()
	if _, err := client.Exec(ctx, `CREATE TABLE orders (id text PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	clock := newClock()
	s := NewStore(client, WithNow(clock.Now))
	count := func() int {
		var n int
		if err := client.QueryRow(ctx, `SELECT count(*) FROM orders`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	place := func(id string, then error) FollowUp {
		return func(ctx context.Context, it Item, _ Decision) (string, error) {
			if _, err := client.Exec(ctx, `INSERT INTO orders (id) VALUES ($1)`, id); err != nil {
				return "", err
			}
			return id, then
		}
	}
	propose := func(subject string) Item {
		it, err := s.Propose(ctx, Item{Kind: "trade", SubjectRef: subject, ExpiresAt: Within(clock.Now(), time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		return it
	}

	failing := propose("fails")
	boom := errors.New("broker down")
	if _, err := s.Decide(ctx, Decision{ID: failing.ID, Approve: true, By: "sean"}, place("o-fails", boom)); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if n := count(); n != 0 {
		t.Errorf("a failed follow-up left %d order rows", n)
	}
	if got, _ := s.Get(ctx, failing.ID); got.State != StatePending {
		t.Errorf("failed follow-up left the item %s", got.State)
	}

	good := propose("works")
	if got, err := s.Decide(ctx, Decision{ID: good.ID, Approve: true, By: "sean"}, place("o-works", nil)); err != nil || got.Outcome != "o-works" {
		t.Fatalf("Decide = %+v, %v", got, err)
	}
	if n := count(); n != 1 {
		t.Errorf("orders = %d, want 1", n)
	}

	// A Rejection discards the follow-up's writes but commits the rejection.
	refused := propose("refused")
	got, err := s.Decide(ctx, Decision{ID: refused.ID, Approve: true, By: "sean"}, place("o-refused", &Rejection{Reason: "stale price"}))
	if err != nil || got.State != StateRejected || got.Outcome != "stale price" {
		t.Fatalf("Decide = %+v, %v", got, err)
	}
	if n := count(); n != 1 {
		t.Errorf("a Rejection kept the follow-up's writes: %d order rows, want 1", n)
	}

	// HandoffIncomplete keeps the follow-up's writes (they are the intent
	// Redrive completes) and leaves the item approved.
	stuck := propose("stuck")
	got, err = s.Decide(ctx, Decision{ID: stuck.ID, Approve: true, By: "sean"}, place("o-stuck", HandoffIncomplete(boom)))
	if err != nil || got.State != StateApproved || got.HandedOff {
		t.Fatalf("Decide = %+v, %v", got, err)
	}
	if n := count(); n != 2 {
		t.Errorf("orders = %d, want 2 (intent kept)", n)
	}
}

// When the handler already holds a transaction, Decide joins it: rolling
// that transaction back undoes the decision.
func TestStore_DecideJoinsTheCallersTransaction(t *testing.T) {
	client := newPG(t)
	ctx := context.Background()
	clock := newClock()
	s := NewStore(client, WithNow(clock.Now))
	it, err := s.Propose(ctx, Item{Kind: "trade", SubjectRef: "s", ExpiresAt: Within(clock.Now(), time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	abort := errors.New("abort the rpc")
	err = client.RunTx(ctx, orm.ReadCommitted, func(ctx context.Context) error {
		if _, err := s.Decide(ctx, Decision{ID: it.ID, Approve: true, By: "sean"}, nil); err != nil {
			return err
		}
		return abort
	})
	if !errors.Is(err, abort) {
		t.Fatalf("err = %v", err)
	}
	if got, _ := s.Get(ctx, it.ID); got.State != StatePending {
		t.Errorf("state = %s after the outer transaction rolled back, want pending", got.State)
	}
}

func TestStore_ConcurrentProposeIsOneItem(t *testing.T) {
	client := newPG(t)
	ctx := context.Background()
	clock := newClock()
	s := NewStore(client, WithNow(clock.Now))
	const n = 16
	ids := make(chan string, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			it, err := s.Propose(ctx, Item{Scope: "a", Kind: "trade", SubjectRef: "same", ExpiresAt: Within(clock.Now(), time.Hour)})
			if err != nil {
				t.Errorf("Propose: %v", err)
				return
			}
			ids <- it.ID
		}()
	}
	close(start)
	wg.Wait()
	close(ids)
	seen := map[string]bool{}
	for id := range ids {
		seen[id] = true
	}
	if len(seen) != 1 {
		t.Errorf("%d distinct items for one subject, want 1", len(seen))
	}
}

// Two redrivers racing over one stuck item run its follow-up once.
func TestStore_ConcurrentRedriveRunsOnce(t *testing.T) {
	client := newPG(t)
	ctx := context.Background()
	clock := newClock()
	s := NewStore(client, WithNow(clock.Now))
	it, err := s.Propose(ctx, Item{Kind: "trade", SubjectRef: "s", ExpiresAt: Within(clock.Now(), time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Decide(ctx, Decision{ID: it.ID, Approve: true, By: "sean"},
		func(context.Context, Item, Decision) (string, error) { return "", HandoffIncomplete(nil) }); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := s.Redrive(ctx, Filter{}, func(context.Context, Item, Decision) (string, error) {
				calls.Add(1)
				return "done", nil
			})
			if err != nil {
				t.Errorf("Redrive: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if calls.Load() != 1 {
		t.Errorf("follow-up ran %d times, want 1", calls.Load())
	}
	if got, _ := s.Get(ctx, it.ID); !got.HandedOff || got.Outcome != "done" {
		t.Errorf("final = %+v", got)
	}
}

// A redriver that reaches an item another redriver holds waits for it rather
// than passing over it: when the holder leaves it incomplete, the waiter runs
// it and reports it, so a successful Redrive never hides held work.
func TestStore_RedriveWaitsForAHeldItem(t *testing.T) {
	client := newPG(t)
	ctx := context.Background()
	clock := newClock()
	s := NewStore(client, WithNow(clock.Now))
	it, err := s.Propose(ctx, Item{Kind: "trade", SubjectRef: "s", ExpiresAt: Within(clock.Now(), time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Decide(ctx, Decision{ID: it.ID, Approve: true, By: "sean"},
		func(context.Context, Item, Decision) (string, error) { return "", HandoffIncomplete(nil) }); err != nil {
		t.Fatal(err)
	}

	entered, release := make(chan struct{}), make(chan struct{})
	holderErr := make(chan error, 1)
	go func() {
		_, err := s.Redrive(ctx, Filter{}, func(context.Context, Item, Decision) (string, error) {
			close(entered)
			<-release
			return "", HandoffIncomplete(nil)
		})
		holderErr <- err
	}()
	<-entered

	type result struct {
		items []Item
		err   error
	}
	waiter := make(chan result, 1)
	go func() {
		items, err := s.Redrive(ctx, Filter{}, func(context.Context, Item, Decision) (string, error) {
			return "done", nil
		})
		waiter <- result{items, err}
	}()

	// Release the holder only once the waiter is blocked on the row lock.
	deadline := time.Now().Add(30 * time.Second)
	for {
		rows, err := client.Query(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'`)
		if err != nil {
			t.Fatal(err)
		}
		var waiting int
		for rows.Next() {
			if err := rows.Scan(&waiting); err != nil {
				t.Fatal(err)
			}
		}
		_ = rows.Close()
		if waiting > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second redriver never waited on the held item")
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(release)

	if err := <-holderErr; err != nil {
		t.Fatalf("holder Redrive: %v", err)
	}
	got := <-waiter
	if got.err != nil || len(got.items) != 1 || got.items[0].ID != it.ID || got.items[0].Outcome != "done" {
		t.Fatalf("waiting Redrive = %+v, %v; want the held item, handed off", got.items, got.err)
	}
}

// Expiry running while a decision is in flight: whichever statement takes the
// row lock first, the item ends expired and the follow-up never runs, because
// the deadline had passed before either started.
func TestStore_ExpiryRacingApprovalRepeated(t *testing.T) {
	client := newPG(t)
	ctx := context.Background()
	clock := newClock()
	s := NewStore(client, WithNow(clock.Now))
	for i := 0; i < 20; i++ {
		it, err := s.Propose(ctx, Item{Kind: "trade", SubjectRef: "s" + string(rune('a'+i)), ExpiresAt: Within(clock.Now(), time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		_ = it
	}
	clock.Advance(time.Minute)
	page, err := s.List(ctx, Filter{PageSize: MaxPageSize})
	if err != nil {
		t.Fatal(err)
	}
	var followUps atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		if _, err := s.ExpireDue(ctx, ""); err != nil {
			t.Errorf("ExpireDue: %v", err)
		}
	}()
	for _, it := range page.Items {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			<-start
			_, err := s.Decide(ctx, Decision{ID: id, Approve: true, By: "sean"},
				func(context.Context, Item, Decision) (string, error) { followUps.Add(1); return "", nil })
			if err != nil && !svcerr.IsFailedPrecondition(err) {
				t.Errorf("Decide: %v", err)
			}
		}(it.ID)
	}
	close(start)
	wg.Wait()
	if followUps.Load() != 0 {
		t.Errorf("follow-up ran %d times on expired items", followUps.Load())
	}
	after, err := s.List(ctx, Filter{PageSize: MaxPageSize})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range after.Items {
		if it.State != StateExpired {
			t.Errorf("item %s is %s, want expired", it.SubjectRef, it.State)
		}
	}
}

func TestStore_ListBadPageTokenID(t *testing.T) {
	client := newPG(t)
	tok := encodePageToken(time.Now(), "not-a-uuid")
	if _, err := NewStore(client).List(context.Background(), Filter{PageToken: tok}); !errors.Is(err, ErrBadPageToken) {
		t.Errorf("err = %v, want ErrBadPageToken", err)
	}
}
