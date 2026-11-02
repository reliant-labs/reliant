// Copyright (c) 2025 Reliant Labs
package dbconn

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// pgbouncerLoginRetry is the error PgBouncer returned to every client during
// the 2026-11-02 reliant-pg restart, as pgx decodes it.
func pgbouncerLoginRetry() *pgconn.PgError {
	return &pgconn.PgError{
		Severity: "FATAL",
		Code:     "08P01",
		Message:  "server login has been failing, cached error: server conn crashed? (server_login_retry)",
	}
}

func TestUnavailable(t *testing.T) {
	t.Parallel()

	refused := func() error {
		// A real ConnectError: nothing listens on port 1.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := pgconn.Connect(ctx, "postgres://u:p@127.0.0.1:1/db?sslmode=disable&connect_timeout=1")
		if err == nil {
			t.Fatal("connecting to port 1 succeeded")
		}
		return err
	}()

	cases := []struct {
		name string
		err  error
		want bool
	}{
		// The incident, exactly as threads.SaveMessage wrapped it.
		{"pgbouncer server_login_retry, wrapped", fmt.Errorf(
			"thread t1 does not exist - must be created before saving messages: failed to get latest context window: %w",
			pgbouncerLoginRetry()), true},
		{"pgbouncer server_login_retry, text only", errors.New(
			"writing message (after 5 tries): failed to get latest context window: FATAL: server login has been failing, " +
				"cached error: server conn crashed? (server_login_retry) (SQLSTATE 08P01)"), true},
		{"admin shutdown 57P01", &pgconn.PgError{Code: "57P01", Message: "terminating connection due to administrator command"}, true},
		{"starting up 57P03", &pgconn.PgError{Code: "57P03", Message: "the database system is starting up"}, true},
		{"connection failure 08006", &pgconn.PgError{Code: "08006", Message: "connection failure"}, true},
		{"too many connections 53300", &pgconn.PgError{Code: "53300", Message: "sorry, too many clients already"}, true},
		{"connect refused", fmt.Errorf("get thread: %w", refused), true},
		{"database/sql bad conn", fmt.Errorf("query: %w", driver.ErrBadConn), true},
		{"database/sql conn done", sql.ErrConnDone, true},

		// The database answered: not an outage.
		{"no rows", fmt.Errorf("thread not found: %w", sql.ErrNoRows), false},
		{"unique violation", &pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint"}, false},
		{"serialization failure", &pgconn.PgError{Code: "40001", Message: "could not serialize access"}, false},
		{"statement timeout", &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"}, false},
		// Not the database at all: a provider's transport says the same words.
		{"bare connection reset", errors.New("stream llm response: read tcp: connection reset by peer"), false},
		{"provider 503", errors.New("stream llm response: 503 service unavailable"), false},
		{"websocket conn closed", errors.New("mcp transport: websocket conn closed"), false},
		{"http2 conn closed", errors.New("stream llm response: http2: client conn closed"), false},
		// pgconn's own closed connection, still typed.
		{"pgconn conn closed", fmt.Errorf("query: %w", pgconn.ErrConnClosed), true},
		// The caller stopped waiting.
		{"caller cancelled", fmt.Errorf("get thread: %w", context.Canceled), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Unavailable(tc.err); got != tc.want {
				t.Fatalf("Unavailable(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
