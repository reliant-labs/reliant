package orm

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
)

// A service that writes through the generated CRUD gets the driver error
// verbatim — the handler-side classification in pkg/crud is downstream of it
// and a service calling CreateJob inside a transaction never reaches it. These
// tests pin the predicates that let the service tell a closed race (a UNIQUE it
// declared on purpose) from a genuine fault, WITHOUT matching driver prose.
//
// Every case runs the error through fmt.Errorf("%w") as well, because a service
// that follows the service-layer skill wraps before it returns, and a
// classifier that only works on the bare error is a classifier that stops
// working the moment it is used as documented.

func TestIsUniqueViolation(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"pgx unique", &pgconn.PgError{Code: "23505", ConstraintName: "jobs_estimate_id_key"}, true},
		{"pq unique", &pq.Error{Code: "23505", Constraint: "invoices_job_id_key"}, true},
		{"pgx check", &pgconn.PgError{Code: "23514", ConstraintName: "invoices_not_overpaid"}, false},
		{"pgx foreign key", &pgconn.PgError{Code: "23503"}, false},
		{"pgx not null", &pgconn.PgError{Code: "23502", ColumnName: "total_cents"}, false},
		{"no rows", sql.ErrNoRows, false},
		{"plain error", errors.New("boom"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsUniqueViolation(tc.err); got != tc.want {
				t.Errorf("IsUniqueViolation(%v) = %v, want %v", tc.err, got, tc.want)
			}
			if tc.err == nil {
				return
			}
			wrapped := fmt.Errorf("create job: %w", tc.err)
			if got := IsUniqueViolation(wrapped); got != tc.want {
				t.Errorf("IsUniqueViolation(wrapped %v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsCheckViolation(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"pgx check", &pgconn.PgError{Code: "23514", ConstraintName: "invoices_not_overpaid"}, true},
		{"pq check", &pq.Error{Code: "23514", Constraint: "invoices_not_overpaid"}, true},
		{"pgx unique", &pgconn.PgError{Code: "23505"}, false},
		{"no rows", sql.ErrNoRows, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsCheckViolation(tc.err); got != tc.want {
				t.Errorf("IsCheckViolation(%v) = %v, want %v", tc.err, got, tc.want)
			}
			if tc.err == nil {
				return
			}
			wrapped := fmt.Errorf("record payment: %w", tc.err)
			if got := IsCheckViolation(wrapped); got != tc.want {
				t.Errorf("IsCheckViolation(wrapped %v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// ConstraintName is what makes the predicates usable on a table with more than
// one UNIQUE: "some unique failed" cannot choose between "this estimate already
// sold a job" and "this job number is taken", and a service that guesses picks
// wrong half the time.
func TestConstraintName(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"pgx constraint", &pgconn.PgError{Code: "23505", ConstraintName: "jobs_estimate_id_key"}, "jobs_estimate_id_key"},
		{"pq constraint", &pq.Error{Code: "23514", Constraint: "invoices_not_overpaid"}, "invoices_not_overpaid"},
		{"not-null reports column, not constraint", &pgconn.PgError{Code: "23502", ColumnName: "total_cents"}, ""},
		{"plain error", errors.New("boom"), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ConstraintName(tc.err); got != tc.want {
				t.Errorf("ConstraintName(%v) = %q, want %q", tc.err, got, tc.want)
			}
			if tc.err == nil {
				return
			}
			wrapped := fmt.Errorf("approve estimate: %w", tc.err)
			if got := ConstraintName(wrapped); got != tc.want {
				t.Errorf("ConstraintName(wrapped %v) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}
