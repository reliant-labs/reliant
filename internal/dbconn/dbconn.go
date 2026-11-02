// Copyright (c) 2025 Reliant Labs

// Package dbconn answers one question about a database error: did the
// database fail to answer at all, rather than answer "no"?
//
// The difference decides what a caller must do. A database that answered — a
// missing row, a constraint violation, a serialization conflict — said
// something about the data, and the caller handles it. A database that did not
// answer — Cloud SQL restarting for maintenance, a failover, PgBouncer unable to
// log in to the server behind it — said nothing about the data at all. The
// operation is as valid as it was a second ago and succeeds once the database
// is back, so the only correct response is to wait and try again: for minutes,
// not seconds, because a maintenance restart takes that long.
//
// Getting this wrong is not cosmetic. On 2026-11-02 a 28-second restart of
// reliant-pg failed three agent runs for good (reliant#2100): PgBouncer's
// cached login error reached threads.SaveMessage, which reported it as
// "thread … does not exist", and the activity layer classified "does not
// exist" as terminal.
//
//forge:exclude-contract: pure error classification over driver error types and texts; no I/O, state or time
package dbconn

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// Unavailable reports whether err means the database could not be reached or
// dropped the connection, as opposed to answering with an error about the
// statement or the data.
//
// It recognises the error by its type wherever the chain still carries one,
// and by its text otherwise (UnavailableText), because an error that crossed a
// serialization boundary — a Temporal activity failure — keeps only its
// message.
//
// It is deliberately specific to the database. A bare "connection reset by
// peer" or "unexpected EOF" is not matched: an LLM provider's transport says
// the same, and calling that a database outage would send whoever reads the
// error the wrong way. A deadline that expired while CONNECTING is matched
// (pgconn reports it as a ConnectError); a deadline on the caller's own
// context mid-statement is not.
func Unavailable(err error) bool {
	// The caller stopped waiting; the database did nothing wrong.
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}

	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return unavailableSQLState(pgErr.Code) || UnavailableText(pgErr.Message)
	}

	// The pool could not open a connection at all: refused, reset, DNS, TLS,
	// or the connect timeout. Every failure to connect is the database (or the
	// pooler in front of it) not answering.
	var connectErr *pgconn.ConnectError
	if errors.As(err, &connectErr) {
		return true
	}

	// database/sql's and pgx's own "this connection is gone" signals. pgconn
	// marks an error SafeToRetry only when the connection failed before the
	// statement was sent, which a closed or broken connection does.
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, sql.ErrConnDone) || pgconn.SafeToRetry(err) {
		return true
	}

	return UnavailableText(err.Error())
}

// unavailableSQLState reports whether a SQLSTATE says the server, or the
// pooler speaking for it, could not serve the connection.
//
//   - class 08, connection exception — including 08P01, which PgBouncer uses
//     for its own refusals (server_login_retry, "no more connections allowed");
//   - 57P01 admin_shutdown, 57P02 crash_shutdown, 57P03 cannot_connect_now: the
//     server is stopping, crashed, or still starting up;
//   - 53300 too_many_connections: the server is up but has no free slot, which
//     clears as clients reconnect after a restart.
func unavailableSQLState(code string) bool {
	switch {
	case strings.HasPrefix(code, "08"):
		return true
	case code == "57P01", code == "57P02", code == "57P03", code == "53300":
		return true
	}
	return false
}

// unavailableTexts are the messages a database outage reaches us with once
// the error's type is gone. Lower case; matched as substrings. Each is specific
// to Postgres, PgBouncer or the Go database drivers.
var unavailableTexts = []string{
	// PgBouncer refusing on behalf of a server it cannot log in to. The
	// "(server_login_retry)" suffix names the setting that caches the failure.
	"server_login_retry",
	"server login has been failing",
	"server conn crashed",
	"pgbouncer cannot connect to server",
	"no more connections allowed",
	// Postgres going away or not there yet.
	"terminating connection due to administrator command",
	"the database system is starting up",
	"the database system is shutting down",
	"the database system is in recovery mode",
	"sorry, too many clients already",
	// SQLSTATEs as pgx renders them: "... (SQLSTATE 08P01)".
	"(sqlstate 08",
	"(sqlstate 57p01)",
	"(sqlstate 57p02)",
	"(sqlstate 57p03)",
	"(sqlstate 53300)",
	// pgconn's ConnectError ("failed to connect to `user=… database=…`") and
	// the drivers noticing the connection is gone.
	"failed to connect to `",
	"driver: bad connection",
	"sql: connection is already closed",
	"conn closed",
}

// UnavailableText is Unavailable for an error that survives only as text.
func UnavailableText(msg string) bool {
	lower := strings.ToLower(msg)
	for _, text := range unavailableTexts {
		if strings.Contains(lower, text) {
			return true
		}
	}
	return false
}
