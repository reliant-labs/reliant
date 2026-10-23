package serverkit

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"syscall"
	"time"
)

// dbconnect.go — startup dependency connect with bounded retry.
//
// A deploy that brings a dependency up (a PgBouncer, a Cloud SQL proxy, a
// freshly failed-over primary) at the same moment as its clients is routine,
// and the dependency is usually reachable within seconds. A boot that treats
// the first failed ping as fatal turns that routine ordering into CrashLoop
// restarts: the pod exits non-zero, the kubelet backs off, and an operator
// sees "restarted 3 times" for what was a 40-second wait.
//
// So connecting retries, with capped exponential backoff and jitter, for a
// bounded total (Config.DBConnectTimeout, DefaultDBConnectTimeout). The
// process stays up while it waits — StartBootProbes serves /healthz (200)
// and /readyz (503 naming the dependency) from the first moment — and only a
// dependency still unreachable at the deadline fails the process, with an
// error that says so.

// DefaultDBConnectTimeout is the total budget for connecting to the database
// at startup when Config.DBConnectTimeout is unset. It is deliberately
// generous (five minutes): the wait costs nothing — the process is alive and
// reports not-ready — while giving up early reintroduces the crash loop this
// exists to remove. It stays inside a Deployment's default 600s progress
// deadline, so a dependency that never comes up still fails the rollout.
const DefaultDBConnectTimeout = 5 * time.Minute

const (
	defaultConnectAttemptTimeout = 5 * time.Second
	defaultConnectInitialBackoff = 500 * time.Millisecond
	defaultConnectMaxBackoff     = 10 * time.Second
	defaultConnectLogEvery       = 15 * time.Second
	defaultConnectName           = "database"
)

// ConnectPolicy tunes RetryConnect / OpenDB. Every zero field takes its
// default, so ConnectPolicy{} is a complete, production-shaped policy.
type ConnectPolicy struct {
	// Name identifies the dependency in logs and in the boot /readyz body.
	// Default "database".
	Name string
	// Timeout is the TOTAL budget across all attempts. Default
	// DefaultDBConnectTimeout.
	Timeout time.Duration
	// AttemptTimeout bounds ONE attempt, so a blackholed host (a dropped
	// SYN never errors) costs one attempt's budget rather than the whole
	// total. Default 5s.
	AttemptTimeout time.Duration
	// InitialBackoff is the delay after the first failure; it doubles up to
	// MaxBackoff, with jitter. Defaults 500ms / 10s.
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	// LogEvery is the width of a log bucket: one WARN per bucket (the first
	// failure always logs), summarising the attempts inside it. Default 15s.
	LogEvery time.Duration
	// Logger receives the WARN/INFO lines. Nil uses slog.Default().
	Logger *slog.Logger
}

func (p ConnectPolicy) withDefaults() ConnectPolicy {
	if p.Name == "" {
		p.Name = defaultConnectName
	}
	if p.Timeout <= 0 {
		p.Timeout = DefaultDBConnectTimeout
	}
	if p.AttemptTimeout <= 0 {
		p.AttemptTimeout = defaultConnectAttemptTimeout
	}
	if p.InitialBackoff <= 0 {
		p.InitialBackoff = defaultConnectInitialBackoff
	}
	if p.MaxBackoff <= 0 {
		p.MaxBackoff = defaultConnectMaxBackoff
	}
	if p.MaxBackoff < p.InitialBackoff {
		p.MaxBackoff = p.InitialBackoff
	}
	if p.LogEvery <= 0 {
		p.LogEvery = defaultConnectLogEvery
	}
	if p.Logger == nil {
		p.Logger = slog.Default()
	}
	return p
}

// ConnectPolicy returns the policy a Config describes: its DBConnectTimeout
// and the given logger. A project passes this to OpenDB.
func (c Config) ConnectPolicy(logger *slog.Logger) ConnectPolicy {
	return ConnectPolicy{Timeout: c.DBConnectTimeout, Logger: logger}
}

// ConnectTimeoutError is returned when the dependency was still unreachable
// at the deadline. It wraps the last attempt's error.
type ConnectTimeoutError struct {
	Name     string
	Elapsed  time.Duration
	Attempts int
	Class    string
	Last     error
}

func (e *ConnectTimeoutError) Error() string {
	return fmt.Sprintf("%s not reachable after %s (%d attempts, last error class %q): %v",
		e.Name, e.Elapsed.Round(time.Second), e.Attempts, e.Class, e.Last)
}

func (e *ConnectTimeoutError) Unwrap() error { return e.Last }

