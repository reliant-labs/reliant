// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/reliant-labs/forge/pkg/orm"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	core "github.com/reliant-labs/reliant/internal/db/core"
	postgresstore "github.com/reliant-labs/reliant/internal/db/postgres"
	pgdb "github.com/reliant-labs/reliant/internal/db/postgres/generated"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/observability"
	"github.com/reliant-labs/reliant/internal/store"
	"go.temporal.io/sdk/activity"
)

const (
	// Retry configuration.
	//
	// maxRetries is sized for the contention the product actually generates,
	// not for an idle database. Update sequences are allocated from ONE
	// counter row per user (update_stream_counters), so every concurrent chat
	// belonging to a user contends on that single row — and a user running
	// many chats at once is the normal case, not the edge case.
	//
	// Measured on the dev Postgres, 5 writes per writer, all on one counter:
	//
	//	writers   failure rate at maxRetries=3
	//	2         0%
	//	4         0%
	//	8         5%
	//	16        8.8%
	//
	// A failed allocation surfaces to the user as a lost update event, so this
	// was a real defect above ~8 concurrent chats and not merely a flaky test.
	// The same measurement shows the work always completes with a little more
	// headroom: at 32 writers every write succeeded within one additional
	// outer attempt. 6 retries (7 attempts) covers 32-way contention with
	// margin, and the ladder is capped by maxRetryDelay so the added budget
	// costs nothing when there is no conflict — a transaction that does not
	// conflict never sleeps at all.
	//
	// That budget was never enough on its own: under SERIALIZABLE every
	// commit on the counter row aborts each writer waiting behind it, so the
	// failure rate keeps climbing with fan-out whatever the budget is. User
	// update allocation has since moved off the serializable path
	// (CreateUserUpdate runs at READ COMMITTED), where a waiter takes the
	// next number instead of aborting. The budget still covers every other
	// SERIALIZABLE writer.
	maxRetries     = 6
	baseRetryDelay = 50 * time.Millisecond
	maxRetryDelay  = 1 * time.Second

	// Default timeout for database operations
	defaultDBTimeout = 10 * time.Second
)

// runAfterCommit defers callback until the transaction ctx carries commits.
// It is orm.AfterCommit: the callbacks belong to one transaction ATTEMPT, so
// a failed attempt's are discarded with it and only the attempt that commits
// runs its own — whichever layer opened the transaction.
func runAfterCommit(ctx context.Context, callback func()) error {
	if err := orm.AfterCommit(ctx, func(context.Context) { callback() }); err != nil {
		return errors.New("after-commit callback requires repository transaction")
	}
	return nil
}

// ============================================================================
// TRANSACTION HELPER TYPES
// ============================================================================

// txMetrics holds timing and retry data for transaction monitoring
type txMetrics struct {
	txStartTime  time.Time
	totalRetries int
}

// txResult represents the outcome of a transaction attempt
type txResult struct {
	committed      bool
	beginDuration  time.Duration
	execDuration   time.Duration
	commitDuration time.Duration
	err            error
}

// UserUpdateNotifier is called after a user update is persisted to the DB.
// Implementations broadcast the event to connected streams.
type UserUpdateNotifier func(update *UserUpdate)

// ChatUpdateNotifier is called after a chat update is persisted to the DB.
// Implementations broadcast the event to connected streams.
type ChatUpdateNotifier func(chatID string, seqNum int64, update ChatUpdate)

type Repo struct {
	DB              *WrappedDBTX
	driver          DatabaseDriver
	chats           core.ChatStore
	runs            core.RunStore
	agentMessages   core.AgentMessageStore
	settings        core.SettingStore
	accounts        *postgresstore.AccountStore
	workflows       core.WorkflowStore
	threads         core.ThreadStore
	contextWindows  core.ContextWindowStore
	workflowCatalog core.WorkflowCatalogStore
	triggers        core.TriggerStore
	inbox           core.InboxStore
	runEvents       core.RunEventStore

	// session is the DatabaseConfig.DirectURL pool, for work that needs a
	// Postgres session to itself. Nil means DB already has session
	// semantics; see sessionPool.
	session *sql.DB

	// Update notifiers — set via SetUpdateNotifiers to push events to
	// streaming hubs after DB writes. Nil = no notification (tests, CLI).
	onUserUpdate UserUpdateNotifier
	onChatUpdate ChatUpdateNotifier

	// retrySleep waits out RunTx's backoff between attempts. Nil (always, in
	// production) means time.Sleep. A test sets it to record the backoff
	// ladder instead of sleeping through it: an exhausted retry budget is
	// ~2.5s of sleep. Per Repo, so parallel tests cannot see each other's.
	retrySleep func(time.Duration)
}

// backoffBeforeRetry sleeps calculateBackoff(attempt) before RunTx retries.
func (r *Repo) backoffBeforeRetry(attempt int) {
	delay := calculateBackoff(attempt)
	if r.retrySleep != nil {
		r.retrySleep(delay)
		return
	}
	time.Sleep(delay)
}

// NewRepo creates a new Repo with the given database
func NewRepo(db *sql.DB) (*Repo, error) {
	return NewRepoWithDriver(db, DriverPostgres)
}

// NewRepoWithDriver builds the Repository and the forge ORM client over the
// one pool db, sharing one transaction: see WrappedDBTX. It pings db (the ORM
// client's constructor does).
func NewRepoWithDriver(db *sql.DB, driver DatabaseDriver) (*Repo, error) {
	client, err := orm.NewClientWithDB(db, "postgres")
	if err != nil {
		return nil, fmt.Errorf("orm client: %w", err)
	}
	q := &WrappedDBTX{db: db, orm: client}
	pgQueries := pgdb.New(q)

	return &Repo{
		DB:            q,
		driver:        driver,
		accounts:      postgresstore.NewProviderAccountStore(db),
		chats:         postgresstore.NewChatStore(pgQueries, q),
		runs:          postgresstore.NewRunStore(client),
		agentMessages: postgresstore.NewAgentMessageStore(pgQueries, q),
		settings: postgresstore.NewSettingStore(pgQueries, q, func(query string) string {
			return (&Repo{driver: DriverPostgres}).bindQuery(query)
		}),
		workflows:       postgresstore.NewWorkflowStore(client),
		threads:         postgresstore.NewThreadStore(pgQueries),
		contextWindows:  postgresstore.NewContextWindowStore(pgQueries),
		workflowCatalog: postgresstore.NewWorkflowCatalogStore(client),
		triggers:        postgresstore.NewTriggerStore(pgQueries),
		inbox:           postgresstore.NewInboxStore(pgQueries),
		runEvents:       postgresstore.NewRunEventStore(client),
	}, nil
}

// ORM is the forge ORM client over the Repository's pool — the orm.Context
// internal/store's generated delegates and _repo_ext.go queries take. It
// shares the Repository's transaction in both directions (WrappedDBTX), so a
// store call inside RunTx and a Repository call inside ORM().RunTx each join
// the transaction the other opened.
func (r *Repo) ORM() *orm.Client {
	return r.DB.orm
}

// SetUpdateNotifiers configures callbacks that are invoked after successful
// DB writes to user_updates and chat_updates. This bridges the DB layer to
// the streaming hub without introducing a direct dependency on the streaming package.
func (r *Repo) SetUpdateNotifiers(onUser UserUpdateNotifier, onChat ChatUpdateNotifier) {
	r.onUserUpdate = onUser
	r.onChatUpdate = onChat
}

