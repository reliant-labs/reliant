// Package dbquery runs one read-only SQL statement against a Postgres and
// returns typed, bounded, JSON-encodable results. It is the execution core
// behind `forge db query`: forge's local transport and the control plane's
// hosted QueryDatabase both call [Run], so a query means the same thing
// wherever it runs.
//
// # What every query does
//
//  1. [Check], the lexical guard: exactly one statement, and one that starts
//     SELECT, WITH, TABLE, VALUES, SHOW or EXPLAIN. It exists for good error
//     messages and is NOT the security boundary.
//  2. BEGIN TRANSACTION READ ONLY ISOLATION LEVEL READ COMMITTED, then
//     statement_timeout, lock_timeout and idle_in_transaction_session_timeout
//     set for that transaction only, and standard_conforming_strings on, so
//     the server reads string literals the way the guard did.
//  3. The statement goes over the extended protocol, which refuses a
//     multi-statement string on its own, and its rows are read up to the cap.
//     [Options.Args] bind the $n parameters the statement references.
//  4. ROLLBACK, always.
//
// # Why READ COMMITTED, and why there is no option
//
// A query is exactly one statement, and one statement sees one snapshot at
// every isolation level: READ COMMITTED takes its snapshot when the statement
// starts, REPEATABLE READ when the transaction's first statement does, and
// here those are the same moment. REPEATABLE READ buys nothing and costs a
// transaction-long snapshot that holds back vacuum and can raise
// serialization failures against the writers a live dashboard must never
// disturb. A caller that wants a multi-statement consistent read is asking
// for a different feature.
//
// The boundary is the read-only transaction, which refuses every write,
// nextval() and a data-modifying CTE, plus the role's privileges, which the
// caller owns (a role that cannot SELECT a column cannot read it through any
// expression). The context deadline is a client-side backstop to the
// server-side timeout: the guard refuses set_config(), but a denylist is not a
// boundary, and a statement that reached set_config('statement_timeout', '0')
// some other way is still stopped.
//
// # Bounds
//
// Rows are capped (default 200, at most 10,000), the encoded response is
// capped at 4 MiB, a single cell at 4 KiB, and a query's time at 5 s by
// default and 30 s at most. A result cut by a cap is not an error: it carries
// Truncated and says which cap.
//
// # Audit
//
// An [Auditor] sees every attempt that gets past argument validation, the
// ones the guard refuses included, so an attempted write against production
// is on record too. A failing Auditor withholds the rows.
//
// This package opens no connections and owns no credentials. The caller
// passes anything with a pgx BeginTx (a *pgx.Conn, a *pgxpool.Pool).
package dbquery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Limits. The Default* values apply to a zero Options field; a value above
// its Max* is refused, not clamped, so a caller learns the cap it asked past.
const (
	DefaultMaxRows = 200
	MaxRowsLimit   = 10_000

	DefaultTimeout = 5 * time.Second
	MaxTimeout     = 30 * time.Second

	// MaxCellBytes is the longest text a single cell keeps; the rest is
	// replaced by a marker.
	MaxCellBytes = 4 << 10
	// MaxResponseBytes caps the encoded size of all cells in a result.
	MaxResponseBytes = 4 << 20

	// DefaultLockTimeout is how long a statement waits for a lock; above
	// MaxLockTimeout is refused. A query that has to wait for a writer's lock
	// should give up fast rather than queue behind it.
	DefaultLockTimeout = time.Second
	MaxLockTimeout     = 5 * time.Second

	// DefaultIdleTimeout is idle_in_transaction_session_timeout; above
	// MaxTimeout is refused.
	DefaultIdleTimeout = 10 * time.Second
)

// ctxGrace is how long past the server-side timeout the client waits before it
// gives up on the connection itself. A variable so a test can shrink it.
var ctxGrace = 2 * time.Second

// Beginner is what [Run] needs from a connection. *pgx.Conn and
// *pgxpool.Pool both satisfy it.
type Beginner interface {
	BeginTx(ctx context.Context, txOptions pgx.TxOptions) (pgx.Tx, error)
}