// RetryConnect calls attempt until it succeeds, the policy's total Timeout
// elapses (a *ConnectTimeoutError), or ctx is cancelled (ctx.Err() wrapped).
// attempt receives a context already bounded by AttemptTimeout.
//
// While ctx carries a *BootProbes (StartBootProbes), the dependency is
// listed as pending in the boot /readyz body for the duration of the wait.
func RetryConnect(ctx context.Context, p ConnectPolicy, attempt func(ctx context.Context) error) error {
	p = p.withDefaults()
	boot := BootProbesFrom(ctx)
	boot.pend(p.Name)

	start := time.Now()
	deadline := start.Add(p.Timeout)
	var (
		attempts      int
		bucketAttempt int
		lastWarn      time.Time
		lastErr       error
		backoff       = p.InitialBackoff
	)
	for {
		attempts++
		bucketAttempt++
		actx, cancel := context.WithDeadline(ctx, minTime(deadline, time.Now().Add(p.AttemptTimeout)))
		err := attempt(actx)
		cancel()
		if err == nil {
			boot.clear(p.Name)
			p.Logger.InfoContext(ctx, p.Name+" connected",
				"dependency", p.Name, "elapsed", time.Since(start).Round(time.Millisecond), "attempts", attempts)
			return nil
		}
		// An attempt cut short by the TOTAL deadline fails with a bare
		// "context deadline exceeded" that says nothing about why the
		// dependency was unreachable; keep the last real error for the
		// exit message instead.
		if lastErr == nil || !(errors.Is(err, context.DeadlineExceeded) && !time.Now().Before(deadline)) {
			lastErr = err
		}
		if cerr := ctx.Err(); cerr != nil {
			return fmt.Errorf("%s connect cancelled after %d attempts: %w (last error: %v)", p.Name, attempts, cerr, lastErr)
		}

		if lastWarn.IsZero() || time.Since(lastWarn) >= p.LogEvery {
			p.Logger.WarnContext(ctx, p.Name+" not reachable yet — retrying",
				"dependency", p.Name,
				"error_class", classifyConnectError(lastErr),
				"error", lastErr,
				"attempts_total", attempts,
				"attempts_since_last_log", bucketAttempt,
				"elapsed", time.Since(start).Round(time.Second),
				"gives_up_in", time.Until(deadline).Round(time.Second))
			lastWarn = time.Now()
			bucketAttempt = 0
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		delay := jitter(backoff)
		if delay > remaining {
			delay = remaining
		}
		backoff = min(backoff*2, p.MaxBackoff)
		t := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			t.Stop()
			return fmt.Errorf("%s connect cancelled after %d attempts: %w (last error: %v)", p.Name, attempts, ctx.Err(), lastErr)
		case <-t.C:
		}
	}
	return &ConnectTimeoutError{
		Name: p.Name, Elapsed: time.Since(start), Attempts: attempts,
		Class: classifyConnectError(lastErr), Last: lastErr,
	}
}

// OpenDB opens a *sql.DB, applies the pool tuning, and verifies connectivity
// with RetryConnect. The pool is closed on failure, so a database that never
// comes up does not leak a half-open pool into the exit path.
//
// The "pgx" driver (the default) is opened so that the pool rides through a
// pooler's rollout: a statement a draining PgBouncer refused is run again on
// another connection, and closing a connection the pooler already dropped is
// not reported as an error. See pooler_drain.go.
func OpenDB(ctx context.Context, driver, dsn string, pool DBPoolTuning, p ConnectPolicy) (*sql.DB, error) {
	if driver == "" {
		driver = "pgx"
	}
	db, err := openPool(driver, dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", driver, err)
	}
	ApplyDBPoolTuning(db, pool)
	if err := RetryConnect(ctx, p, db.PingContext); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// classifyConnectError names the failure for a log line, so "the dependency
// is down" and "the credentials are wrong" are distinguishable at a glance.
func classifyConnectError(err error) string {
	var (
		dnsErr   *net.DNSError
		netErr   net.Error
		sqlState interface{ SQLState() string }
	)
	switch {
	case err == nil:
		return "none"
	case errors.As(err, &sqlState):
		switch code := sqlState.SQLState(); {
		case len(code) >= 2 && code[:2] == "28":
			return "auth"
		case code == "57P03":
			return "db_starting_up"
		case len(code) >= 2 && code[:2] == "53":
			return "db_resources"
		default:
			return "db_error_" + code
		}
	case errors.As(err, &dnsErr):
		return "dns"
	case errors.Is(err, syscall.ECONNREFUSED):
		return "connection_refused"
	case errors.Is(err, syscall.ECONNRESET):
		return "connection_reset"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.As(err, &netErr) && netErr.Timeout():
		return "timeout"
	default:
		return "other"
	}
}

// jitter returns a random delay in [d/2, d]: enough spread that a fleet of
// replicas restarted together does not retry in lockstep.
func jitter(d time.Duration) time.Duration {
	if d <= 1 {
		return d
	}
	half := d / 2
	return half + time.Duration(rand.Int64N(int64(d-half)+1))
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