// bindQuery rewrites generic '?' placeholders into Postgres-style positional
// placeholders when running with the Postgres driver.
func (r *Repo) bindQuery(query string) string {
	if r.driver != DriverPostgres {
		return query
	}

	var builder strings.Builder
	builder.Grow(len(query) + 16)
	argIndex := 1

	for _, ch := range query {
		if ch == '?' {
			builder.WriteByte('$')
			builder.WriteString(strconv.Itoa(argIndex))
			argIndex++
			continue
		}
		builder.WriteRune(ch)
	}

	return builder.String()
}

func NewRepoFromConfig(cfg DatabaseConfig) (*Repo, error) {
	db, err := ConnectWithConfig(cfg)
	if err != nil {
		return nil, err
	}

	repo, err := NewRepoWithDriver(db, cfg.Driver)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if cfg.DirectURL != "" {
		session, err := openSessionPool(cfg.DirectURL)
		if err != nil {
			_ = db.Close()
			return nil, err
		}
		repo.WithSessionPool(session)
	}
	return repo, nil
}

// WithSessionPool routes session-scoped work (see DatabaseConfig.DirectURL) to
// pool instead of the main one. The Repo owns pool from here on: Close closes
// it.
func (r *Repo) WithSessionPool(pool *sql.DB) *Repo {
	r.session = pool
	return r
}

// sessionPool is where a caller that needs a Postgres session to itself takes
// its connection: the DirectURL pool when one is configured, else the main pool,
// which then has session semantics already.
func (r *Repo) sessionPool() *sql.DB {
	if r.session != nil {
		return r.session
	}
	return r.DB.SQLDB()
}

// Close closes the repository's pools. A connection the database end had
// already dropped is not a close failure: the main pool's connections do not
// report one (serverkit.OpenDB), and the session pool keeps no idle
// connection to close.
func (r *Repo) Close() error {
	err := r.DB.Close()
	if r.session != nil {
		err = errors.Join(err, r.session.Close())
	}
	return err
}

func (r *Repo) Ping(ctx context.Context) error {
	return r.DB.Ping(ctx)
}

// isRetryableError checks if an error is retryable (e.g., transaction conflicts)
func isRetryableError(err error) bool {
	if err == nil {
		return false
	}

	// Prefer classifying by SQLSTATE code when the pgx error is still in the
	// chain. This is robust against message-text variations (e.g. a 40001 can
	// be "could not serialize access ..." or "canceling statement due to
	// conflict with recovery" depending on where it was raised).
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "40001", // serialization_failure
			"40P01", // deadlock_detected
			"40002", // transaction_integrity_constraint_violation
			"25P02", // in_failed_sql_transaction (aborted tx; fresh BEGIN on retry)
			"23505": // unique_violation (parallel writers; see comment below)
			return true
		}
	}

	errMsg := strings.ToLower(err.Error())

	// Fallback for errors flattened to strings (e.g. re-wrapped with
	// errors.New) where the pgconn error is no longer in the chain.
	if strings.Contains(errMsg, "sqlstate 40001") ||
		strings.Contains(errMsg, "sqlstate 40p01") {
		return true
	}

	// Generic transaction/concurrency errors
	if strings.Contains(errMsg, "concurrent update") ||
		strings.Contains(errMsg, "could not serialize") ||
		strings.Contains(errMsg, "deadlock") ||
		strings.Contains(errMsg, "transaction conflict") {
		return true
	}

	// Postgres: transaction already aborted by a prior failed statement (SQLSTATE 25P02).
	// Retrying the whole transaction from scratch will start a fresh BEGIN.
	if strings.Contains(errMsg, "25p02") ||
		strings.Contains(errMsg, "current transaction is aborted") {
		return true
	}

	// UNIQUE constraint violations during parallel operations
	// This handles the case where multiple parallel tool executions try to create
	// the same tool result message simultaneously. The constraint violation means
	// another transaction succeeded, so we should retry to find the created record.
	if strings.Contains(errMsg, "unique constraint") ||
		strings.Contains(errMsg, "constraint failed") {
		return true
	}

	return false
}

// calculateBackoff calculates the retry delay with exponential backoff and jitter
func calculateBackoff(attempt int) time.Duration {
	delay := baseRetryDelay * time.Duration(1<<uint(attempt))
	if delay > maxRetryDelay {
		delay = maxRetryDelay
	}

	// Add jitter (±25% of delay)
	jitter := time.Duration(rand.Int63n(int64(delay / 2)))
	return delay + jitter - delay/4
}

// GetPendingWrites returns 0 — write serialization has been removed (Postgres only).
func GetPendingWrites() int64 { return 0 }

// GetPeakPendingWrites returns 0 — write serialization has been removed (Postgres only).
func GetPeakPendingWrites() int64 { return 0 }

// ResetPeakPendingWrites is a no-op — write serialization has been removed (Postgres only).
func ResetPeakPendingWrites() {}

// ============================================================================
// TRANSACTION EXECUTION HELPERS
// ============================================================================

// executeTransaction executes a function within a transaction and handles commit/rollback.
// The returned txResult always has execDuration and commitDuration set; beginDuration
// should be set by the caller (attemptTransaction) since it tracks the BeginImmediate call.
func (r *Repo) executeTransaction(ctx context.Context, tx *orm.Tx, f func(ctx context.Context) error) txResult {
	var result txResult

	// Setup deferred rollback for panics and errors
	defer func() {
		if !result.committed {
			_ = tx.Rollback()
		}
	}()

	// Execute the transaction function under the ctx tx.RunTx hands it: a
	// forge transaction context, which this package's queries (WrappedDBTX)
	// and internal/store's both resolve to. After-commit callbacks
	// (runAfterCommit, orm.AfterCommit) belong to this attempt, so a
	// SERIALIZABLE retry cannot publish events created by the rolled-back
	// attempt.
	execTime := time.Now()
	err := tx.RunTx(ctx, f)
	result.execDuration = time.Since(execTime)

	if err != nil {
		result.err = err
		return result
	}

	// Commit, which runs the attempt's after-commit callbacks once it has.
	commitTime := time.Now()
	err = tx.Commit()
	result.commitDuration = time.Since(commitTime)

	if err != nil {
		observability.DBErrorsTotal.WithLabelValues("commit", string(r.driver)).Inc()
		result.err = err
		return result
	}

	result.committed = true
	return result
}

// logTransactionTiming logs slow or notable transaction timing
func logTransactionTiming(metrics *txMetrics, result *txResult) {
	totalDuration := time.Since(metrics.txStartTime)

	if totalDuration > 1*time.Second {
		logging.Warn("Slow transaction detected",
			"totalDuration", totalDuration,
			"begin", result.beginDuration,
			"exec", result.execDuration,
			"commit", result.commitDuration,
			"retries", metrics.totalRetries)
	} else if totalDuration > 100*time.Millisecond {
		logging.Debug("Transaction timing",
			"totalDuration", totalDuration,
			"begin", result.beginDuration,
			"exec", result.execDuration,
			"commit", result.commitDuration,
			"retries", metrics.totalRetries)
	}
}

// Isolation selects a transaction's isolation level.
//
// The zero value is IsolationDefault, which means SERIALIZABLE — deliberately,
// so that the DANGEROUS choice is always the explicit one. A zero-valued
// TxOptions therefore behaves exactly as every transaction did before this type
// existed, and forgetting to think about isolation cannot silently weaken a
// guarantee.
type Isolation int