// Options bound one query. The zero value is the default policy.
type Options struct {
	// MaxRows is the row cap. 0 means DefaultMaxRows; above MaxRowsLimit is
	// refused.
	MaxRows int
	// Timeout is the statement timeout. 0 means DefaultTimeout; above
	// MaxTimeout is refused.
	Timeout time.Duration
	// LockTimeout is how long the statement waits for a lock. 0 means
	// DefaultLockTimeout; above MaxLockTimeout is refused.
	LockTimeout time.Duration
	// IdleTimeout is idle_in_transaction_session_timeout. 0 means
	// DefaultIdleTimeout; above MaxTimeout is refused.
	IdleTimeout time.Duration
	// MaxBytes caps the encoded size of the cells kept, below the response
	// cap. 0 means MaxResponseBytes; above it is refused.
	MaxBytes int
	// Args are the values of the statement's $1, $2, ... parameters, bound
	// over the extended protocol so a value is never spliced into the SQL.
	// Only $1..$n for the highest $n the statement references are bound (see
	// [ParamRefs]); a reference past len(Args) is refused. Postgres infers a
	// parameter's type from its use, so cast one that has no context
	// ($1::timestamptz).
	Args []any
	// Auditor, when set, is told how every attempt went.
	Auditor Auditor

	// skipGuard bypasses [Check] so a test can prove the read-only
	// transaction refuses what the guard would have. Unexported on purpose:
	// no caller may turn the first line of defence off.
	skipGuard bool
}

// Column describes one result column.
type Column struct {
	Name string `json:"name"`
	// PGType is Postgres' name for the type ("int4", "timestamptz", an enum's
	// name).
	PGType string `json:"pg_type"`
}

// Result is a completed query. Each row has one cell per column, in column
// order. A cell is nil (NULL), a bool, a [json.Number], a string, or a
// [json.RawMessage] (json and jsonb); the whole value marshals with
// encoding/json as is.
//
// Numbers are exact: int2, int4 and floats are JSON numbers, and int8 and
// numeric are JSON numbers only up to 15 digits, a string beyond that, since
// a JSON reader that parses doubles would round them. Everything else, dates
// and times, uuid, arrays, bytea, enums, is the text Postgres prints for it.
type Result struct {
	Columns []Column `json:"columns"`
	Rows    [][]any  `json:"rows"`
	// RowCount is len(Rows).
	RowCount int `json:"row_count"`
	// Truncated is true when a cap cut the result; TruncatedBy names it:
	// "rows" or "bytes".
	Truncated   bool   `json:"truncated"`
	TruncatedBy string `json:"truncated_by,omitempty"`
	// CellsTruncated counts cells cut to MaxCellBytes.
	CellsTruncated int   `json:"cells_truncated,omitempty"`
	DurationMS     int64 `json:"duration_ms"`
	// Bytes is the encoded size of the cells kept, the figure the response
	// cap counts.
	Bytes int `json:"bytes"`
}

// Event is what an [Auditor] receives.
type Event struct {
	SQL       string
	SQLSHA256 string
	// Leader is the statement's leading keyword, empty when the guard
	// refused before finding one.
	Leader string
	// Outcome is "ok", or the [Kind] of the failure.
	Outcome string
	// Err is the failure, nil on success.
	Err        error
	RowCount   int
	Bytes      int
	Truncated  bool
	StartedAt  time.Time
	Duration   time.Duration
	MaxRows    int
	TimeoutSec float64
	// Args are the values bound to the statement's parameters, in order; nil
	// when none were. Values only: they are never part of SQL.
	Args []any
}

// Auditor records one query attempt. It runs after the attempt, once, on a
// context that survives the caller's cancellation. If it returns an error,
// [Run] returns KindAudit and no rows, unless the attempt itself already
// failed, in which case that failure is returned.
type Auditor interface {
	Audit(ctx context.Context, e Event) error
}

// AuditFunc adapts a function to [Auditor].
type AuditFunc func(ctx context.Context, e Event) error

func (f AuditFunc) Audit(ctx context.Context, e Event) error { return f(ctx, e) }

// Run executes sql as one bounded read-only query. Every failure is an
// *[Error].
func Run(ctx context.Context, db Beginner, sql string, opts Options) (*Result, error) {
	lim, err := opts.limits()
	if err != nil {
		return nil, err
	}
	start := time.Now()
	ev := Event{
		SQL:        sql,
		SQLSHA256:  sha(sql),
		StartedAt:  start,
		MaxRows:    lim.rows,
		TimeoutSec: lim.timeout.Seconds(),
	}

	res, runErr := run(ctx, db, sql, opts, lim, &ev)

	ev.Duration = time.Since(start)
	ev.Outcome = "ok"
	if runErr != nil {
		ev.Err = runErr
		ev.Outcome = string(runErr.Kind)
	} else {
		res.DurationMS = ev.Duration.Milliseconds()
		ev.RowCount, ev.Bytes, ev.Truncated = res.RowCount, res.Bytes, res.Truncated
	}
	if opts.Auditor != nil {
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if aerr := opts.Auditor.Audit(actx, ev); aerr != nil && runErr == nil {
			return nil, &Error{Kind: KindAudit, Message: "the query was not recorded, so its result is withheld: " + aerr.Error(), Err: aerr}
		}
	}
	if runErr != nil {
		return nil, runErr
	}
	return res, nil
}

