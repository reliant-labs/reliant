package pgtest

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EnvCreateTimeout names the env var that overrides DefaultCreateTimeout: a
// Go duration ("5m"). Zero or negative waits as long as the server takes.
const EnvCreateTimeout = "FORGE_TEST_CREATE_TIMEOUT"

// DefaultCreateTimeout bounds one CREATE DATABASE of a scratch database on
// the shared server — New, NewURL, NewClone and the clone NewFromTemplate
// makes — counted from the moment the statement waits for a maintenance
// connection.
//
// A healthy server answers in well under a second, and a few seconds on a
// machine running a dozen test fan-outs at once. A create that has not
// finished in minutes is stuck, and without a bound it waited for the go test
// timeout, which reported only a goroutine dump parked in a socket read. With
// one, it fails as a pgtest error naming the server, its data directory and
// what the statement's backend was waiting on.
//
// Two minutes leaves room for a heavily loaded machine and still fails well
// inside the default 10-minute go test timeout and a 5-minute one.
const DefaultCreateTimeout = 2 * time.Minute

// createTimeout resolves EnvCreateTimeout, falling back to
// DefaultCreateTimeout when it is unset or unparsable.
func createTimeout() time.Duration {
	if v := strings.TrimSpace(os.Getenv(EnvCreateTimeout)); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return DefaultCreateTimeout
}

// cloneStrategy is the STRATEGY clause a server's CREATE DATABASE … TEMPLATE
// takes: FILE_COPY on Postgres 15+ running with fsync off, which is what the
// embedded server runs; the server's default (WAL_LOG on 15+) everywhere
// else.
//
// WAL_LOG, the 15+ default, reads every block of the template through
// shared buffers and writes each one to WAL. A test suite's template holds
// hundreds of tables, and a fan-out clones it once per test from dozens of
// processes at once, so the clones queued on WAL insertion and on evicting
// each other's buffers: with 16 concurrent clones of a 200-table template on
// a loaded machine, a clone took 1.9s at the median and 3.9s at p95.
// FILE_COPY copies the template's files after a checkpoint; with fsync off
// the checkpoint writes only dirty buffers to the page cache and the copy is
// never synced, so the same clones took 0.4s and 0.8s, and the fan-out made
// about 2.5 times as many per second.
//
// With fsync on — an external FORGE_TEST_POSTGRES_URL server can be a real
// one — each FILE_COPY checkpoint syncs everything dirty on the server, which
// is the cost WAL_LOG exists to avoid, so such a server keeps its default.
func cloneStrategy(versionNum int, fsync string) string {
	if versionNum >= 150000 && strings.EqualFold(strings.TrimSpace(fsync), "off") {
		return "FILE_COPY"
	}
	return ""
}

// createDatabaseSQL is the CREATE DATABASE statement for a scratch database
// on s: empty, or a copy of template made with s's clone strategy.
func (s *server) createDatabaseSQL(name, template string) string {
	stmt := createDatabaseSQL(name, template)
	if template != "" && s.cloneStrategy != "" {
		stmt += " STRATEGY = " + s.cloneStrategy
	}
	return stmt
}

// execCreate runs stmt, a CREATE DATABASE, on s's maintenance connection,
// bounded by createTimeout. A statement still running at the deadline is
// cancelled and reported as a createTimeoutError carrying what its backend
// was waiting on, read just before the cancel.
func (s *server) execCreate(stmt string) error {
	limit := createTimeout()
	if limit <= 0 {
		_, err := s.baseDB.Exec(stmt)
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var (
		waiting string
		fired   = make(chan struct{})
	)
	timer := time.AfterFunc(limit, func() {
		defer close(fired)
		waiting = s.describeCreate(stmt)
		cancel()
	})
	_, err := s.baseDB.ExecContext(ctx, stmt)
	if timer.Stop() {
		return err
	}
	<-fired
	if err == nil {
		return nil // it finished while the backend was being read
	}
	return &createTimeoutError{stmt: stmt, limit: limit, where: s.where(), waiting: waiting, err: err}
}

// describeCreate reports what the backend running stmt is waiting on, read
// from pg_stat_activity over a connection of its own: the maintenance pool
// may be exhausted by the very creates that are stuck.
func (s *server) describeCreate(stmt string) string {
	db, err := open(s.baseURL)
	if err != nil {
		return "unknown (" + err.Error() + ")"
	}
	defer func() { _ = db.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var state, event string
	var secs float64
	err = db.QueryRowContext(ctx, `
		SELECT COALESCE(state, ''),
		       COALESCE(wait_event_type || '/' || wait_event, 'nothing (running)'),
		       EXTRACT(EPOCH FROM now() - query_start)
		FROM pg_stat_activity
		WHERE query = $1 AND pid <> pg_backend_pid()
		ORDER BY query_start
		LIMIT 1`, stmt).Scan(&state, &event, &secs)
	if err != nil {
		if strings.Contains(err.Error(), "no rows") {
			return "no backend was running it: it was still waiting for one of pgtest's maintenance connections, all busy with other creates and drops"
		}
		return "unknown (reading pg_stat_activity: " + err.Error() + ")"
	}
	return fmt.Sprintf("%s for %.0fs in state %q", event, secs, state)
}

// where names s for an error message: its address and, for the embedded
// server, its data directory.
func (s *server) where() string {
	u, err := url.Parse(s.baseURL)
	if err != nil {
		return "the shared postgres"
	}
	if strings.TrimSpace(os.Getenv(EnvBaseURL)) != "" {
		return fmt.Sprintf("the %s server at %s", EnvBaseURL, u.Host)
	}
	return fmt.Sprintf("the shared embedded postgres at %s (data dir %s)", u.Host, filepath.Join(instanceRoot(), u.Port(), "data"))
}

// createTimeoutError is a CREATE DATABASE that did not finish within
// createTimeout.
type createTimeoutError struct {
	stmt    string
	limit   time.Duration
	where   string
	waiting string
	err     error
}

func (e *createTimeoutError) Error() string {
	return fmt.Sprintf("%s did not finish within %s on %s; its backend was waiting on %s (set %s to wait longer): %v",
		e.stmt, e.limit, e.where, e.waiting, EnvCreateTimeout, e.err)
}

func (e *createTimeoutError) Unwrap() error { return e.err }
