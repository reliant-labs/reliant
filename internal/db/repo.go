// Copyright (c) 2025 Reliant Labs
package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	core "github.com/reliant-labs/reliant/internal/db/core"
	postgresstore "github.com/reliant-labs/reliant/internal/db/postgres"
	pgdb "github.com/reliant-labs/reliant/internal/db/postgres/generated"
	"github.com/reliant-labs/reliant/internal/logging"
	"github.com/reliant-labs/reliant/internal/observability"
	"go.temporal.io/sdk/activity"
)

type transactionKey string

const (
	txKey          transactionKey = `tx`
	afterCommitKey transactionKey = `after-commit`

	// Retry configuration
	maxRetries     = 3
	baseRetryDelay = 50 * time.Millisecond
	maxRetryDelay  = 1 * time.Second

	// Default timeout for database operations
	defaultDBTimeout = 10 * time.Second
)

// afterCommitCallbacks belongs to one transaction ATTEMPT. A failed attempt's
// callbacks are discarded with its transaction; only the callbacks collected
// by the attempt that actually commits are run.
type afterCommitCallbacks struct {
	callbacks []func()
}

func runAfterCommit(ctx context.Context, callback func()) error {
	callbacks, ok := ctx.Value(afterCommitKey).(*afterCommitCallbacks)
	if !ok || callbacks == nil {
		return errors.New("after-commit callback requires repository transaction")
	}
	callbacks.callbacks = append(callbacks.callbacks, callback)
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
	planTasks       core.PlanTaskStore
	chats           core.ChatStore
	messages        core.MessageStore
	approvals       core.ApprovalStore
	agentMessages   core.AgentMessageStore
	projects        core.ProjectStore
	worktrees       core.WorktreeStore
	repos           core.RepoStore
	settings        core.SettingStore
	attachments     core.AttachmentStore
	workflows       core.WorkflowStore
	threads         core.ThreadStore
	contextWindows  core.ContextWindowStore
	workflowCatalog core.WorkflowCatalogStore
	tokenCounts     tokenCountStore

	// Update notifiers — set via SetUpdateNotifiers to push events to
	// streaming hubs after DB writes. Nil = no notification (tests, CLI).
	onUserUpdate UserUpdateNotifier
	onChatUpdate ChatUpdateNotifier
}

// NewRepo creates a new Repo with the given database
func NewRepo(db *sql.DB) *Repo {
	return NewRepoWithDriver(db, DriverPostgres)
}