// bounds are an Options' limits with the defaults filled in.
type bounds struct {
	rows        int
	timeout     time.Duration
	lockTimeout time.Duration
	idleTimeout time.Duration
	maxBytes    int
}

func (o Options) limits() (bounds, *Error) {
	b := bounds{rows: o.MaxRows, timeout: o.Timeout, lockTimeout: o.LockTimeout, idleTimeout: o.IdleTimeout, maxBytes: o.MaxBytes}
	switch {
	case b.rows < 0 || b.rows > MaxRowsLimit:
		return bounds{}, refused("max_rows", fmt.Sprintf("max rows must be between 1 and %d", MaxRowsLimit))
	case b.rows == 0:
		b.rows = DefaultMaxRows
	}
	switch {
	case b.timeout < 0 || b.timeout > MaxTimeout:
		return bounds{}, refused("timeout", fmt.Sprintf("timeout must be between 1ms and %s", MaxTimeout))
	case b.timeout == 0:
		b.timeout = DefaultTimeout
	}
	switch {
	case b.lockTimeout < 0 || b.lockTimeout > MaxLockTimeout:
		return bounds{}, refused("lock_timeout", fmt.Sprintf("lock timeout must be between 1ms and %s", MaxLockTimeout))
	case b.lockTimeout == 0:
		b.lockTimeout = DefaultLockTimeout
	}
	switch {
	case b.idleTimeout < 0 || b.idleTimeout > MaxTimeout:
		return bounds{}, refused("idle_timeout", fmt.Sprintf("idle timeout must be between 1ms and %s", MaxTimeout))
	case b.idleTimeout == 0:
		b.idleTimeout = DefaultIdleTimeout
	}
	switch {
	case b.maxBytes < 0 || b.maxBytes > MaxResponseBytes:
		return bounds{}, refused("max_bytes", fmt.Sprintf("max bytes must be between 1 and %d", MaxResponseBytes))
	case b.maxBytes == 0:
		b.maxBytes = MaxResponseBytes
	}
	return b, nil
}

func run(ctx context.Context, db Beginner, sql string, opts Options, lim bounds, ev *Event) (*Result, *Error) {
	if !opts.skipGuard {
		leader, err := Check(sql)
		if err != nil {
			return nil, classify(err)
		}
		ev.Leader = leader
	}

	refs, err := ParamRefs(sql)
	if err != nil {
		return nil, classify(err)
	}
	if refs > len(opts.Args) {
		return nil, refused("missing_args", fmt.Sprintf("the statement references $%d but %d arguments were given", refs, len(opts.Args)))
	}
	var args []any
	if refs > 0 {
		args = opts.Args[:refs:refs]
		ev.Args = args
	}
	timeout := lim.timeout

	ctx, cancel := context.WithTimeout(ctx, timeout+ctxGrace)
	defer cancel()

	tx, err := db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, classify(err)
	}
	defer func() {
		// A fresh context: the query's own may be the reason we are here.
		rctx, rcancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer rcancel()
		_ = tx.Rollback(rctx)
	}()

	if _, err := tx.Exec(ctx,
		// Qualified with pg_catalog: a database or role default search_path
		// that puts a schema ahead of pg_catalog would otherwise let a
		// same-named function there stand in for set_config.
		// standard_conforming_strings is pinned on because the guard lexes a
		// plain '...' string the way that setting reads it: with it off (a
		// database or role default can switch it off), a backslash would escape
		// the quote and the server would read a different statement than the
		// one the guard judged.
		`SELECT pg_catalog.set_config('statement_timeout', $1, true), pg_catalog.set_config('lock_timeout', $2, true), pg_catalog.set_config('idle_in_transaction_session_timeout', $3, true), pg_catalog.set_config('standard_conforming_strings', 'on', true)`,
		strconv.FormatInt(timeout.Milliseconds(), 10)+"ms",
		strconv.FormatInt(lim.lockTimeout.Milliseconds(), 10)+"ms",
		strconv.FormatInt(lim.idleTimeout.Milliseconds(), 10)+"ms",
	); err != nil {
		return nil, classify(err)
	}

	// QueryExecModeExec: the extended protocol with text results. It rejects
	// a multi-statement string, and text is how Postgres prints every type,
	// enums and extension types included.
	rows, err := tx.Query(ctx, sql, append([]any{pgx.QueryExecModeExec}, args...)...)
	if err != nil {
		return nil, classify(err)
	}
	defer rows.Close()

	fds := rows.FieldDescriptions()
	res := &Result{Columns: make([]Column, len(fds)), Rows: [][]any{}}
	oids := make([]uint32, len(fds))
	for i, fd := range fds {
		res.Columns[i].Name = fd.Name
		oids[i] = fd.DataTypeOID
	}

	for rows.Next() {
		if len(res.Rows) >= lim.rows {
			res.Truncated, res.TruncatedBy = true, "rows"
			break
		}
		raw := rows.RawValues()
		row := make([]any, len(raw))
		size := 0
		cut := 0
		for i, cell := range raw {
			v, n, wasCut := encodeCell(oids[i], cell)
			row[i] = v
			size += n
			if wasCut {
				cut++
			}
		}
		if res.Bytes+size > lim.maxBytes {
			res.Truncated, res.TruncatedBy = true, "bytes"
			break
		}
		res.Bytes += size
		res.CellsTruncated += cut
		res.Rows = append(res.Rows, row)
	}
	// Close drains whatever the cap left unread, bounded by the statement
	// timeout. A failure while draining does not undo a result we already
	// hold in full; only a failure before the cap does.
	rows.Close()
	drainErr := rows.Err()
	if drainErr != nil && !res.Truncated {
		return nil, classify(drainErr)
	}
	res.RowCount = len(res.Rows)

	// After a failed drain the transaction is aborted: name only the types
	// pgx knows rather than query for the rest.
	lookup := tx
	if drainErr != nil {
		lookup = nil
	}
	names, err := typeNames(ctx, lookup, tx.Conn().TypeMap(), oids)
	if err != nil {
		return nil, classify(err)
	}
	for i, oid := range oids {
		res.Columns[i].PGType = names[oid]
	}
	return res, nil
}

