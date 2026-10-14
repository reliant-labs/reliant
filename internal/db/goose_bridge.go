// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pressly/goose/v3"

	"github.com/reliant-labs/reliant/internal/logging"
)

// THE GOOSE BRIDGE.
//
// forge's migrator adopts a database goose migrated only when goose applied
// EXACTLY the history the baseline replaces (db/migrations, the first
// migration's `-- forge:adopts goose` list). A database an older goose release
// migrated — a self-hosted install that skipped releases, a stack nobody
// started for a while — is BEHIND that history, and forge alone would refuse
// it: this binary carries no forge migration that finishes goose's history.
//
// It does carry goose's history itself, frozen (embed.go). So before forge's
// migrator runs, Migrate brings a database that is behind to goose's frozen
// head with goose — RunMigrations, the exact goose.Up the goose releases ran at
// boot, over the exact files they embedded — and the migrator then adopts it in
// the same run. To that database the upgrade looks like the next goose release
// followed by the cutover.
//
// The bridge acts only on a database forge has recorded nothing in, whose
// goose history is a SUBSET of the frozen one. Anything else is left to the
// migrator, or refused before goose runs (MigrationPlan.bridgeRefusal):
//
//   - a goose version the frozen history does not hold — a branch's migration
//     applied to a shared database, or a release newer than the cutover's
//     goose head that cannot exist — means the schema is not one goose's
//     history builds;
//   - a frozen version whose latest goose row says it was rolled back: goose
//     itself counts any row as applied and would never re-run it, so the
//     adoption that follows would refuse anyway, after goose had changed the
//     schema;
//   - reliant's tables with no goose or forge history at all.
//
// Each refusal changes nothing.
//
// CONCURRENCY. goose takes no lock: two processes running goose.Up at once
// collide in the catalog (see MigrationPolicy). Replicas booting together run
// the catch-up one at a time under gooseBridgeLockKey, and every one after the
// first re-reads the database under the lock and finds nothing to do. The lock
// is a session lock on the migration's one session, so goose — which takes a
// *sql.DB — runs on the very session that holds it (Migrate's pool has one
// connection). A process waiting for it polls pg_try_advisory_lock rather than
// blocking in pg_advisory_lock: a blocked lock statement holds a snapshot, and
// goose's history has CREATE INDEX CONCURRENTLY migrations, which wait for
// every older snapshot to end. Holder waiting on the waiter, waiter on the
// holder: postgres breaks that deadlock by cancelling the CREATE INDEX, which
// leaves an INVALID index behind. A poll holds no snapshot between attempts.

// migrationsDir is the path of goose's migrations within FS.
const migrationsDir = "migrations/postgres"

// gooseBridgeLockKey serializes the goose catch-up across processes.
// worktreesweep's sweepLockKey is "reliant\x01"; this is "reliant\x02".
const gooseBridgeLockKey int64 = 0x7265_6c69_616e_7402

// gooseBridgePoll is how often a process waiting for another's catch-up
// re-tries the lock.
const gooseBridgePoll = 500 * time.Millisecond

// initGoose points goose at the embedded migrations and selects the dialect.
func initGoose() error {
	goose.SetBaseFS(FS)

	if err := goose.SetDialect("postgres"); err != nil {
		logging.Error("Failed to set dialect", "error", err)
		return fmt.Errorf("failed to set dialect: %w", err)
	}

	return nil
}

// RunMigrations runs goose's migrations, exactly as every goose release did at
// boot. Since the cutover it runs only as the goose bridge's catch-up (see
// above), on a database that already has goose's history; never call it on a
// fresh database, which the baseline builds.
func RunMigrations(db *sql.DB, driver ...DatabaseDriver) error {
	if err := initGoose(); err != nil {
		return err
	}

	if err := goose.Up(db, migrationsDir, goose.WithAllowMissing()); err != nil {
		logging.Error("Failed to apply migrations", "error", err)
		return fmt.Errorf("failed to apply migrations: %w", err)
	}

	return nil
}