func NewRepoWithDriver(db *sql.DB, driver DatabaseDriver) *Repo {
	q := &WrappedDBTX{db: db}
	pgQueries := pgdb.New(q)

	tokenCounts := newSQLTokenCountStore(q, func(query string) string {
		return (&Repo{driver: driver}).bindQuery(query)
	})

	return &Repo{
		DB:            q,
		driver:        driver,
		planTasks:     postgresstore.NewPlanTaskStore(pgQueries),
		chats:         postgresstore.NewChatStore(pgQueries, q),
		messages:      postgresstore.NewMessageStore(pgQueries, q),
		approvals:     postgresstore.NewApprovalStore(pgQueries),
		agentMessages: postgresstore.NewAgentMessageStore(pgQueries, q),
		projects:      postgresstore.NewProjectStore(pgQueries),
		worktrees:     postgresstore.NewWorktreeStore(pgQueries),
		repos:         postgresstore.NewRepoStore(pgQueries),
		settings: postgresstore.NewSettingStore(pgQueries, q, func(query string) string {
			return (&Repo{driver: DriverPostgres}).bindQuery(query)
		}),
		attachments:     postgresstore.NewAttachmentStore(pgQueries, q),
		workflows:       postgresstore.NewWorkflowStore(pgQueries),
		threads:         postgresstore.NewThreadStore(pgQueries),
		contextWindows:  postgresstore.NewContextWindowStore(pgQueries),
		workflowCatalog: postgresstore.NewWorkflowCatalogStore(pgQueries),
		tokenCounts:     tokenCounts,
	}
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

func NewRepoFromDir(dbPath string) (*Repo, error) {
	cfg, err := ResolveDatabaseConfig(dbPath)
	if err != nil {
		return nil, err
	}

	db, err := ConnectWithConfig(cfg)
	if err != nil {
		return nil, err
	}

	return NewRepoWithDriver(db, cfg.Driver), nil
}

func NewRepoFromConfig(cfg DatabaseConfig) (*Repo, error) {
	db, err := ConnectWithConfig(cfg)
	if err != nil {
		return nil, err
	}

	return NewRepoWithDriver(db, cfg.Driver), nil
}

func DatabaseDriverFromEnv() DatabaseDriver {
	driver, err := ParseDatabaseDriver(os.Getenv("DATABASE_DRIVER"))
	if err != nil {
		return DriverPostgres
	}
	return driver
}

func (r *Repo) Close() error {
	return r.DB.Close()
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
func (r *Repo) executeTransaction(ctx context.Context, tx *sql.Tx, f func(ctx context.Context) error) txResult {
	var result txResult

	// Setup deferred rollback for panics and errors
	defer func() {
		if !result.committed {
			tx.Rollback()
		}
	}()

	// Execute the transaction function. After-commit callbacks live on this
	// attempt's context so a SERIALIZABLE retry cannot publish events created by
	// the rolled-back attempt.
	execTime := time.Now()
	afterCommit := &afterCommitCallbacks{}
	txCtx := context.WithValue(ctx, txKey, tx)
	txCtx = context.WithValue(txCtx, afterCommitKey, afterCommit)
	err := f(txCtx)
	result.execDuration = time.Since(execTime)

	if err != nil {
		result.err = err
		return result
	}

	// Try to commit
	commitTime := time.Now()
	err = tx.Commit()
	result.commitDuration = time.Since(commitTime)

	if err != nil {
		observability.DBErrorsTotal.WithLabelValues("commit", string(r.driver)).Inc()
		result.err = err
		return result
	}

	result.committed = true
	for _, callback := range afterCommit.callbacks {
		callback()
	}
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
	if _, ok := ctx.Value(txKey).(pgdb.DBTX); ok {
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
				logging.Debug("Transaction begin failed, retrying",
					"attempt", attempt+1,
					"maxRetries", maxRetries,
					"error", err)
				time.Sleep(calculateBackoff(attempt))
				continue
			}
			logging.Error("Failed to begin transaction", "error", err, "attempts", attempt+1)
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
			logging.Debug("Transaction failed, retrying",
				"attempt", attempt+1,
				"maxRetries", maxRetries,
				"error", result.err)
			time.Sleep(calculateBackoff(attempt))
			continue
		}

		// Non-retryable business errors (e.g. sql.ErrNoRows from a lookup
		// inside the tx) are expected control flow for callers — log them
		// quietly so genuine transaction failures stand out.
		if errors.Is(result.err, sql.ErrNoRows) {
			logging.Debug("Transaction returned business error (not retryable)",
				"error", result.err,
				"attempts", attempt+1,
				"duration", time.Since(metrics.txStartTime))
		} else {
			logging.Error("Transaction failed",
				"error", result.err,
				"attempts", attempt+1,
				"duration", time.Since(metrics.txStartTime))
		}
		return result.err
	}

	// All retries exhausted
	logging.Error("Transaction failed after all retries",
		"retries", metrics.totalRetries,
		"lastError", lastErr,
		"duration", time.Since(metrics.txStartTime))
	observability.DBErrorsTotal.WithLabelValues("transaction", string(r.driver)).Inc()

	if lastErr != nil {
		return errors.New("transaction failed after retries: " + lastErr.Error())
	}
	return errors.New("transaction failed after retries")
}