// typeNames names each OID: pgx knows the built-ins, and one pg_type lookup
// inside the same transaction names the rest (enums, domains, extensions).
func typeNames(ctx context.Context, tx pgx.Tx, tm *pgtype.Map, oids []uint32) (map[uint32]string, error) {
	names := make(map[uint32]string, len(oids))
	var unknown []uint32
	for _, oid := range oids {
		if _, done := names[oid]; done {
			continue
		}
		if t, ok := tm.TypeForOID(oid); ok {
			names[oid] = t.Name
			continue
		}
		names[oid] = "oid:" + strconv.FormatUint(uint64(oid), 10)
		unknown = append(unknown, oid)
	}
	if len(unknown) == 0 || tx == nil {
		return names, nil
	}
	rows, err := tx.Query(ctx, `SELECT oid::int8, typname FROM pg_catalog.pg_type WHERE oid = ANY($1::oid[])`, unknown)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var oid int64
		var name string
		if err := rows.Scan(&oid, &name); err != nil {
			return nil, err
		}
		names[uint32(oid)] = name
	}
	return names, rows.Err()
}

// encodeCell converts one text-format cell. It returns the value, the bytes
// it counts against the response cap, and whether it was cut to MaxCellBytes.
func encodeCell(oid uint32, raw []byte) (v any, size int, cut bool) {
	if raw == nil {
		return nil, 4, false
	}
	switch oid {
	case pgtype.BoolOID:
		return string(raw) == "t", 5, false
	case pgtype.Int2OID, pgtype.Int4OID, pgtype.Float4OID, pgtype.Float8OID:
		if s := string(raw); isJSONNumber(s) {
			return json.Number(s), len(s), false
		}
	case pgtype.Int8OID, pgtype.NumericOID:
		if s := string(raw); isJSONNumber(s) && digits(s) <= 15 {
			return json.Number(s), len(s), false
		}
	case pgtype.JSONOID, pgtype.JSONBOID:
		// Postgres only sends valid JSON; the check keeps one bad cell from
		// making the whole result unencodable.
		if len(raw) <= MaxCellBytes && json.Valid(raw) {
			return json.RawMessage(append([]byte(nil), raw...)), len(raw), false
		}
	}
	return text(raw)
}

// text returns raw as a string, cut at a rune boundary to MaxCellBytes with a
// marker saying how much there was.
func text(raw []byte) (any, int, bool) {
	if len(raw) <= MaxCellBytes {
		s := strings.ToValidUTF8(string(raw), "\uFFFD")
		return s, len(s), false
	}
	end := MaxCellBytes
	for end > 0 && !utf8.RuneStart(raw[end]) {
		end--
	}
	s := strings.ToValidUTF8(string(raw[:end]), "\uFFFD") + fmt.Sprintf("…[truncated: %d of %d bytes shown]", end, len(raw))
	return s, len(s), true
}

// isJSONNumber reports whether s is both a Postgres number and a valid JSON
// number: it excludes NaN and Infinity, which Postgres prints.
func isJSONNumber(s string) bool {
	if s == "" || !(s[0] == '-' || (s[0] >= '0' && s[0] <= '9')) {
		return false
	}
	return json.Valid([]byte(s))
}

func digits(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			n++
		}
	}
	return n
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
