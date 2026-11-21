package orm

import (
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/lib/pq"
)

// Ready-made classifiers for TxOptions.WithRetryOn. Each one is a
// func(error) bool that sees through %w, so it can also fill a
// retry.Policy's Retryable.
//
// None of them is retried by default. The default — 40001 / 40P01 — is the
// set of errors postgres raises to say "this transaction lost a race; run it
// again", and nothing else. The errors below are retryable only under
// assumptions about the body that the package cannot check, so a project
// states them where it knows they hold.

// RetryOnSQLState returns a classifier that reports true for a postgres error
// — *pgconn.PgError (pgx, what forge opens) or *pq.Error (lib/pq), anywhere
// in the %w chain — whose SQLSTATE is one of codes:
//
//	orm.Serializable.WithRetryOn(orm.RetryOnSQLState("40002", "25P02"))
//
// The codes worth naming:
//
//   - 40002 transaction_integrity_constraint_violation: a deferred
//     constraint failed at COMMIT. Retryable when a concurrent writer is the
//     cause, an answer when the body's own data is.
//   - 25P02 in_failed_sql_transaction: the body ran a statement after an
//     earlier one failed and its error was swallowed. The retry starts with
//     a fresh BEGIN; the cause is unknown (see RetryOnCommitRollback).
//   - 23505 unique_violation: only for a body that races another writer to
//     an insert and re-reads to find the winner's row. Everywhere else it is
//     the answer AlreadyExists, and retrying it turns that answer into
//     svcerr.Aborted after MaxAttempts rollbacks.
//
// codes are 5-character SQLSTATEs, matched case-insensitively. It panics on
// an empty list or a malformed code, as regexp.MustCompile does: a typo would
// otherwise be a classifier that silently never matches.
func RetryOnSQLState(codes ...string) func(error) bool {
	if len(codes) == 0 {
		panic("orm.RetryOnSQLState: no SQLSTATE codes")
	}
	want := make(map[string]struct{}, len(codes))
	for _, code := range codes {
		if len(code) != 5 {
			panic(fmt.Sprintf("orm.RetryOnSQLState: %q is not a 5-character SQLSTATE", code))
		}
		want[strings.ToUpper(code)] = struct{}{}
	}
	return func(err error) bool {
		state, _, ok := pgDiagnostic(err)
		if !ok {
			return false
		}
		_, hit := want[strings.ToUpper(state)]
		return hit
	}
}

// RetryOnCommitRollback reports whether err is a COMMIT that postgres turned
// into a ROLLBACK: pgx's ErrTxCommitRollback ("commit unexpectedly resulted
// in rollback") or lib/pq's ErrInFailedTransaction, anywhere in the %w chain
// (RunTx wraps the commit error in a *TransactionError, which unwraps).
//
// That happens when a statement failed inside the transaction and the body
// swallowed its error — a best-effort read that logs and carries on — then
// returned nil. Postgres had already aborted the transaction, so COMMIT
// rolls it back and nothing is committed: re-running the body is safe in the
// same sense any retry is.
//
//	orm.Serializable.WithRetryOn(orm.RetryOnCommitRollback)
//
// It is not retried by default because the cause is gone. Postgres answers
// the COMMIT with only a ROLLBACK tag, and both drivers return a sentinel
// that carries no SQLSTATE, so RunTx cannot tell a swallowed 40001 — which
// a retry cures, and which is the usual cause under SERIALIZABLE — from a
// swallowed 23505, 23514 or syntax error, which fails identically on every
// attempt. Retrying those by default would turn today's immediate failure
// into MaxAttempts rollbacks with backoff, ending in svcerr.Aborted. Opt in
// where the body's swallowed errors are conflicts; better still, return them.
func RetryOnCommitRollback(err error) bool {
	return errors.Is(err, pgx.ErrTxCommitRollback) || errors.Is(err, pq.ErrInFailedTransaction)
}
