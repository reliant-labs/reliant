package dbquery

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// Kind classifies why a query did not return rows. A caller maps it onto its
// own transport (a Connect code, a CLI exit code) with one switch; the
// package imports neither.
type Kind string

const (
	// KindRefused: the request was refused before it reached the database —
	// the lexical guard, an empty or oversized statement, or limits out of
	// range. The request is wrong; retrying it unchanged cannot help.
	KindRefused Kind = "refused"
	// KindReadOnly: the database refused a write inside the read-only
	// transaction. The guard let the statement through (or was bypassed); the
	// transaction is the real boundary, and this is it holding.
	KindReadOnly Kind = "read_only"
	// KindPermission: the role may not read something the statement named
	// (SQLSTATE 42501) — a column marked forge:secret, for example.
	KindPermission Kind = "permission_denied"
	// KindTimeout: the statement outran its timeout, or waited past the lock
	// timeout.
	KindTimeout Kind = "timeout"
	// KindCanceled: the caller's context was canceled.
	KindCanceled Kind = "canceled"
	// KindSQL: the database rejected the statement itself — a syntax error, an
	// unknown column, a multi-statement string reaching the extended protocol.
	KindSQL Kind = "sql_error"
	// KindUnavailable: no connection, or the connection broke.
	KindUnavailable Kind = "unavailable"
	// KindAudit: the [Auditor] failed. The rows are withheld.
	KindAudit Kind = "audit_failed"
)

// Error is every failure [Run] and [Check] return.
type Error struct {
	Kind Kind
	// Reason is a stable machine-readable subcode within Kind
	// ("multiple_statements", "statement_timeout", ...), empty when Kind says
	// it all.
	Reason string
	// Message is safe to show the caller who wrote the SQL.
	Message string
	// Err is the underlying cause, if any.
	Err error
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return string(e.Kind)
}

func (e *Error) Unwrap() error { return e.Err }

// KindOf reports the Kind of err, or "" when err is not (or does not wrap) an
// *Error.
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return ""
}

func refused(reason, msg string) *Error {
	return &Error{Kind: KindRefused, Reason: reason, Message: msg}
}

// classify turns whatever the driver returned into an *Error.
func classify(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return &Error{Kind: KindTimeout, Reason: "deadline", Message: "the query did not finish in time", Err: err}
	case errors.Is(err, context.Canceled):
		return &Error{Kind: KindCanceled, Message: "the query was canceled", Err: err}
	}
	var pg *pgconn.PgError
	if !errors.As(err, &pg) {
		return &Error{Kind: KindUnavailable, Message: "the database is unreachable: " + err.Error(), Err: err}
	}
	msg := pg.Message
	if pg.Hint != "" {
		msg += " (hint: " + pg.Hint + ")"
	}
	switch {
	case pg.Code == "25006":
		return &Error{Kind: KindReadOnly, Message: "this connection is read-only: " + pg.Message, Err: err}
	case pg.Code == "42601" && strings.Contains(pg.Message, "multiple commands"):
		return &Error{Kind: KindRefused, Reason: "multiple_statements", Message: "only one statement per query", Err: err}
	case pg.Code == "42501":
		return &Error{Kind: KindPermission, Message: msg, Err: err}
	case pg.Code == "57014" && strings.Contains(pg.Message, "statement timeout"):
		return &Error{Kind: KindTimeout, Reason: "statement_timeout", Message: "the statement exceeded its timeout", Err: err}
	case pg.Code == "57014":
		return &Error{Kind: KindCanceled, Message: pg.Message, Err: err}
	case pg.Code == "55P03":
		return &Error{Kind: KindTimeout, Reason: "lock_timeout", Message: "the statement waited too long for a lock", Err: err}
	case strings.HasPrefix(pg.Code, "08"), strings.HasPrefix(pg.Code, "57P"), pg.Code == "25P03":
		return &Error{Kind: KindUnavailable, Message: msg, Err: err}
	default:
		return &Error{Kind: KindSQL, Reason: pg.Code, Message: msg, Err: err}
	}
}
