// Package cronkit runs a cron-scheduled job so that each tick fires on
// exactly ONE replica, however many replicas run the worker.
//
// # Why a tick claim, not a leader
//
// A cron worker runs inside a long-running workload, so every replica of that
// workload computes the same schedule and wakes at the same instant. Without
// coordination a poller scaled to three replicas polls three times an hour.
// A leader lease (forge/pkg/lease, lifecyclekit.Singleton) would fix that by
// idling every replica but one; a tick claim fixes it without a standby: each
// tick is a row keyed by (job, scheduled tick time) in the app's Postgres, the
// replica whose INSERT lands runs the tick, and every other replica's INSERT
// is a no-op, so it skips the tick (a debug log and a "skipped" count).
//
// The key is the SCHEDULED time, not the moment a replica woke, so replicas
// whose timers fire a few hundred milliseconds apart (or whose clocks are a
// little skewed) still contend for the same row. An `@every <d>` schedule is
// aligned to multiples of d for the same reason: robfig/cron counts it from
// the moment each process started, which no two replicas share.
//
// # What a claim guarantees
//
// At most once per tick. The claim is taken before the job runs, so a replica
// that crashes mid-tick loses that tick (cron semantics: a missed tick is not
// caught up). A claim that cannot be taken (the database is down) skips the
// tick on that replica rather than risking a double run, and logs at ERROR.
// Ticks on one replica never overlap: a tick that outlives its period skips
// the ticks it covered. A LATER tick may run on another replica while a slow
// one is still running here; a job that must never overlap itself as well
// should hold a forge/pkg/lease lease inside its job.
//
// # Running everywhere on purpose
//
// [Options.PerReplica] fires every tick on every replica and never consults
// the claims: for a job whose effect is local to the pod (refreshing an
// in-memory cache, sweeping a local temp dir). With no [Options.Claims] (the
// app has no database) every replica fires too, and Run says so at boot with
// a WARN, because that is a fallback, not a choice.
package cronkit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/robfig/cron/v3"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/reliant-labs/forge/pkg/observe"
)

// DefaultClaimTimeout bounds one claim. A claim that times out skips the tick.
const DefaultClaimTimeout = 10 * time.Second

// The values of the outcome attribute on forge_cron_ticks_total.
const (
	OutcomeRan        = "ran"         // this replica ran the tick and it returned nil
	OutcomeFailed     = "failed"      // this replica ran the tick and it returned an error or panicked
	OutcomeSkipped    = "skipped"     // another replica claimed the tick
	OutcomeClaimError = "claim_error" // the claim could not be taken; the tick was skipped here
)

// Claims decides which replica runs a tick. [Postgres] is the implementation
// a forge app uses.
type Claims interface {
	// Claim reports whether the caller won tick of job. Across every
	// replica sharing the store, exactly one call per (job, tick) returns
	// true.
	Claim(ctx context.Context, job string, tick time.Time) (bool, error)
}

// Options configures [Run].
type Options struct {
	// Name is the job's name: the claim key and the worker label on logs,
	// spans and metrics. Required. A cron worker passes its Name().
	Name string
	// Schedule is a standard 5-field cron expression or a descriptor
	// (@hourly, @every 5m), as robfig/cron's ParseStandard reads it.
	// Required.
	Schedule string
	// Claims makes each tick fire on one replica. Nil fires every tick on
	// every replica, with a WARN at start (unless PerReplica says that is
	// intended).
	Claims Claims
	// PerReplica fires every tick on every replica and never consults
	// Claims: the opt-out for a job meant to run on each pod.
	PerReplica bool
	// ClaimTimeout bounds one claim (default [DefaultClaimTimeout]).
	ClaimTimeout time.Duration
	// Logger receives the start line, skipped ticks (DEBUG), failed ticks
	// and claim errors. nil uses slog.Default().
	Logger *slog.Logger
	// Meter records forge_cron_ticks_total. nil uses the global meter
	// provider.
	Meter metric.Meter

	// clock is the test seam for time; nil is the wall clock.
	clock clock
}

// clock is time, for tests.
type clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type wallClock struct{}