const (
	// IsolationDefault is SERIALIZABLE. Correct for anything that writes.
	IsolationDefault Isolation = iota

	// IsolationSerializable is the explicit spelling of the default. Use it
	// when a transaction's serializability is load-bearing and you want that
	// stated at the call site rather than inherited.
	IsolationSerializable

	// IsolationReadCommitted weakens isolation to Postgres' own default.
	//
	// Only valid where the transaction's correctness does not depend on the
	// absence of concurrent modification: it can see rows committed by other
	// transactions BETWEEN its own statements, so two reads in one transaction
	// may disagree, and a read-then-write sequence has no protection against a
	// lost update.
	//
	// The gain is that it takes no predicate locks and cannot raise 40001. For
	// a single-statement lookup — where "between statements" has no meaning —
	// that is a strict win. For anything multi-statement it is a correctness
	// decision, not a performance tweak, and needs a reason at the call site.
	IsolationReadCommitted
)

// sqlLevel maps to database/sql's isolation constants.
func (i Isolation) sqlLevel() sql.IsolationLevel {
	if i == IsolationReadCommitted {
		return sql.LevelReadCommitted
	}
	return sql.LevelSerializable
}

func (i Isolation) String() string {
	switch i {
	case IsolationReadCommitted:
		return "read committed"
	default:
		return "serializable"
	}
}

// TxOptions configures a transaction. The zero value is the historical
// default: a read-write SERIALIZABLE transaction.
type TxOptions struct {
	// Isolation selects the isolation level. Zero value = SERIALIZABLE.
	Isolation Isolation

	// ReadOnly runs the transaction as READ ONLY DEFERRABLE.
	//
	// This is a scalability lever, not a safety annotation. Every transaction
	// here is SERIALIZABLE, where a read takes PREDICATE LOCKS over whatever it
	// touched — and a read that cannot use an index takes one over the whole
	// relation. Those locks are what make two transactions on completely
	// disjoint chats abort each other with SQLSTATE 40001.
	//
	// Postgres treats READ ONLY DEFERRABLE specially: such a transaction takes
	// NO predicate locks at all and can never be aborted with a serialization
	// failure. It waits at BEGIN until it can obtain a snapshot guaranteed free
	// of later conflicts, and from then on runs without contributing to — or
	// suffering from — the serialization graph.
	//
	// So a read-only path marked here stops both taking conflicts and CAUSING
	// them for concurrent writers. With one user running a dozen agents against
	// one chat, measured contention was 81 serialization failures in ~50
	// minutes (12 of which exhausted the retry budget and reached the user), so
	// removing read paths from the graph is the highest-leverage change
	// available short of a schema redesign.
	//
	// The cost is the DEFERRABLE wait: on a busy database BEGIN can block
	// briefly. That is the right trade for a read (it delays one query) and the
	// wrong one for anything that writes — Postgres rejects writes in a
	// read-only transaction outright, so a mislabelled path fails loudly rather
	// than silently corrupting.
	ReadOnly bool
}

func (r *Repo) RunTx(ctx context.Context, f func(ctx context.Context) error) error {
	return r.RunTxWithOptions(ctx, TxOptions{}, f)
}

// RunTxReadOnly runs f in a READ ONLY DEFERRABLE transaction. Sugar for the
// common case; see TxOptions.ReadOnly for why this matters.
func (r *Repo) RunTxReadOnly(ctx context.Context, f func(ctx context.Context) error) error {
	return r.RunTxWithOptions(ctx, TxOptions{ReadOnly: true}, f)
}

// RunTxWithOptions is RunTx with explicit transaction options.
func (r *Repo) RunTxWithOptions(ctx context.Context, opts TxOptions, f func(ctx context.Context) error) error {
	// Check if database is initialized
	if r.DB == nil {
		observability.DBErrorsTotal.WithLabelValues("transaction", string(r.driver)).Inc()
		return errors.New("database connection not initialized")
	}

	// Already inside a transaction: join it rather than opening a nested one.
	//
	// The caller's options are deliberately IGNORED here, because the ambient
	// transaction already exists and its mode cannot be changed mid-flight.
	// This is safe in the direction that matters — a read-only inner call
	// joining a read-write outer transaction just reads — and the opposite
	// (a writing inner call joining a read-only outer transaction) is rejected
	// by Postgres at the point of the write, which is exactly where the
	// mistake is.
	if r.DB.inTx(ctx) {
		return f(ctx)
	}

	// Initialize metrics
	metrics := &txMetrics{txStartTime: time.Now()}

	err := r.runTxWithRetries(ctx, opts, f, metrics)
	duration := time.Since(metrics.txStartTime).Seconds()
	observability.DBQueryDuration.WithLabelValues("transaction", string(r.driver)).Observe(duration)
	return err
}

// runTxWithRetries executes the transaction with retry logic
func (r *Repo) runTxWithRetries(ctx context.Context, opts TxOptions, f func(ctx context.Context) error, metrics *txMetrics) error {
	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		result, err := r.attemptTransaction(ctx, opts, f, metrics)
		if err != nil {
			// Begin transaction failed
			if isRetryableError(err) && attempt < maxRetries {
				metrics.totalRetries++
				logging.DebugContext(ctx, "Transaction begin failed, retrying",
					"attempt", attempt+1,
					"maxRetries", maxRetries,
					"error", err)
				r.backoffBeforeRetry(attempt)
				continue
			}
			logging.ErrorContext(ctx, "Failed to begin transaction", "error", err, "attempts", attempt+1)
			return err
		}

		if result.committed {
			logTransactionTiming(metrics, result)
			return nil
		}

		// Transaction failed - check if retryable
		if isRetryableError(result.err) && attempt < maxRetries {
			metrics.totalRetries++
			lastErr = result.err
			logging.DebugContext(ctx, "Transaction failed, retrying",
				"attempt", attempt+1,
				"maxRetries", maxRetries,
				"error", result.err)
			r.backoffBeforeRetry(attempt)
			continue
		}

		// Non-retryable business errors (e.g. sql.ErrNoRows from a lookup
		// inside the tx) are expected control flow for callers — log them
		// quietly so genuine transaction failures stand out.
		if errors.Is(result.err, sql.ErrNoRows) {
			logging.DebugContext(ctx, "Transaction returned business error (not retryable)",
				"error", result.err,
				"attempts", attempt+1,
				"duration", time.Since(metrics.txStartTime))
		} else {
			logging.ErrorContext(ctx, "Transaction failed",
				"error", result.err,
				"attempts", attempt+1,
				"duration", time.Since(metrics.txStartTime))
		}
		return result.err
	}

	// All retries exhausted
	logging.ErrorContext(ctx, "Transaction failed after all retries",
		"retries", metrics.totalRetries,
		"lastError", lastErr,
		"duration", time.Since(metrics.txStartTime))
	observability.DBErrorsTotal.WithLabelValues("transaction", string(r.driver)).Inc()

	if lastErr != nil {
		return errors.New("transaction failed after retries: " + lastErr.Error())
	}
	return errors.New("transaction failed after retries")
}