// attemptTransaction tries to begin and execute a single transaction
// Returns (result, nil) if transaction was started, (nil, err) if begin failed
func (r *Repo) attemptTransaction(ctx context.Context, opts TxOptions, f func(ctx context.Context) error, metrics *txMetrics) (*txResult, error) {
	beginTime := time.Now()
	tx, err := r.DB.BeginTxWithOptions(ctx, opts)
	if err != nil {
		return nil, err
	}

	result := r.executeTransaction(ctx, tx, f)
	result.beginDuration = time.Since(beginTime)
	return &result, nil
}

type WrappedDBTX struct {
	db *sql.DB
}

func (w *WrappedDBTX) DB(ctx context.Context) pgdb.DBTX {
	tx, ok := ctx.Value(txKey).(pgdb.DBTX)
	if ok && tx != nil {
		return tx
	}

	return w.db
}

// BeginImmediate starts a read-write transaction with Serializable isolation.
func (w *WrappedDBTX) BeginImmediate(ctx context.Context) (*sql.Tx, error) {
	return w.BeginTxWithOptions(ctx, TxOptions{})
}

// BeginTxWithOptions starts a SERIALIZABLE transaction, read-only when asked.
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
	if _, inTx := ctx.Value(txKey).(pgdb.DBTX); inTx {
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
			logging.Debug("Database exec failed, retrying",
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
	if _, inTx := ctx.Value(txKey).(pgdb.DBTX); inTx {
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
			logging.Debug("Database query failed, retrying",
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
	var sequence sql.NullInt64

	query := `
		SELECT MAX(sequence_number)
		FROM chat_updates
		WHERE chat_id = ?
	`
	query = r.bindQuery(query)

	err := r.DB.DB(ctx).QueryRowContext(ctx, query, chatID).Scan(&sequence)
	if err != nil {
		return 0, err
	}

	if !sequence.Valid {
		return 0, nil // No updates yet
	}

	return sequence.Int64, nil
}

const (
	updateStreamKindUser = "user"
	updateStreamKindChat = "chat"
)

// allocateUpdateSequence advances one logical stream's transactional counter.
//
// The counter update and the ledger insert MUST share a transaction. The row
// lock is what prevents N+1 from committing before N, and rolling the
// transaction back must roll both the counter and ledger row back together.
// Keeping this primitive transaction-only makes it impossible for a future
// caller to accidentally consume a cursor outside the durable write.
func (r *Repo) allocateUpdateSequence(ctx context.Context, streamKind, streamID string) (int64, error) {
	if _, ok := ctx.Value(txKey).(pgdb.DBTX); !ok {
		return 0, fmt.Errorf("allocate %s update sequence: transaction required", streamKind)
	}

	var nextSeq int64
	query := `
		INSERT INTO update_stream_counters (
			stream_kind, stream_id, last_assigned_seq
		) VALUES (?, ?, 1)
		ON CONFLICT (stream_kind, stream_id) DO UPDATE
		SET last_assigned_seq = update_stream_counters.last_assigned_seq + 1
		RETURNING last_assigned_seq
	`
	query = r.bindQuery(query)

	err := r.DB.DB(ctx).QueryRowContext(ctx, query, streamKind, streamID).Scan(&nextSeq)
	if err != nil {
		return 0, fmt.Errorf("allocate %s update sequence: %w", streamKind, err)
	}
	return nextSeq, nil
}

// CreateChatUpdate creates a new chat update (for dual-write pattern)
// This operation is atomic - the sequence number generation and insert happen in a transaction
// to prevent race conditions when multiple parallel operations try to create updates.
func (r *Repo) CreateChatUpdate(ctx context.Context, chatID string, updateType reliantv1.ChatUpdateType, entityID string, data string) error {
	var chatUpdate ChatUpdate

	// Wrap in transaction to make sequence number generation + insert atomic
	// This prevents UNIQUE constraint violations when parallel goroutines try to create updates
	err := r.RunTx(ctx, func(ctx context.Context) error {
		// Get next sequence number within the transaction
		nextSeq, err := r.allocateUpdateSequence(ctx, updateStreamKindChat, chatID)
		if err != nil {
			return fmt.Errorf("failed to get next sequence number: %w", err)
		}

		// Generate ID
		updateID := fmt.Sprintf("%s-%d", chatID, nextSeq)

		// UTC, not local. chat_updates.created_at is TIMESTAMP WITHOUT TIME
		// ZONE, so the driver drops the offset and keeps the wall clock: a
		// local time.Now() is stored as its local reading and then read back
		// and serialized as RFC3339 with a "Z". The value does not become
		// wrong at the format step — it is already wrong here, and no consumer
		// can recover it. Every sibling writer in this file already does this.
		createdAt := time.Now().UTC()

		chatUpdate = ChatUpdate{
			ID:             updateID,
			ChatID:         chatID,
			SequenceNumber: nextSeq,
			UpdateType:     updateType,
			EntityID:       entityID,
			Data:           json.RawMessage(data),
			CreatedAt:      createdAt,
		}

		err = r.chats.CreateChatUpdate(ctx, chatUpdate)
		if err != nil {
			return err
		}

		if r.onChatUpdate != nil {
			committedUpdate := chatUpdate
			if err := runAfterCommit(ctx, func() {
				r.onChatUpdate(chatID, committedUpdate.SequenceNumber, committedUpdate)
			}); err != nil {
				return err
			}
		}

		return nil
	})
	if err != nil {
		return err
	}

	return nil
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
	// Tool-call updates need a different identity than entity_id. Their entity
	// id is built as "tool-<tool_call_id>-<timestamp>"
	// (EntityIDForToolCall), so every status transition of the SAME tool call
	// — pending → executing → completed — lands under a DISTINCT entity_id and
	// per-entity dedup can never collapse them. The frontend keys tool state by
	// tool_call_id and only ever renders the latest status, so replaying every
	// historical transition is pure weight: deduping on the JSON tool_call_id
	// instead of the whole entity_id halves the update count on a long chat
	// (measured 10,571 → 5,327).
	//
	// The tool id can contain '-' (for example resumptions and synthesized ids),
	// so parsing it back out of entity_id with split_part is unsafe. The payload
	// already carries the canonical key; use data::jsonb->>'tool_call_id'.
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
	query := `
		SELECT DISTINCT ON (dedup_key)
			id,
			chat_id,
			sequence_number,
			update_type,
			entity_id,
			data,
			created_at
		FROM (
			SELECT
				id,
				chat_id,
				sequence_number,
				update_type,
				entity_id,
				data,
				created_at,
				CASE
					WHEN update_type = ? THEN COALESCE(NULLIF(data::jsonb->>'tool_call_id', ''), entity_id)
					WHEN update_type = ? THEN left(entity_id, length(entity_id) - position('-' in reverse(entity_id)))
					ELSE entity_id
				END AS dedup_key
			FROM chat_updates
			WHERE chat_id = ? AND update_type NOT IN (?, ?)
		) t
		ORDER BY dedup_key, sequence_number DESC
	`
	query = r.bindQuery(query)

	rows, err := r.DB.DB(ctx).QueryContext(ctx, query,
		int(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_TOOL_CALL),
		int(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_QUESTION),
		chatID,
		int(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE),
		int(reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_STREAM_FINALIZED))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	updates := []ChatUpdate{}
	for rows.Next() {
		var update ChatUpdate
		var dataJSON string

		if err := rows.Scan(
			&update.ID,
			&update.ChatID,
			&update.SequenceNumber,
			&update.UpdateType,
			&update.EntityID,
			&dataJSON,
			&update.CreatedAt,
		); err != nil {
			return nil, err
		}

		// No message-update enrichment here: this query excludes them by
		// construction, which is the entire point.
		update.Data = json.RawMessage(dataJSON)
		updates = append(updates, update)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// DISTINCT ON forces ORDER BY entity_id; restore sequence order so
	// consumers see updates in the order they were emitted.
	sort.Slice(updates, func(i, j int) bool {
		return updates[i].SequenceNumber < updates[j].SequenceNumber
	})

	return updates, nil
}

// GetUpdatesSince returns all updates since a given sequence number
// This is used for polling clients to fetch new updates
// limit: maximum number of updates to return (defaults to 100 if <= 0)
func (r *Repo) GetUpdatesSince(ctx context.Context, chatID string, sinceSeq int64, limit int) ([]ChatUpdate, error) {
	// Default to 100 updates per batch if not specified or invalid
	if limit <= 0 {
		limit = 100
	}

	query := `
		SELECT
			id,
			chat_id,
			sequence_number,
			update_type,
			entity_id,
			data,
			created_at
		FROM chat_updates
		WHERE chat_id = ? AND sequence_number > ?
		ORDER BY sequence_number ASC
		LIMIT ?
	`
	query = r.bindQuery(query)

	rows, err := r.DB.DB(ctx).QueryContext(ctx, query, chatID, sinceSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var updates []ChatUpdate
	for rows.Next() {
		var update ChatUpdate
		var dataJSON string

		err := rows.Scan(
			&update.ID,
			&update.ChatID,
			&update.SequenceNumber,
			&update.UpdateType,
			&update.EntityID,
			&dataJSON,
			&update.CreatedAt,
		)
		if err != nil {
			return nil, err
		}

		// Parse JSON data field
		update.Data = json.RawMessage(dataJSON)

		// Enrich message updates with content blocks
		if update.UpdateType == reliantv1.ChatUpdateType_CHAT_UPDATE_TYPE_MESSAGE {
			// Check if this is a minimal message update (not already enriched)
			var dataMap map[string]interface{}
			if err := json.Unmarshal(update.Data, &dataMap); err == nil {
				// Only enrich if content_blocks is missing
				if _, hasContentBlocks := dataMap["content_blocks"]; !hasContentBlocks {
					enrichedData, err := r.EnrichMessageUpdate(ctx, update)
					if err != nil {
						// Log error but continue with original data
						logging.Warn("Failed to enrich message update",
							"error", err,
							"messageID", update.EntityID,
							"sequence", update.SequenceNumber)
					} else {
						update.Data = enrichedData
					}
				}
			}
		}

		updates = append(updates, update)
	}

	if updates == nil {
		updates = []ChatUpdate{} // Return empty slice instead of nil
	}

	// COMPREHENSIVE LOGGING - what we found
	updateTypes := make(map[reliantv1.ChatUpdateType]int)
	for _, update := range updates {
		updateTypes[update.UpdateType]++
	}

	return updates, rows.Err()
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
			logging.Warn("Failed to fetch attachments for message update", "error", err, "messageID", messageID)
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
				logging.Warn("Attachment not found in database, using placeholder", "attachmentID", attachmentID, "messageID", messageID)
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

func nullTimeToPtr(nt sql.NullTime) *time.Time {
	if nt.Valid {
		return &nt.Time
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
	var sequence sql.NullInt64

	query := `
		SELECT MAX(sequence_number)
		FROM user_updates
		WHERE user_id = ?
	`
	query = r.bindQuery(query)

	err := r.DB.DB(ctx).QueryRowContext(ctx, query, userID).Scan(&sequence)
	if err != nil {
		return 0, err
	}

	if !sequence.Valid {
		return 0, nil // No updates yet
	}

	return sequence.Int64, nil
}

// CreateUserUpdate creates a new user update for the global WebSocket.
//
// This mirrors CreateChatUpdate semantics:
// 1) sequence allocation and insert are atomic in a transaction
// 2) writes go through RunTx with retry logic for transient errors
// 3) nested transaction calls (ctx already carrying txKey) are supported
func (r *Repo) CreateUserUpdate(ctx context.Context, update *UserUpdate) error {
	// Whether the caller supplied an explicit ID. When they didn't, derive the
	// ID again on every transaction retry because the scoped allocation from a
	// failed attempt rolls back and the retried attempt obtains the then-current
	// next value.
	callerSuppliedID := update.ID != ""
	err := r.RunTx(ctx, func(txCtx context.Context) error {
		// Get next sequence number within the transaction for atomicity.
		nextSeq, err := r.allocateUpdateSequence(txCtx, updateStreamKindUser, update.UserID)
		if err != nil {
			return fmt.Errorf("failed to get next user sequence number: %w", err)
		}
		update.SequenceNumber = nextSeq

		if !callerSuppliedID {
			update.ID = fmt.Sprintf("%s-%d", update.UserID, nextSeq)
		}

		query := `
			INSERT INTO user_updates (
				id, user_id, sequence_number, project_id, worktree_id, chat_id,
				update_type, entity_type, entity_id, data, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`
		query = r.bindQuery(query)

		_, err = r.DB.ExecContext(txCtx, query,
			update.ID,
			update.UserID,
			update.SequenceNumber,
			update.ProjectID,
			update.WorktreeID,
			update.ChatID,
			int64(update.UpdateType),
			int64(update.EntityType),
			update.EntityID,
			string(update.Data),
			time.Now().UTC(),
		)
		if err != nil {
			return fmt.Errorf("failed to insert user update: %w", err)
		}

		if r.onUserUpdate != nil {
			committedUpdate := *update
			if err := runAfterCommit(txCtx, func() {
				r.onUserUpdate(&committedUpdate)
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	return nil
}

// GetUserUpdatesSince returns all user updates since a given sequence number
func (r *Repo) GetUserUpdatesSince(ctx context.Context, userID string, sinceSeq int64, limit int) ([]UserUpdate, error) {
	if limit <= 0 {
		limit = 100
	}

	query := `
		SELECT
			id, user_id, sequence_number, project_id, worktree_id, chat_id,
			update_type, entity_type, entity_id, data, created_at
		FROM user_updates
		WHERE user_id = ? AND sequence_number > ?
		ORDER BY sequence_number ASC
		LIMIT ?
	`
	query = r.bindQuery(query)

	rows, err := r.DB.DB(ctx).QueryContext(ctx, query, userID, sinceSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var updates []UserUpdate
	for rows.Next() {
		var update UserUpdate
		var projectID, worktreeID, chatID sql.NullString
		var dataJSON string
		var updateTypeInt, entityTypeInt int64

		err := rows.Scan(
			&update.ID,
			&update.UserID,
			&update.SequenceNumber,
			&projectID,
			&worktreeID,
			&chatID,
			&updateTypeInt,
			&entityTypeInt,
			&update.EntityID,
			&dataJSON,
			&update.CreatedAt,
		)
		if err != nil {
			return nil, err
		}

		update.ProjectID = nullStringToPtr(projectID)
		update.WorktreeID = nullStringToPtr(worktreeID)
		update.ChatID = nullStringToPtr(chatID)
		update.UpdateType = UserUpdateType(updateTypeInt)
		update.EntityType = UserUpdateEntityType(entityTypeInt)
		update.Data = json.RawMessage(dataJSON)

		updates = append(updates, update)
	}

	if updates == nil {
		updates = []UserUpdate{}
	}

	return updates, rows.Err()
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
		logging.Error("Failed to create user update for chat state change",
			"error", err,
			"chatID", chatID,
			"state", state)
	}

	// Also emit chat_activity_changed (with dedup) so the sidebar reflects updated activity
	if emitErr := r.emitChatActivityIfChanged(ctx, chatID); emitErr != nil {
		logging.Error("Failed to emit activity changed on chat state change",
			"error", emitErr,
			"chatID", chatID)
	}

	logging.Debug("Chat state updated",
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
		logging.Error("Failed to create user update for chat unread change",
			"error", err,
			"chatID", chatID,
			"unread", unread)
	}

	logging.Debug("Chat unread updated",
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
