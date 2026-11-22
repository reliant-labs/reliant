package cronkit

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

// stepClock is a replica's clock that jumps straight to each wait's end, so
// a schedule's hours of ticks run in microseconds. Past end it cancels the
// run and never fires again.
type stepClock struct {
	mu     sync.Mutex
	now    time.Time
	end    time.Time
	cancel context.CancelFunc
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *stepClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.now.Add(d).After(c.end) {
		c.cancel()
		return nil
	}
	c.now = c.now.Add(d)
	ch := make(chan time.Time, 1)
	ch <- c.now
	return ch
}

// memClaims is the claim semantics Postgres has, in memory: first claim of a
// (job, tick) wins.
type memClaims struct {
	mu    sync.Mutex
	won   map[string]bool
	calls int
}

func (m *memClaims) Claim(_ context.Context, job string, tick time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.won == nil {
		m.won = map[string]bool{}
	}
	k := job + "@" + tick.UTC().Format(time.RFC3339)
	if m.won[k] {
		return false, nil
	}
	m.won[k] = true
	return true, nil
}

type errClaims struct{}

func (errClaims) Claim(context.Context, string, time.Time) (bool, error) {
	return false, errors.New("database is down")
}

// fires records each tick a job ran, by scheduled time.
type fires struct {
	mu sync.Mutex
	by map[time.Time]int
	n  int
}