// RunTxNoRetry runs f in one transaction attempt and returns its error
// verbatim, with no retry ladder.
//
// This exists to MEASURE contention rather than to survive it. RunTx's retries
// convert a serialization conflict into either eventual success or a
// "transaction failed after retries" string, both of which hide the raw
// conflict rate — and the raw rate is the only honest way to compare two write
// designs. A benchmark or control test that wants to know "does this shape
// conflict at all" must not have the answer absorbed before it sees it.
//
// Not for production paths: a real writer wants the retries.
func (r *Repo) RunTxNoRetry(ctx context.Context, f func(ctx context.Context) error) error {
	if r.DB == nil {
		return errors.New("database connection not initialized")
	}
	if r.DB.inTx(ctx) {
		return f(ctx)
	}

	metrics := &txMetrics{txStartTime: time.Now()}
	result, err := r.attemptTransaction(ctx, TxOptions{}, f, metrics)
	if err != nil {
		return err
	}
	if result.committed {
		return nil
	}
	return result.err
}

// attemptTransaction tries to begin and execute a single transaction
// Returns (result, nil) if transaction was started, (nil, err) if begin failed
func (r *Repo) attemptTransaction(ctx context.Context, opts TxOptions, f func(ctx context.Context) error, metrics *txMetrics) (*txResult, error) {
	beginTime := time.Now()
	tx, err := r.DB.beginSharedTx(ctx, opts)
	if err != nil {
		return nil, err
	}

	result := r.executeTransaction(ctx, tx, f)
	result.beginDuration = time.Since(beginTime)
	return &result, nil
}

// BeginTxWithOptions starts a DETACHED transaction on the pool: one the
// caller holds and ends itself, never carried in a ctx, so nothing else joins
// it (the run-control and provider-key locks). RunTx's transactions are
// beginSharedTx's.
//
// It is SERIALIZABLE unless opts says otherwise, read-only when asked.
//
// database/sql's ReadOnly maps to Postgres READ ONLY. Combined with
// SERIALIZABLE it also implies DEFERRABLE behavior for the purpose that
// matters here: the transaction takes no predicate locks and cannot fail with
// a serialization error. See TxOptions.ReadOnly.
func (w *WrappedDBTX) BeginTxWithOptions(ctx context.Context, opts TxOptions) (*sql.Tx, error) {
	tx, err := w.db.BeginTx(ctx, &sql.TxOptions{
		Isolation: opts.Isolation.sqlLevel(),
		ReadOnly:  opts.ReadOnly,
	})
	if err != nil {
		return nil, err
	}

	// DEFERRABLE only has meaning for a SERIALIZABLE READ ONLY transaction —
	// it is what buys the "no predicate locks, cannot be aborted" guarantee.
	// It is a no-op under READ COMMITTED (which already takes none), so it is
	// applied only where it does something.
	if opts.ReadOnly && opts.Isolation.sqlLevel() == sql.LevelSerializable {
		// database/sql has no DEFERRABLE flag. Without it the transaction is
		// READ ONLY but still participates in the serialization graph — it can
		// still be aborted with 40001, which is the entire thing we are trying
		// to avoid. Set it explicitly on the open transaction.
		if _, err := tx.ExecContext(ctx, "SET TRANSACTION READ ONLY DEFERRABLE"); err != nil {
			tx.Rollback()
			return nil, fmt.Errorf("set read-only deferrable: %w", err)
		}
	}
	return tx, nil
}

// SQLDB exposes the underlying pool for stores that own their SQL rather than
// going through sqlc-generated queries (see internal/connectorgrant). It
// deliberately ignores any ambient transaction: callers of this are
// self-contained stores, not participants in a caller's transaction.
func (w *WrappedDBTX) SQLDB() *sql.DB {
	return w.db
}

func (w *WrappedDBTX) Close() error {
	return w.db.Close()
}

func (w *WrappedDBTX) Ping(ctx context.Context) error {
	return w.db.PingContext(ctx)
}

func (w *WrappedDBTX) ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	return w.execContextWithRetry(ctx, query, args...)
}

func (w *WrappedDBTX) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return w.DB(ctx).PrepareContext(ctx, query)
}

func (w *WrappedDBTX) QueryContext(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return w.queryContextWithRetry(ctx, query, args...)
}

func (w *WrappedDBTX) QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row {
	return w.DB(ctx).QueryRowContext(ctx, query, args...)
}

// execContextWithRetry executes a query with automatic retry on lock errors.
// Retries are skipped when inside a transaction — on Postgres, a failed statement
// poisons the entire transaction so retrying individual statements is futile.
// Transaction-level retries in RunTx handle recovery instead.
func (w *WrappedDBTX) execContextWithRetry(ctx context.Context, query string, args ...interface{}) (sql.Result, error) {
	// Skip statement-level retries when inside a transaction
	if w.inTx(ctx) {
		return w.DB(ctx).ExecContext(ctx, query, args...)
	}

	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		result, err := w.DB(ctx).ExecContext(ctx, query, args...)
		if err == nil {
			return result, nil
		}

		// Check if error is retryable and we have retries left
		if isRetryableError(err) && attempt < maxRetries {
			lastErr = err
			logging.DebugContext(ctx, "Database exec failed, retrying",
				"attempt", attempt+1,
				"maxRetries", maxRetries,
				"error", err)
			time.Sleep(calculateBackoff(attempt))
			continue
		}

		return nil, err
	}

	if lastErr != nil {
		return nil, errors.New("exec failed after retries: " + lastErr.Error())
	}
	return nil, errors.New("exec failed after retries")
}

// queryContextWithRetry executes a query with automatic retry on lock errors.
// Retries are skipped when inside a transaction — on Postgres, a failed statement
// poisons the entire transaction so retrying individual statements is futile.
// Transaction-level retries in RunTx handle recovery instead.
func (w *WrappedDBTX) queryContextWithRetry(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	// Skip statement-level retries when inside a transaction
	if w.inTx(ctx) {
		return w.DB(ctx).QueryContext(ctx, query, args...)
	}

	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		rows, err := w.DB(ctx).QueryContext(ctx, query, args...)
		if err == nil {
			return rows, nil
		}

		// Check if error is retryable and we have retries left
		if isRetryableError(err) && attempt < maxRetries {
			lastErr = err
			logging.DebugContext(ctx, "Database query failed, retrying",
				"attempt", attempt+1,
				"maxRetries", maxRetries,
				"error", err)
			time.Sleep(calculateBackoff(attempt))
			continue
		}

		return nil, err
	}

	if lastErr != nil {
		return nil, errors.New("query failed after retries: " + lastErr.Error())
	}
	return nil, errors.New("query failed after retries")
}

// ==================== Sequence-based Sync for WebSocket ====================

// GetLatestUpdateSequence returns the latest sequence number for a given chat
// This is used by polling clients to check if they're in sync
func (r *Repo) GetLatestUpdateSequence(ctx context.Context, chatID string) (int64, error) {
	return store.LatestChatUpdateSequence(ctx, r.ORM(), chatID)
}

// CreateChatUpdate creates a new chat update (for dual-write pattern)
// This operation is atomic - the sequence number generation and insert happen in a transaction
// to prevent race conditions when multiple parallel operations try to create updates.
func (r *Repo) CreateChatUpdate(ctx context.Context, chatID string, updateType reliantv1.ChatUpdateType, entityID string, data string) error {
	// The allocation and the insert share one transaction: the counter's row
	// lock is what keeps sequence order equal to commit order, and a rollback
	// must take both back (store.AllocateUpdateSequence).
	return r.RunTx(ctx, func(ctx context.Context) error {
		chatUpdate, err := store.AppendChatUpdate(ctx, r.ORM(), chatID, updateType, entityID, data)
		if err != nil {
			return err
		}
		if r.onChatUpdate != nil {
			return runAfterCommit(ctx, func() {
				r.onChatUpdate(chatID, chatUpdate.SequenceNumber, chatUpdate)
			})
		}
		return nil
	})
}