// catchUpGoose brings a database goose migrated to goose's frozen head, when it
// is behind and nothing refuses it, and returns the goose versions it applied.
// pool is Migrate's one-connection pool.
func catchUpGoose(ctx context.Context, pool *sql.DB, logger *slog.Logger) ([]int64, error) {
	plan, err := PlanMigration(ctx, pool)
	if err != nil {
		return nil, err
	}
	if err := plan.bridgeRefusal(); err != nil {
		return nil, err
	}
	if !plan.needsGooseCatchUp() {
		return nil, nil
	}

	unlock, err := lockGooseBridge(ctx, pool, logger)
	if err != nil {
		return nil, err
	}

	applied, err := catchUpGooseLocked(ctx, pool, logger)
	if unlockErr := unlock(); unlockErr != nil && err == nil {
		err = unlockErr
	}
	return applied, err
}

// catchUpGooseLocked is catchUpGoose's work under the bridge lock. It reads the
// database again first: the process that held the lock before this one may
// have caught it up, and adopted it, already.
func catchUpGooseLocked(ctx context.Context, pool *sql.DB, logger *slog.Logger) ([]int64, error) {
	plan, err := PlanMigration(ctx, pool)
	if err != nil {
		return nil, err
	}
	if err := plan.bridgeRefusal(); err != nil {
		return nil, err
	}
	if !plan.needsGooseCatchUp() {
		return nil, nil
	}

	missing := plan.Goose.Missing
	head := plan.Goose.Declared[len(plan.Goose.Declared)-1]
	logger.Warn("database is BEHIND goose's frozen history: running goose to its head before forge's migrator adopts it",
		"goose_missing", len(missing), "first", missing[0], "last", missing[len(missing)-1], "goose_head", head)
	if err := RunMigrations(pool); err != nil {
		return nil, fmt.Errorf("goose catch-up (%d migration(s) behind goose's frozen head %d): %w", len(missing), head, err)
	}

	after, err := PlanMigration(ctx, pool)
	if err != nil {
		return nil, fmt.Errorf("verify the goose catch-up: %w", err)
	}
	if err := after.bridgeRefusal(); err != nil {
		return nil, fmt.Errorf("verify the goose catch-up: %w", err)
	}
	if after.needsGooseCatchUp() {
		return nil, fmt.Errorf("goose reported success, but goose version(s) %s are still not applied", formatVersions(after.Goose.Missing))
	}
	logger.Warn("goose catch-up complete: the database is at goose's frozen head", "goose_applied", len(missing), "goose_head", head)
	return missing, nil
}

// lockGooseBridge takes gooseBridgeLockKey on pool's one session, polling
// until it is free (see CONCURRENCY above), and returns the unlock.
//
// The unlock also proves the catch-up ran on the session that held the lock:
// pg_advisory_unlock reports false on a session that does not hold it, which
// is what a pool that silently replaced a dropped connection would hand it.
func lockGooseBridge(ctx context.Context, pool *sql.DB, logger *slog.Logger) (func() error, error) {
	start := time.Now()
	var lastLog time.Time
	for {
		var got bool
		if err := pool.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, gooseBridgeLockKey).Scan(&got); err != nil {
			return nil, fmt.Errorf("take the goose catch-up lock: %w", err)
		}
		if got {
			break
		}
		if time.Since(lastLog) >= 10*time.Second {
			logger.Info("Waiting for another process's goose catch-up to finish", "waited", time.Since(start).Round(time.Second).String())
			lastLog = time.Now()
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for the goose catch-up lock: %w", ctx.Err())
		case <-time.After(gooseBridgePoll):
		}
	}

	return func() error {
		var held bool
		if err := pool.QueryRowContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, gooseBridgeLockKey).Scan(&held); err != nil {
			return fmt.Errorf("release the goose catch-up lock: %w", err)
		}
		if !held {
			return fmt.Errorf("the goose catch-up lost its database session while it ran (the connection dropped and was replaced), " +
				"so part of it may have run without the catch-up lock; run `reliant db migrate status`, then migrate again")
		}
		return nil
	}, nil
}

