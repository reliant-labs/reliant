package cronkit_test

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/reliant-labs/forge/pkg/cronkit"
	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/pgtest"
	"github.com/reliant-labs/forge/pkg/testkit"
)

// The ORM handle a forge worker's Deps carries is what the scaffolded cron
// worker hands to cronkit.Postgres.
var (
	_ cronkit.DB = orm.Context(nil)
	_ cronkit.DB = (*orm.Client)(nil)
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func testDSN(t *testing.T) string {
	t.Helper()
	testkit.Slow(t, "boots real postgres (pgtest)")
	dsn, cleanup, err := pgtest.NewURL()
	if err != nil {
		t.Fatalf("test postgres: %v", err)
	}
	t.Cleanup(cleanup)
	return dsn
}

// pool opens an independent pool on dsn, so each "replica" has its own
// connections exactly as separate processes would.
func pool(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestPostgresClaimsHaveOneWinnerPerTick(t *testing.T) {
	dsn := testDSN(t)
	ctx := context.Background()
	tick := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)

	// Eight replicas, each on a fresh process's view of a database with no
	// table yet, race the first claim: the table is created once, and one
	// claim lands.
	const n = 8
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins []string
	)
	for i := range n {
		c := cronkit.Postgres(cronkit.SQLDB(pool(t, dsn)), cronkit.PostgresOptions{Replica: string(rune('a' + i))})
		wg.Add(1)
		go func() {
			defer wg.Done()
			won, err := c.Claim(ctx, "poll-reddit", tick)
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			if won {
				mu.Lock()
				wins = append(wins, "won")
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(wins) != 1 {
		t.Fatalf("%d replicas won the same tick, want exactly 1", len(wins))
	}

	c := cronkit.Postgres(cronkit.SQLDB(pool(t, dsn)))
	if won, err := c.Claim(ctx, "poll-reddit", tick.Add(time.Minute)); err != nil || !won {
		t.Fatalf("the next tick = %v, %v; want won", won, err)
	}
	if won, err := c.Claim(ctx, "poll-hn", tick); err != nil || !won {
		t.Fatalf("another job's same tick = %v, %v; want won (the key is job AND tick)", won, err)
	}
}

func TestPostgresWinnerPrunesItsOldTicks(t *testing.T) {
	dsn := testDSN(t)
	ctx := context.Background()
	db := pool(t, dsn)
	c := cronkit.Postgres(cronkit.SQLDB(db), cronkit.PostgresOptions{Retention: time.Hour})
	old := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	for _, tick := range []time.Time{old, old.Add(30 * time.Minute), old.Add(2 * time.Hour)} {
		if _, err := c.Claim(ctx, "poll", tick); err != nil {
			t.Fatal(err)
		}
	}
	var left int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM `+cronkit.Table+` WHERE job = 'poll'`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 1 {
		t.Fatalf("%d rows left, want 1: rows older than the retention before the newest win are pruned", left)
	}
}

// Two runners, each on its own pool as two replicas would be, run the same
// schedule against one database for a few ticks: every tick fires exactly
// once between them.
func TestTwoRunnersOnPostgresFireOncePerTick(t *testing.T) {
	dsn := testDSN(t)
	var (
		mu    sync.Mutex
		fired = map[time.Time][]string{}
	)
	job := func(replica string) func(context.Context) error {
		return func(ctx context.Context) error {
			at, _ := cronkit.TickTime(ctx)
			mu.Lock()
			fired[at] = append(fired[at], replica)
			mu.Unlock()
			return nil
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3500*time.Millisecond)
	defer cancel()
	var wg sync.WaitGroup
	for _, replica := range []string{"api-0", "api-1"} {
		claims := cronkit.Postgres(cronkit.SQLDB(pool(t, dsn)), cronkit.PostgresOptions{Replica: replica})
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := cronkit.Run(ctx, cronkit.Options{Name: "poll-reddit", Schedule: "@every 1s", Claims: claims, Logger: quiet}, job(replica))
			if err != nil {
				t.Errorf("%s: %v", replica, err)
			}
		}()
	}
	wg.Wait()

	if len(fired) < 2 {
		t.Fatalf("only %d ticks fired in 3.5s of @every 1s: %v", len(fired), fired)
	}
	for at, by := range fired {
		if len(by) != 1 {
			t.Errorf("tick %s fired on %v, want exactly one replica", at.Format(time.RFC3339), by)
		}
	}

	var rows int
	if err := pool(t, dsn).QueryRow(`SELECT count(*) FROM ` + cronkit.Table + ` WHERE job = 'poll-reddit'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != len(fired) {
		t.Errorf("%d claim rows for %d fired ticks", rows, len(fired))
	}
}

// The scaffolded worker passes its ORM handle; one built on a pool is a
// working claimant too.
func TestPostgresOverTheORM(t *testing.T) {
	dsn := testDSN(t)
	client, err := orm.NewClientWithDB(pool(t, dsn), "postgres")
	if err != nil {
		t.Fatal(err)
	}
	var db orm.Context = client
	c := cronkit.Postgres(db)
	tick := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	if won, err := c.Claim(context.Background(), "poll", tick); err != nil || !won {
		t.Fatalf("first claim = %v, %v", won, err)
	}
	if won, err := c.Claim(context.Background(), "poll", tick); err != nil || won {
		t.Fatalf("second claim = %v, %v; want lost", won, err)
	}

	var none *orm.Client
	if cronkit.Postgres(orm.Context(none)) != nil {
		t.Fatal("an ORM handle with no database must be no claims")
	}
}