// GetLatestNonMessageUpdatesPerEntity returns, for each entity in a chat, only
// the newest non-message chat_update row.
//
// This replaces the pattern of reading GetUpdatesSince(chatID, 0, 10000) and
// discarding message updates in Go, which was both slow and WRONG:
//
//   - Wrong: the LIMIT applied to a sequence-ASCENDING scan of ALL update
//     types. Message updates dominate the table, so on a long-lived chat the
//     cap was consumed by rows the caller then threw away, and genuinely
//     needed non-message updates past the cap never reached the client. The
//     OLDEST rows evicted the NEWEST — the opposite of what a snapshot wants.
//     Because the snapshot's sequence high-water mark is computed separately,
//     those dropped updates were never backfilled by gap detection either.
//
//   - Slow: it read (and JSON-unmarshalled) every message update's payload,
//     and GetUpdatesSince enriches message updates with an N+1 per-row
//     GetMessage + ListContentBlocks fan-out — all of it wasted, since the
//     caller drops every message row.
//
// Filtering by type in SQL and keeping one row per entity makes the result
// complete by construction: there is no cap to evict anything.
//
// STREAM_FINALIZED is excluded as well. Those markers exist only to retire
// in-flight streaming placeholders ("any delta carrying this message_id is a
// stale tail"), and a fresh snapshot has no in-flight deltas to retire — the
// finalized messages are already present as persisted rows. They are pure
// weight on the initial load, and on a long chat they are thousands of rows.
func (r *Repo) GetLatestNonMessageUpdatesPerEntity(ctx context.Context, chatID string) ([]ChatUpdate, error) {
	// DISTINCT ON keeps the first row per identity under the ORDER BY, i.e.
	// the highest sequence_number — matching the "last write per entity wins"
	// dedup the Go caller used to do.
	//
	// (Tool-call updates used to be deduped here by JSON tool_call_id; they are
	// no longer read by this query at all — see below.)
	//
	// Question updates need the same treatment, for a user-visible reason. A
	// question writes TWO rows over its life — "pending" when the gate opens
	// and "resolved" when it is answered — and EntityIDForQuestion also embeds
	// a timestamp, so per-entity_id dedup keeps BOTH. The snapshot then replays
	// the stale "pending" row to a client that is opening the chat, and an
	// already-answered question renders until the "resolved" row is applied
	// behind it. That is the "answered ask briefly pops up on open" flash.
	// Collapsing to the latest status per question makes the pending row
	// unreachable rather than merely short-lived.
	//
	// Question ids are UUIDs, which contain '-', so split_part cannot pick them
	// out the way it does for tool calls. The timestamp suffix has no '-' of
	// its own, so stripping the final '-' segment yields "question-<uuid>" —
	// stable across both transitions, and distinct per question.
	//
	// Thread updates need it for a different reason: their entity_id is the
	// WORKFLOW id, not the thread id (emitThreadUpdate keys on input.WorkflowID).
	// An inline fork reuses its parent's workflow id, so one workflow id can own
	// several threads AND is shared with that workflow's workflow_status rows.
	// Under entity_id dedup those all land in one bucket and the newest wins:
	// completing a run wrote a workflow_status that EVICTED the fork's only
	// announcement, so the snapshot carried no origin for the thread,
	// InterleavedTimeline could not classify it, and every message on it was
	// dropped from the timeline. Measured on a real database: 3,196 announcements
	// across 180 chats. The bug hid behind the live stream, which carries origin
	// directly — the thread rendered fine while running and vanished on reload.
	//
	// The payload's `thread` is the identity actually being deduped, so key on
	// it. The 'thread:' prefix keeps that key in its own namespace: thread ids
	// and workflow ids are both bare UUIDs, and an inline fork's workflow id IS
	// the main thread's id, which would otherwise collide with the main thread's
	// own bucket.
	//
	// Tool calls are NOT read here at all. They were 25,660 of 27,279 rows on
	// the measured chat and dominated both the sort and the payload; the
	// snapshot now synthesizes the few it needs from the durable tool_calls
	// table (see snapshotToolCalls). Excluding them also lets this read use a
	// skip-scan: types 1, 4 and 19 are excluded by the partial predicate of
	// idx_chat_updates_snapshot_heads.
	//
	// Shape: a recursive skip-scan (one index probe per DISTINCT entity_id)
	// for every type that dedups by entity_id, plus a separate small read of
	// the types that re-key (THREAD, QUESTION). The split is load-bearing: a
	// pure per-entity_id skip-scan LOSES rows, because thread updates share
	// entity_id (the workflow id) with workflow_status rows, so the newest of
	// the two would evict the other (measured: 198 workflow_status rows
	// dropped). Re-keyed types are few (656 + 0 on the measured chat), so they
	// can afford the CASE-key DISTINCT ON; everything else is bounded by the
	// number of entities, not the number of updates. Measured on a 2.48M-row
	// table: ~3-4s before, ~100ms after, with identical results minus tool
	// calls.
	//
	// "Few" only holds if they can be FOUND without reading the rest:
	// idx_chat_updates_snapshot_rekeyed is a partial index over exactly the
	// re-keyed types. Without it the re-keyed read rechecked every row
	// idx_chat_updates_snapshot_heads holds for the chat (165,843 on chat
	// 8bb0a875, almost all NODE_EXECUTION) to keep 758: ~350ms of a ~475ms
	// query. With it, ~3ms.
	//
	// AGENT_MESSAGES_DRAINED is excluded too. It is a live-only signal: it
	// tells an open pending-queue strip to retire mailbox rows in the same
	// commit as the transcript messages they became. Every drain has its own
	// entity_id, so per-entity dedup kept the chat's ENTIRE drain history —
	// 624 rows / 90KB of a 1.72MB snapshot on chat 8bb0a875 — and a snapshot
	// has no strip to reconcile: the mailbox hook re-reads
	// ListQueuedAgentMessages when it mounts. Live and replay delivery still
	// carry it (a client that missed a drain while connected-but-lagging can
	// still be showing the rows it retires).
	//
	// The update types are LITERALS, not bind parameters, and that is
	// load-bearing. The planner can use a partial index only when it can PROVE
	// the query's predicate implies the index's, and it can prove that only
	// from values it sees at plan time. pgx prepares statements, and Postgres
	// switches a prepared statement to a generic plan after five executions —
	// one that cannot see a bound $2..$6. With parameters, that generic plan
	// lost idx_chat_updates_snapshot_heads and fell back to a per-entity scan
	// of the chat's whole history: measured >20s (cancelled) on the dev copy.
	// TestSnapshotHeadsQueryMatchesPartialIndex pins these literals to the
	// enum values and to both index predicates in the migrations.
	//
	// The statement and the literal-vs-parameter reasoning are
	// store.SnapshotHeadsSQL's; TestSnapshotHeadsQueryMatchesPartialIndex
	// pins its literals to the enum values and to both index predicates.
	return store.LatestNonMessageUpdatesPerEntity(ctx, r.ORM(), chatID)
}