// GooseHistory is a database's goose history set against the frozen one the
// baseline adopts.
type GooseHistory struct {
	// Declared is every goose version the baseline adopts: goose's frozen
	// history, ascending.
	Declared []int64
	// Applied is how many goose versions the database applied.
	Applied int
	// Missing is every Declared version the database has not applied: what
	// the goose catch-up applies.
	Missing []int64
	// Unexpected is every version the database applied that Declared does
	// not hold. Refused.
	Unexpected []int64
	// RolledBack is every Declared version whose latest goose row records a
	// rollback (is_applied false). Refused: see the bridge's doc comment.
	RolledBack []int64
}

// readGooseHistory reads goose_db_version the way forge's migrator does when
// it adopts — the LATEST row for a version decides, and version 0 is the row
// goose writes when it creates its table — and sets it against declared.
// found is false when the database has no goose_db_version.
func readGooseHistory(ctx context.Context, q querier, declared []int64) (*GooseHistory, bool, error) {
	var found bool
	if err := q.QueryRowContext(ctx, `SELECT to_regclass('goose_db_version') IS NOT NULL`).Scan(&found); err != nil {
		return nil, false, fmt.Errorf("look for goose_db_version: %w", err)
	}
	if !found {
		return nil, false, nil
	}

	rows, err := q.QueryContext(ctx, `SELECT version_id, is_applied FROM goose_db_version ORDER BY id`)
	if err != nil {
		return nil, true, fmt.Errorf("read goose_db_version: %w", err)
	}
	defer func() { _ = rows.Close() }()
	latest := map[int64]bool{}
	for rows.Next() {
		var (
			v       int64
			applied bool
		)
		if err := rows.Scan(&v, &applied); err != nil {
			return nil, true, fmt.Errorf("read goose_db_version: %w", err)
		}
		latest[v] = applied
	}
	if err := rows.Err(); err != nil {
		return nil, true, fmt.Errorf("read goose_db_version: %w", err)
	}

	h := &GooseHistory{Declared: declared}
	for _, v := range declared {
		applied, recorded := latest[v]
		switch {
		case applied:
			h.Applied++
		case recorded:
			h.RolledBack = append(h.RolledBack, v)
			h.Missing = append(h.Missing, v)
		default:
			h.Missing = append(h.Missing, v)
		}
	}
	for v, applied := range latest {
		if applied && v != 0 && !slices.Contains(declared, v) {
			h.Unexpected = append(h.Unexpected, v)
			h.Applied++
		}
	}
	slices.Sort(h.Unexpected)
	return h, true, nil
}

// gooseFileVersions returns the version of every goose migration this binary
// embeds, ascending.
func gooseFileVersions() ([]int64, error) {
	entries, err := fs.ReadDir(FS, migrationsDir)
	if err != nil {
		return nil, fmt.Errorf("read the embedded goose history: %w", err)
	}
	var versions []int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		prefix, _, _ := strings.Cut(e.Name(), "_")
		v, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("goose migration %s: version %q is not a number", e.Name(), prefix)
		}
		versions = append(versions, v)
	}
	slices.Sort(versions)
	return versions, nil
}

// formatVersions renders versions for a message, eliding the middle of a long
// list.
func formatVersions(vs []int64) string {
	const edge = 3
	join := func(xs []int64) string {
		parts := make([]string, len(xs))
		for i, v := range xs {
			parts[i] = strconv.FormatInt(v, 10)
		}
		return strings.Join(parts, ", ")
	}
	if len(vs) <= 2*edge {
		return join(vs)
	}
	return fmt.Sprintf("%s, … %d more …, %s", join(vs[:edge]), len(vs)-2*edge, join(vs[len(vs)-edge:]))
}