func (f *fires) job(ctx context.Context) error {
	at, ok := TickTime(ctx)
	if !ok {
		return errors.New("tick ctx carries no TickTime")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.by == nil {
		f.by = map[time.Time]int{}
	}
	f.by[at]++
	f.n++
	return nil
}

var start = time.Date(2026, 11, 4, 10, 0, 30, 0, time.UTC)

// replicas runs n copies of a job for span of simulated time, each with its
// own clock (offset by skew per replica, as real pods wake a little apart)
// and all sharing claims.
func replicas(t *testing.T, n int, span, skew time.Duration, opts Options, job func(context.Context) error) {
	t.Helper()
	var wg sync.WaitGroup
	for i := range n {
		ctx, cancel := context.WithCancel(context.Background())
		o := opts
		begin := start.Add(time.Duration(i) * skew)
		o.clock = &stepClock{now: begin, end: start.Add(span), cancel: cancel}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer cancel()
			if err := Run(ctx, o, job); err != nil {
				t.Errorf("Run: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestEachTickFiresOnExactlyOneReplica(t *testing.T) {
	claims := &memClaims{}
	f := &fires{}
	replicas(t, 3, 10*time.Minute, 150*time.Millisecond,
		Options{Name: "poll-reddit", Schedule: "* * * * *", Claims: claims, Logger: quiet}, f.job)

	if f.n != 10 {
		t.Fatalf("fired %d times across 3 replicas over 10 minutes, want 10 (once per tick)", f.n)
	}
	for at, n := range f.by {
		if n != 1 {
			t.Errorf("tick %s fired %d times, want 1", at, n)
		}
		if at.Second() != 0 || at.Nanosecond() != 0 {
			t.Errorf("tick %s is not the scheduled minute: the claim key must be the schedule's time, not a replica's wake time", at)
		}
	}
	if claims.calls != 30 {
		t.Errorf("claims consulted %d times, want 30 (every replica, every tick)", claims.calls)
	}
}

func TestPerReplicaFiresEverywhereAndNeverClaims(t *testing.T) {
	claims := &memClaims{}
	f := &fires{}
	replicas(t, 3, 10*time.Minute, 0,
		Options{Name: "cache-refresh", Schedule: "* * * * *", Claims: claims, PerReplica: true, Logger: quiet}, f.job)

	if f.n != 30 {
		t.Fatalf("fired %d times, want 30 (3 replicas x 10 ticks)", f.n)
	}
	if claims.calls != 0 {
		t.Fatalf("PerReplica consulted the claims %d times", claims.calls)
	}
}

func TestNoClaimsFiresEverywhereWithABootWarning(t *testing.T) {
	var logs bytes.Buffer
	f := &fires{}
	replicas(t, 2, 3*time.Minute, 0,
		Options{Name: "poll", Schedule: "* * * * *", Logger: slog.New(slog.NewTextHandler(&logs, nil))}, f.job)

	if f.n != 6 {
		t.Fatalf("fired %d times, want 6 (the pre-claims behaviour: every replica)", f.n)
	}
	if got := strings.Count(logs.String(), "level=WARN msg=\"cron worker started WITHOUT tick claims"); got != 2 {
		t.Fatalf("want one boot warning per replica, got %d:\n%s", got, logs.String())
	}
}

func TestAClaimErrorSkipsTheTickOnThatReplica(t *testing.T) {
	var logs bytes.Buffer
	f := &fires{}
	replicas(t, 1, 3*time.Minute, 0,
		Options{Name: "poll", Schedule: "* * * * *", Claims: errClaims{}, Logger: slog.New(slog.NewTextHandler(&logs, nil))}, f.job)

	if f.n != 0 {
		t.Fatalf("fired %d times with no claim taken, want 0 (at most once: never run unclaimed)", f.n)
	}
	if !strings.Contains(logs.String(), "level=ERROR msg=\"cron tick claim failed") {
		t.Fatalf("claim failure not logged at ERROR:\n%s", logs.String())
	}
}

func TestAFailingOrPanickingTickKeepsTheSchedule(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	job := func(context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		calls++
		switch calls {
		case 1:
			panic("boom")
		case 2:
			return errors.New("upstream 503")
		}
		return nil
	}
	replicas(t, 1, 4*time.Minute, 0, Options{Name: "poll", Schedule: "* * * * *", Logger: quiet}, job)
	if calls != 4 {
		t.Fatalf("ran %d ticks, want 4: a panic or an error must not stop the schedule", calls)
	}
}

func TestSkippedTicksAreCountedByOutcome(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	meter := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test")
	replicas(t, 2, 5*time.Minute, 0,
		Options{Name: "poll", Schedule: "* * * * *", Claims: &memClaims{}, Logger: quiet, Meter: meter}, (&fires{}).job)

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "forge_cron_ticks_total" {
				continue
			}
			for _, dp := range m.Data.(metricdata.Sum[int64]).DataPoints {
				outcome, _ := dp.Attributes.Value("outcome")
				got[outcome.AsString()] += dp.Value
			}
		}
	}
	if got[OutcomeRan] != 5 || got[OutcomeSkipped] != 5 {
		t.Fatalf("forge_cron_ticks_total by outcome = %v, want ran=5 skipped=5", got)
	}
}

func TestEveryIsAlignedSoReplicasShareTicks(t *testing.T) {
	s, err := Parse("@every 5m")
	if err != nil {
		t.Fatal(err)
	}
	a := s.Next(time.Date(2026, 11, 4, 10, 1, 7, 0, time.UTC))
	b := s.Next(time.Date(2026, 11, 4, 10, 4, 59, 0, time.UTC))
	want := time.Date(2026, 11, 4, 10, 5, 0, 0, time.UTC)
	if !a.Equal(want) || !b.Equal(want) {
		t.Fatalf("@every 5m from two start times = %s, %s; want both %s", a, b, want)
	}
}

func TestStandardScheduleNext(t *testing.T) {
	s, err := Parse("*/15 * * * *")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := s.Next(start), time.Date(2026, 11, 4, 10, 15, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("Next = %s, want %s", got, want)
	}
}

func TestRunRejectsWhatItCannotRun(t *testing.T) {
	ctx := context.Background()
	if err := Run(ctx, Options{Schedule: "* * * * *"}, nil); err == nil {
		t.Error("an unnamed job ran: the name is the claim key")
	}
	if err := Run(ctx, Options{Name: "x", Schedule: "every tuesday"}, nil); err == nil {
		t.Error("an unparseable schedule ran")
	}
}

func TestRunWaitsForTheInFlightTickOnShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started, finished := make(chan struct{}), make(chan struct{})
	clk := &stepClock{now: start, end: start.Add(time.Hour), cancel: func() {}}
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, Options{Name: "slow", Schedule: "* * * * *", Logger: quiet, clock: clk}, func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			close(finished)
			return ctx.Err()
		})
	}()
	<-started
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	default:
		t.Fatal("Run returned before the in-flight tick did")
	}
}

func TestPostgresOfNothingIsNoClaims(t *testing.T) {
	var typedNil *nilDB
	for name, db := range map[string]DB{"nil": nil, "typed nil": typedNil, "nil *sql.DB": SQLDB(nil)} {
		if c := Postgres(db); c != nil {
			t.Errorf("Postgres(%s) = %v, want nil so Run falls back with its warning", name, c)
		}
	}
}

type nilDB struct{}

func (*nilDB) Exec(context.Context, string, ...any) (sql.Result, error) { return nil, nil }