// GetUpdatesSince returns all updates since a given sequence number
// This is used for polling clients to fetch new updates
// limit: maximum number of updates to return (defaults to 100 if <= 0)
func (r *Repo) GetUpdatesSince(ctx context.Context, chatID string, sinceSeq int64, limit int) ([]ChatUpdate, error) {
	// Default to 100 updates per batch if not specified or invalid
	if limit <= 0 {
		limit = 100
	}

	updates, err := store.ChatUpdatesSince(ctx, r.ORM(), chatID, sinceSeq, limit)
	if err != nil {
		return nil, err
	}
	for i := range updates {
		update := &updates[i]
		// Enrich message updates with content blocks
		if update.UpdateType == reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE {
			// Check if this is a minimal message update (not already enriched)
			var dataMap map[string]interface{}
			if err := json.Unmarshal(update.Data, &dataMap); err == nil {
				// Only enrich if content_blocks is missing
				if _, hasContentBlocks := dataMap["content_blocks"]; !hasContentBlocks {
					enrichedData, err := r.EnrichMessageUpdate(ctx, *update)
					if err != nil {
						// Log error but continue with original data
						logging.WarnContext(ctx, "Failed to enrich message update",
							"error", err,
							"messageID", update.EntityID,
							"sequence", update.SequenceNumber)
					} else {
						update.Data = enrichedData
					}
				}
			}
		}
	}
	return updates, nil
}

// EnrichMessageUpdate adds content blocks to a message update
// This ensures websocket clients receive complete MessageWithBlocks objects
func (r *Repo) EnrichMessageUpdate(ctx context.Context, update ChatUpdate) (json.RawMessage, error) {
	// Parse the existing update data
	var updateData map[string]interface{}
	if err := json.Unmarshal(update.Data, &updateData); err != nil {
		return nil, fmt.Errorf("failed to parse update data: %w", err)
	}

	// Get message ID from the update
	messageID, ok := updateData["id"].(string)
	if !ok {
		return nil, fmt.Errorf("message ID not found in update data")
	}

	// Fetch message details
	msg, err := r.GetMessage(ctx, messageID)
	if err != nil {
		return nil, fmt.Errorf("failed to get message: %w", err)
	}

	// Fetch content blocks
	blocks, err := r.ListContentBlocks(ctx, messageID)
	if err != nil {
		return nil, fmt.Errorf("failed to get content blocks: %w", err)
	}

	// Compute streaming state from blocks instead of using deprecated field
	blockValues := make([]MessageContentBlock, len(blocks))
	for i, block := range blocks {
		if block != nil {
			blockValues[i] = *block
		}
	}
	streamingState := ComputeStreamingState(blockValues)

	// Build content blocks array and extract attachments for response

	// First, collect all attachment IDs from image and file_reference blocks
	attachmentIDs := []string{}
	for _, block := range blocks {
		if (block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE || block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_FILE_REFERENCE) && block.Content != nil {
			attachmentIDs = append(attachmentIDs, *block.Content)
		}
	}

	// Fetch attachment metadata from database in bulk
	attachmentMap := make(map[string]*Attachment)
	if len(attachmentIDs) > 0 {
		attachmentsData, err := r.GetAttachmentsByIDs(ctx, attachmentIDs)
		if err != nil {
			logging.WarnContext(ctx, "Failed to fetch attachments for message update", "error", err, "messageID", messageID)
			// Continue anyway - we'll use placeholder data
		} else {
			for _, att := range attachmentsData {
				attachmentMap[att.ID] = att
			}
		}
	}

	attachments := []map[string]interface{}{}
	for _, block := range blocks {
		// Extract attachments from image and file_reference blocks
		if (block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE || block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_FILE_REFERENCE) && block.Content != nil {
			attachmentID := *block.Content

			// Try to get real attachment data from database
			if att, found := attachmentMap[attachmentID]; found {
				// Use real attachment metadata
				attachments = append(attachments, map[string]interface{}{
					"id":        att.ID,
					"filename":  att.Filename,
					"size":      att.Size,
					"mime_type": att.MimeType,
					"url":       fmt.Sprintf("/api/attachments/%s", att.ID),
				})
			} else {
				// Fallback when attachment metadata is unavailable
				defaultFilename := "file"
				defaultMime := "application/octet-stream"
				if block.BlockType == reliantv1.ContentBlockType_CONTENT_BLOCK_TYPE_IMAGE {
					defaultFilename = "image"
					defaultMime = "image/jpeg"
				}
				logging.WarnContext(ctx, "Attachment not found in database, using placeholder", "attachmentID", attachmentID, "messageID", messageID)
				attachments = append(attachments, map[string]interface{}{
					"id":        attachmentID,
					"filename":  defaultFilename,
					"size":      0,
					"mime_type": defaultMime,
					"url":       fmt.Sprintf("/api/attachments/%s", attachmentID),
				})
			}
		}

	}
	// One serializer for a block's wire shape (see ContentBlockPayloads).
	contentBlocks := r.ContentBlockPayloads(ctx, blocks)

	// Get context_sequence from context_window for frontend compatibility
	var contextSequence int
	cw, err := r.GetContextWindow(ctx, msg.ContextWindowID)
	if err != nil {
		return nil, fmt.Errorf("failed to get context window for message %s: %w", msg.ID, err)
	}
	if cw != nil {
		contextSequence = cw.Sequence
	}

	// Build enriched message update matching MessageWithBlocks interface
	// seq is the chat-global order the client sorts by. Omitting it makes a
	// live message deserialize with seq 0, which sorts it to the top of the
	// transcript instead of the bottom — the message arrives but appears to
	// go missing. `ordinal` rides along only for consumers that have not
	// moved off it yet; nothing orders by it.
	enrichedData := MessageUpdateData{
		UpdateType:      "message",
		ID:              msg.ID,
		Role:            msg.Role,
		Seq:             msg.Seq,
		Ordinal:         msg.Ordinal,
		Thread:          msg.ThreadID, // Direct access
		ContextSequence: &contextSequence,
		ContextWindowID: msg.ContextWindowID,
		StreamingState:  streamingState.State, // Use computed state instead of field
		CreatedAt:       msg.CreatedAt.Format(time.RFC3339),
		UpdatedAt:       msg.UpdatedAt.Format(time.RFC3339),
		ContentBlocks:   contentBlocks,
		Attachments:     &attachments, // Include attachments for image preview
	}

	// Add optional fields
	if msg.TokenCount != nil {
		enrichedData.TokenCount = msg.TokenCount
	}
	// See the note in SaveMessageToThreadWithID: dropping display_style here
	// makes a HIDDEN message render until the next reload.
	if msg.DisplayStyle != nil {
		style := int32(*msg.DisplayStyle)
		enrichedData.DisplayStyle = &style
	}

	// Marshal back to JSON
	enrichedJSON, err := json.Marshal(enrichedData)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal enriched data: %w", err)
	}

	return enrichedJSON, nil
}

// ==================== Approval Methods ==
// Note: Old tool_approvals and workflow_approvals have been consolidated into approvals table
// Methods now in repository_impl.go using the new unified Approval model

// =============================================================================
// Workflow Status Query Functions
// =============================================================================

// Workflow execution methods removed - use Temporal client directly to query workflow state
// Temporal is the source of truth for workflow execution status