func (wallClock) Now() time.Time                         { return time.Now() }
func (wallClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

type tickKey struct{}

// TickTime returns the scheduled time of the tick ctx belongs to: the same
// value on every replica, so a job can key an idempotent write on it.
func TickTime(ctx context.Context) (time.Time, bool) {
	t, ok := ctx.Value(tickKey{}).(time.Time)
	return t, ok
}

// Parse reads a schedule the way [Run] does, so a caller can validate one up
// front. An `@every` schedule is aligned to multiples of its interval.
func Parse(schedule string) (cron.Schedule, error) {
	s, err := cron.ParseStandard(schedule)
	if err != nil {
		return nil, err
	}
	if every, ok := s.(cron.ConstantDelaySchedule); ok {
		return alignedEvery{every.Delay}, nil
	}
	return s, nil
}

// alignedEvery is `@every d` on multiples of d, so every replica computes the
// same ticks whenever it started.
type alignedEvery struct{ d time.Duration }

func (a alignedEvery) Next(t time.Time) time.Time { return t.Truncate(a.d).Add(a.d) }

// Run calls fn at every tick of opts.Schedule until ctx is cancelled, then
// waits for an in-flight tick to return (its ctx is cancelled too) and
// returns nil. It returns an error only for options it cannot run.
//
// Each tick that this replica runs is a trace of its own (a root span named
// "cron.<name>.tick"); an error fn returns is logged and recorded on the
// span, and a panic is recovered, so one bad tick never stops the schedule.
func Run(ctx context.Context, opts Options, fn func(ctx context.Context) error) error {
	if opts.Name == "" {
		return errors.New("cronkit.Run: Options.Name is required")
	}
	sched, err := Parse(opts.Schedule)
	if err != nil {
		return fmt.Errorf("cronkit.Run(%s): schedule %q: %w", opts.Name, opts.Schedule, err)
	}
	r := newRunner(opts, sched)
	r.announce(ctx)

	var last time.Time
	for {
		now := r.clock.Now()
		from := now
		if from.Before(last) {
			from = last // a clock stepped back never re-fires a tick
		}
		next := sched.Next(from)
		if !last.IsZero() {
			if missed := sched.Next(last); missed.Before(next) {
				r.log.WarnContext(ctx, "cron tick outran its schedule; the ticks it covered are skipped",
					"worker", opts.Name, "schedule", opts.Schedule, "first_skipped", missed, "next", next)
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-r.clock.After(next.Sub(now)):
		}
		r.tick(ctx, next, fn)
		last = next
	}
}

type runner struct {
	opts    Options
	sched   cron.Schedule
	clock   clock
	log     *slog.Logger
	ticks   metric.Int64Counter
	spanFor string
}

func newRunner(opts Options, sched cron.Schedule) *runner {
	if opts.ClaimTimeout <= 0 {
		opts.ClaimTimeout = DefaultClaimTimeout
	}
	r := &runner{opts: opts, sched: sched, clock: opts.clock, log: opts.Logger, spanFor: "cron." + opts.Name + ".tick"}
	if r.clock == nil {
		r.clock = wallClock{}
	}
	if r.log == nil {
		r.log = slog.Default()
	}
	meter := opts.Meter
	if meter == nil {
		meter = otel.Meter("github.com/reliant-labs/forge/pkg/cronkit")
	}
	ticks, err := meter.Int64Counter("forge_cron_ticks_total",
		metric.WithDescription("Cron ticks seen by this replica, by worker and outcome (ran, failed, skipped, claim_error)."))
	if err != nil {
		r.log.Warn("cronkit: tick counter unavailable", "worker", opts.Name, "error", err)
	}
	r.ticks = ticks
	return r
}

// announce is the start line, and the boot warning when ticks are not
// claimed although the worker did not ask to run everywhere.
func (r *runner) announce(ctx context.Context) {
	switch {
	case r.opts.PerReplica:
		r.log.InfoContext(ctx, "cron worker started; every tick fires on every replica (PerReplica)",
			"worker", r.opts.Name, "schedule", r.opts.Schedule)
	case r.opts.Claims == nil:
		r.log.WarnContext(ctx, "cron worker started WITHOUT tick claims: every replica fires every tick. "+
			"Give the worker a database (cronkit.Postgres) to fire each tick once, or set PerReplica to say this is intended",
			"worker", r.opts.Name, "schedule", r.opts.Schedule)
	default:
		r.log.InfoContext(ctx, "cron worker started; each tick fires on the one replica that claims it",
			"worker", r.opts.Name, "schedule", r.opts.Schedule)
	}
}

func (r *runner) count(ctx context.Context, outcome string) {
	if r.ticks == nil {
		return
	}
	r.ticks.Add(ctx, 1, metric.WithAttributes(attribute.String("worker", r.opts.Name), attribute.String("outcome", outcome)))
}

// tick claims (unless PerReplica or no claims) and runs one tick.
func (r *runner) tick(ctx context.Context, at time.Time, fn func(context.Context) error) {
	if r.opts.Claims != nil && !r.opts.PerReplica {
		cctx, cancel := context.WithTimeout(ctx, r.opts.ClaimTimeout)
		won, err := r.opts.Claims.Claim(cctx, r.opts.Name, at)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return // shutting down
			}
			r.log.ErrorContext(ctx, "cron tick claim failed; this replica skips the tick",
				"worker", r.opts.Name, "tick", at, "error", err)
			r.count(ctx, OutcomeClaimError)
			return
		}
		if !won {
			r.log.DebugContext(ctx, "cron tick claimed by another replica; skipping", "worker", r.opts.Name, "tick", at)
			r.count(ctx, OutcomeSkipped)
			return
		}
	}

	tctx := context.WithValue(ctx, tickKey{}, at)
	err := r.run(tctx, at, fn)
	if err != nil && ctx.Err() == nil {
		r.log.ErrorContext(ctx, "cron tick failed", "worker", r.opts.Name, "tick", at, "error", err)
	}
	if err != nil {
		r.count(ctx, OutcomeFailed)
	} else {
		r.count(ctx, OutcomeRan)
	}
}

// run calls fn under a root span, turning a panic into an error.
func (r *runner) run(ctx context.Context, at time.Time, fn func(context.Context) error) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()
	return observe.RunRoot(ctx, r.spanFor, fn,
		trace.WithAttributes(attribute.String("cron.worker", r.opts.Name), attribute.String("cron.tick", at.UTC().Format(time.RFC3339))))
}