// IsChatBusy removed - use Temporal client to query running workflows for a chat

// Null type conversion helpers
func nullStringToPtr(ns sql.NullString) *string {
	if ns.Valid {
		return &ns.String
	}
	return nil
}

// =============================================================================
// Activity Idempotency Helpers
// =============================================================================

// GetActivityInfo extracts Temporal activity info from context
// Returns empty strings if not in activity context (for testing)
func GetActivityInfo(ctx context.Context) (activityID string, workflowRunID string, attemptNumber int) {
	// Try to get activity info from Temporal context
	// This will panic if not in activity context, so we recover
	defer func() {
		if r := recover(); r != nil {
			// Not in activity context - return empty values
			activityID = ""
			workflowRunID = ""
			attemptNumber = 1
		}
	}()

	// Import activity package dynamically to avoid circular dependencies
	// This is safe because GetActivityInfo is only called from activities
	info := activity.GetInfo(ctx)
	return info.ActivityID, info.WorkflowExecution.RunID, int(info.Attempt)
}

// =============================================================================
// User Updates (Global WebSocket for workspace-level updates)
// =============================================================================

// GetLatestUserUpdateSequence returns the latest sequence number for a user's updates
func (r *Repo) GetLatestUserUpdateSequence(ctx context.Context, userID string) (int64, error) {
	return store.LatestUserUpdateSequence(ctx, r.ORM(), userID)
}

// CreateUserUpdate creates a new user update for the global WebSocket.
//
// Sequence allocation and the ledger insert are atomic in one transaction, and
// a call made inside a caller's transaction joins it.
//
// # Why READ COMMITTED
//
// Every update for a user is allocated from ONE counter row, so every chat that
// user has running contends on it — a multi-spawn run is dozens of writers on
// a single row. Under SERIALIZABLE (the RunTx default) a writer that WAITS on
// that row lock is aborted with SQLSTATE 40001 the moment the holder commits:
// Postgres will not let a serializable transaction update a row version that
// changed after its snapshot. So each commit on the row aborts everyone queued
// behind it, and under real fan-out writers burned the whole retry budget and
// the update event was lost ("could not serialize access due to concurrent
// update"). Raising maxRetries only moved the threshold.
//
// At READ COMMITTED the same waiter re-evaluates against the committed row
// (EvalPlanQual) and takes N+1. The row lock is still held until COMMIT, so
// sequence order cannot diverge from commit order and the client's `WHERE
// sequence_number > cursor` resume contract holds exactly. This is the same
// reasoning, for the same shape of write, as SaveMessageAtomic's counters.
//
// What READ COMMITTED gives up is cross-statement stability. The two
// statements here do not need it: the insert uses only the value the upsert
// RETURNED, and reads nothing else.
//
// This only governs a transaction CreateUserUpdate opens itself. A call that
// joins an outer transaction (ctx already carries one) inherits that
// transaction's isolation — see RunTxWithOptions — so the workflow-status
// writers that emit activity inside their own SERIALIZABLE RunTx still allocate
// serializably, and still rely on the outer retry ladder.
func (r *Repo) CreateUserUpdate(ctx context.Context, update *UserUpdate) error {
	// Whether the caller supplied an explicit ID. When they didn't, derive the
	// ID again on every transaction retry because the scoped allocation from a
	// failed attempt rolls back and the retried attempt obtains the then-current
	// next value.
	callerSuppliedID := update.ID != ""
	return r.RunTxWithOptions(ctx, TxOptions{Isolation: IsolationReadCommitted}, func(txCtx context.Context) error {
		if err := store.AppendUserUpdate(txCtx, r.ORM(), update, callerSuppliedID); err != nil {
			return err
		}
		if r.onUserUpdate != nil {
			committedUpdate := *update
			return runAfterCommit(txCtx, func() {
				r.onUserUpdate(&committedUpdate)
			})
		}
		return nil
	})
}

// GetUserUpdatesSince returns all user updates since a given sequence number
func (r *Repo) GetUserUpdatesSince(ctx context.Context, userID string, sinceSeq int64, limit int) ([]UserUpdate, error) {
	if limit <= 0 {
		limit = 100
	}
	return store.UserUpdatesSince(ctx, r.ORM(), userID, sinceSeq, limit)
}

// UpdateChatState updates a chat's state and emits a user update
func (r *Repo) UpdateChatState(ctx context.Context, chatID string, state ChatState, reason string) error {
	// Get the chat first to get user_id and project_id
	chat, err := r.GetChat(ctx, chatID)
	if err != nil {
		return fmt.Errorf("failed to get chat: %w", err)
	}

	previousState := chat.State

	// If we're archiving, capture the worktree name onto the chat so archived chat
	// display remains stable even if the worktree row is later deleted.
	archivedWorktreeName := chat.ArchivedWorktreeName
	if state == ChatStateArchived && archivedWorktreeName == nil {
		if chat.WorktreeID != nil {
			if wt, err := r.GetWorktree(ctx, *chat.WorktreeID); err == nil {
				archivedWorktreeName = &wt.Name
			}
		}
	}

	// Update the chat state
	now := time.Now().UTC()
	query := `
		UPDATE chats
		SET state = ?, 
		    archived_worktree_name = COALESCE(archived_worktree_name, ?),
		    updated_at = ?,
		    last_active = ?
		WHERE id = ?
	`
	query = r.bindQuery(query)

	_, err = r.DB.ExecContext(ctx, query, int32(state), archivedWorktreeName, now, now, chatID)
	if err != nil {
		return fmt.Errorf("failed to update chat state: %w", err)
	}

	// Create user update for the state change
	updateData := map[string]interface{}{
		"state":          int32(state),
		"previous_state": int32(previousState),
		"reason":         reason,
		"title":          chat.Title,
		"chat_id":        chatID,
	}

	dataJSON, err := json.Marshal(updateData)
	if err != nil {
		return fmt.Errorf("failed to marshal state update: %w", err)
	}

	userUpdate := &UserUpdate{
		UserID:     chat.UserID,
		ProjectID:  &chat.ProjectID,
		WorktreeID: chat.WorktreeID,
		ChatID:     &chatID,
		UpdateType: UserUpdateChatStateChange,
		EntityType: EntityTypeChat,
		EntityID:   chatID,
		Data:       dataJSON,
	}

	if err := r.CreateUserUpdate(ctx, userUpdate); err != nil {
		// Log but don't fail - the state update succeeded
		logging.ErrorContext(ctx, "Failed to create user update for chat state change",
			"error", err,
			"chatID", chatID,
			"state", state)
	}

	// Also emit chat_activity_changed (with dedup) so the sidebar reflects updated activity
	if emitErr := r.emitChatActivityIfChanged(ctx, chatID); emitErr != nil {
		logging.ErrorContext(ctx, "Failed to emit activity changed on chat state change",
			"error", emitErr,
			"chatID", chatID)
	}

	logging.DebugContext(ctx, "Chat state updated",
		"chatID", chatID,
		"previousState", previousState,
		"newState", state,
		"reason", reason)

	return nil
}

// UpdateChatActiveDaemon sets the active daemon for a chat session.
// When daemonID is nil, the active daemon is cleared (revert to default resolution).
func (r *Repo) UpdateChatActiveDaemon(ctx context.Context, chatID string, daemonID *string) error {
	return r.chats.UpdateChatActiveDaemon(ctx, chatID, daemonID)
}

// SetChatAdopted adopts or un-adopts a chat for its owner. It reports whether
// the chat exists and belongs to userID. Repeating the call is a no-op that
// still reports true. Adoption changes list_in_sidebar, so it emits
// chat_activity_changed to make the sidebar refetch.
func (r *Repo) SetChatAdopted(ctx context.Context, chatID, userID string, adopted bool) (bool, error) {
	var owned bool
	err := r.RunTx(ctx, func(txCtx context.Context) error {
		ok, err := r.chats.SetChatAdopted(txCtx, chatID, userID, adopted)
		if err != nil || !ok {
			return err
		}
		owned = true
		return r.emitChatActivityIfChanged(txCtx, chatID)
	})
	return owned, err
}

// SetChatDaemonBlocked records (or clears) that the chat's run is blocked on a
// suspended or starting machine. It emits chat_activity_changed only when the
// stored value changed, so a stream of healthy tool calls costs one cheap
// UPDATE that matches no row.
func (r *Repo) SetChatDaemonBlocked(ctx context.Context, chatID string, blocked bool) error {
	return r.RunTx(ctx, func(txCtx context.Context) error {
		changed, err := r.chats.SetChatDaemonBlocked(txCtx, chatID, blocked)
		if err != nil || !changed {
			return err
		}
		return r.emitChatActivityIfChanged(txCtx, chatID)
	})
}

// SetChatQueuedForMachine records (or clears) that the chat holds a message
// queued for its machine, and reports whether the stored value changed. A
// clear that reports true is a claim: exactly one of two racing deliverers
// gets it. It emits chat_activity_changed only on a change.
func (r *Repo) SetChatQueuedForMachine(ctx context.Context, chatID string, queued bool) (bool, error) {
	var changed bool
	err := r.RunTx(ctx, func(txCtx context.Context) error {
		var err error
		changed, err = r.chats.SetChatQueuedForMachine(txCtx, chatID, queued)
		if err != nil || !changed {
			return err
		}
		return r.emitChatActivityIfChanged(txCtx, chatID)
	})
	return changed, err
}

// ListChatsQueuedForMachine lists chats holding a message queued for their
// machine; see the Repository interface.
func (r *Repo) ListChatsQueuedForMachine(ctx context.Context, userID string, limit int) ([]QueuedForMachineChat, error) {
	return r.chats.ListChatsQueuedForMachine(ctx, userID, limit)
}

// SetChatPausedForMachine records (or clears) that the daemon-offline breaker
// paused the chat's run, and reports whether the stored value changed.
func (r *Repo) SetChatPausedForMachine(ctx context.Context, chatID string, paused bool) (bool, error) {
	return r.chats.SetChatPausedForMachine(ctx, chatID, paused)
}

// ListChatsPausedForMachine lists chats the breaker paused; see the Repository
// interface.
func (r *Repo) ListChatsPausedForMachine(ctx context.Context, userID string, limit int) ([]PausedForMachineChat, error) {
	return r.chats.ListChatsPausedForMachine(ctx, userID, limit)
}

// ListChatsWaitingForMachine lists a user's chats whose live root run is
// parked waiting for a machine.
func (r *Repo) ListChatsWaitingForMachine(ctx context.Context, userID string) ([]WaitingForMachineRun, error) {
	return r.chats.ListChatsWaitingForMachine(ctx, userID)
}

// UpdateChatUnread sets the unread flag on a chat and emits a user update.
func (r *Repo) UpdateChatUnread(ctx context.Context, chatID string, unread bool, reason string) error {
	chat, err := r.GetChat(ctx, chatID)
	if err != nil {
		return fmt.Errorf("failed to get chat: %w", err)
	}

	unreadInt := 0
	if unread {
		unreadInt = 1
	}

	now := time.Now().UTC()
	query := `UPDATE chats SET unread = ?, updated_at = ? WHERE id = ?`
	query = r.bindQuery(query)

	_, err = r.DB.ExecContext(ctx, query, unreadInt, now, chatID)
	if err != nil {
		return fmt.Errorf("failed to update chat unread: %w", err)
	}

	updateData := map[string]interface{}{
		"unread":  unread,
		"reason":  reason,
		"chat_id": chatID,
	}

	dataJSON, err := json.Marshal(updateData)
	if err != nil {
		return fmt.Errorf("failed to marshal unread update: %w", err)
	}

	userUpdate := &UserUpdate{
		UserID:     chat.UserID,
		ProjectID:  &chat.ProjectID,
		WorktreeID: chat.WorktreeID,
		ChatID:     &chatID,
		UpdateType: UserUpdateChatStateChange,
		EntityType: EntityTypeChat,
		EntityID:   chatID,
		Data:       dataJSON,
	}

	if err := r.CreateUserUpdate(ctx, userUpdate); err != nil {
		logging.ErrorContext(ctx, "Failed to create user update for chat unread change",
			"error", err,
			"chatID", chatID,
			"unread", unread)
	}

	logging.DebugContext(ctx, "Chat unread updated",
		"chatID", chatID,
		"unread", unread,
		"reason", reason)

	return nil
}

// ============================================================================
// Node Execution Event Methods
// ============================================================================
// These methods emit real-time execution state to the chat_updates table for UI streaming

// EmitNodeExecutionEvent creates a node_execution update in chat_updates
// This is called when a workflow node starts, makes progress, or completes
func (r *Repo) EmitNodeExecutionEvent(ctx context.Context, eventType string, state *NodeExecutionState) error {
	if state == nil {
		return fmt.Errorf("node execution state is nil")
	}

	// Build the event data structure
	eventData := map[string]interface{}{
		"update_type": "node_execution",
		"event_type":  eventType, // "started", "progress", "completed", "failed"
		"node_id":     state.NodeID,
		"node_type":   state.NodeType,
		"status":      state.Status,
		"workflow_id": state.WorkflowID,
		"chat_id":     state.ChatID,
	}

	// Add optional fields if present
	if state.ParentNodeID != nil {
		eventData["parent_node_id"] = *state.ParentNodeID
	}
	if state.ActivityID != nil {
		eventData["activity_id"] = *state.ActivityID
	}
	if state.StartedAt != nil {
		eventData["started_at"] = state.StartedAt.UnixMilli()
	}
	if state.CompletedAt != nil {
		eventData["completed_at"] = state.CompletedAt.UnixMilli()
	}
	if state.DurationMs != nil {
		eventData["duration_ms"] = *state.DurationMs
	}
	if state.ExitCode != nil {
		eventData["exit_code"] = *state.ExitCode
	}
	if state.ErrorMessage != nil {
		eventData["error_message"] = *state.ErrorMessage
	}
	if state.Iteration != nil {
		eventData["iteration"] = *state.Iteration
	}
	if state.MaxIterations != nil {
		eventData["max_iterations"] = *state.MaxIterations
	}
	if len(state.Metadata) > 0 {
		eventData["metadata"] = state.Metadata
	}

	// Serialize to JSON
	eventJSON, err := json.Marshal(eventData)
	if err != nil {
		return fmt.Errorf("failed to marshal node execution event: %w", err)
	}

	// Use node_id as entity_id for deduplication and filtering
	entityID := fmt.Sprintf("%s:%s", state.WorkflowID, state.NodeID)

	return r.CreateChatUpdate(ctx, state.ChatID, UpdateTypeNodeExecution, entityID, string(eventJSON))
}
