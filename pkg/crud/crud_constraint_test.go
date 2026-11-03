package crud

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// TestMapRepoErr_ConstraintViolations pins the constraint→code AND
// constraint→reason mapping that replaced the "everything is a 500"
// collapse. It runs hermetically (no postgres) by synthesizing the driver
// error each supported driver would produce for a given SQLSTATE, so it
// covers BOTH jackc/pgx (*pgconn.PgError, what generated apps use) and
// lib/pq (*pq.Error, what the embedded-pg test harness uses) — and asserts
// no SQLSTATE / SQL text crosses the wire.
//
// The reason assertion is not decoration. For a long time this test
// asserted ONLY the Connect code, and it passed green the entire time
// forge shipped zero x-forge-error-reason headers — while
// web-runtime/src/errors.ts told frontend authors to key off exactly that
// header. A test that checks the coarse half of a contract and skips the
// half the docs point at is worse than no test: it certifies the gap.
func TestMapRepoErr_ConstraintViolations(t *testing.T) {
	cases := []struct {
		name       string
		sqlstate   string
		want       connect.Code
		wantReason string
	}{
		{"unique_violation", "23505", connect.CodeAlreadyExists, ReasonDuplicate},
		{"foreign_key_violation", "23503", connect.CodeFailedPrecondition, ReasonReferenceMissing},
		{"check_violation", "23514", connect.CodeInvalidArgument, ReasonConstraintViolated},
		{"not_null_violation", "23502", connect.CodeInvalidArgument, ReasonRequiredFieldMissing},
		{"invalid_text_representation", "22P02", connect.CodeInvalidArgument, ReasonInvalidFormat},
		// A class-23 code we do NOT special-case stays Internal (safe default)
		// — and still carries a reason, because the vocabulary is total.
		{"exclusion_violation_falls_through", "23P01", connect.CodeInternal, ReasonInternal},
		// A non-constraint code stays Internal.
		{"serialization_failure_falls_through", "40001", connect.CodeInternal, ReasonInternal},
	}

	// Each SQLSTATE is exercised through both drivers, wrapped the way the
	// repo wraps it (fmt.Errorf("...: %w", driverErr)), so errors.As has to
	// unwrap to find the *PgError / *pq.Error.
	drivers := []struct {
		name string
		mk   func(code string) error
	}{
		{"pgx", func(code string) error {
			return &pgconn.PgError{
				Code:           code,
				Message:        `duplicate key value violates unique constraint "gadgets_sku_key"`,
				ConstraintName: "gadgets_sku_key",
				Detail:         "Key (sku)=(abc) already exists.",
			}
		}},
		{"pq", func(code string) error {
			return &pq.Error{
				Code:       pq.ErrorCode(code),
				Message:    `duplicate key value violates unique constraint "gadgets_sku_key"`,
				Constraint: "gadgets_sku_key",
				Detail:     "Key (sku)=(abc) already exists.",
			}
		}},
	}

	for _, drv := range drivers {
		for _, tc := range cases {
			t.Run(drv.name+"/"+tc.name, func(t *testing.T) {
				repoErr := fmt.Errorf("create gadgets: %w", drv.mk(tc.sqlstate))
				err := mapRepoErr("create", "gadget", repoErr)

				cerr := new(connect.Error)
				if !errors.As(err, &cerr) {
					t.Fatalf("want a connect.Error, got %T: %v", err, err)
				}
				if cerr.Code() != tc.want {
					t.Fatalf("SQLSTATE %s → code %v, want %v", tc.sqlstate, cerr.Code(), tc.want)
				}
				// The machine-readable reason is the half of the contract the
				// frontend actually routes on (ConnectClientError.reason).
				if got := cerr.Meta().Get(svcerr.ReasonHeader); got != tc.wantReason {
					t.Fatalf("SQLSTATE %s → %s %q, want %q", tc.sqlstate, svcerr.ReasonHeader, got, tc.wantReason)
				}
				// No driver PROSE, SQLSTATE, or key values may reach the
				// client message. The constraint NAME is the one thing that
				// does — the application authored it, and it is what tells a
				// person which field to fix (see pgFailure.identity, and
				// constraint_identity_test.go for that half of the contract).
				msg := cerr.Message()
				for _, leak := range []string{tc.sqlstate, "Key (sku)", "duplicate key", "violates unique constraint", "sku)=(abc"} {
					if strings.Contains(msg, leak) {
						t.Fatalf("client message leaks driver text %q: %q", leak, msg)
					}
				}
				// The entity name should be present for log/UX readability.
				if !strings.Contains(msg, "gadget") {
					t.Errorf("client message should name the entity: %q", msg)
				}
			})
		}
	}
}

// TestSQLState_NonPostgresError confirms pgFailureOf reports ok=false for a
// plain error so mapRepoErr falls through to Internal (the pre-fix default
// for anything without a SQLSTATE — e.g. a dropped connection).
func TestSQLState_NonPostgresError(t *testing.T) {
	if _, ok := pgFailureOf(errors.New("connection refused")); ok {
		t.Fatal("a non-postgres error must not report a SQLSTATE")
	}
	err := mapRepoErr("create", "gadget", errors.New("connection refused"))
	cerr := new(connect.Error)
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeInternal {
		t.Fatalf("plain error should map to Internal, got %v", err)
	}
	if got := cerr.Meta().Get(svcerr.ReasonHeader); got != ReasonInternal {
		t.Fatalf("unclassified error %s = %q, want %q", svcerr.ReasonHeader, got, ReasonInternal)
	}
}

// TestMapRepoErr_ForeignKeyReasonSplitsOnOperation pins the one place the
// reason vocabulary is finer than the SQLSTATE. Postgres reports both
// "the record you referenced does not exist" and "this record is still
// referenced" as 23503, but the caller's remedy differs completely, so the
// operation disambiguates: a delete can only trip 23503 because something
// still points at the target; any other op can only trip it because the
// target does not exist.
func TestMapRepoErr_ForeignKeyReasonSplitsOnOperation(t *testing.T) {
	fkErr := fmt.Errorf("repo op: %w", &pgconn.PgError{
		Code:           "23503",
		Message:        `violates foreign key constraint "gadgets_owner_id_fkey"`,
		ConstraintName: "gadgets_owner_id_fkey",
	})
	for _, tc := range []struct {
		op         string
		wantReason string
	}{
		{"create", ReasonReferenceMissing},
		{"update", ReasonReferenceMissing},
		{"delete", ReasonReferenceInUse},
	} {
		t.Run(tc.op, func(t *testing.T) {
			cerr := new(connect.Error)
			if !errors.As(mapRepoErr(tc.op, "gadget", fkErr), &cerr) {
				t.Fatalf("want a connect.Error")
			}
			if cerr.Code() != connect.CodeFailedPrecondition {
				t.Fatalf("code = %v, want FailedPrecondition", cerr.Code())
			}
			if got := cerr.Meta().Get(svcerr.ReasonHeader); got != tc.wantReason {
				t.Fatalf("op %q → %s %q, want %q", tc.op, svcerr.ReasonHeader, got, tc.wantReason)
			}
		})
	}
}

// TestMapRepoErr_AppReasonSurvivesPack pins that mapPackErr does not stamp
// over an application's own classification. A Pack that returned its own
// connect.Error with a reason knows more than this package's "internal"
// bucket; an unreasoned one still gets stamped, so the vocabulary stays
// total either way.
func TestMapPackErr_ReasonHandling(t *testing.T) {
	appErr := connect.NewError(connect.CodeFailedPrecondition, errors.New("wallet drained"))
	appErr.Meta().Set(svcerr.ReasonHeader, "insufficient_balance")
	cerr := new(connect.Error)
	if !errors.As(mapPackErr(appErr), &cerr) {
		t.Fatalf("want a connect.Error")
	}
	if got := cerr.Meta().Get(svcerr.ReasonHeader); got != "insufficient_balance" {
		t.Fatalf("app reason clobbered: %s = %q", svcerr.ReasonHeader, got)
	}

	if !errors.As(mapPackErr(errors.New("corrupt enum value")), &cerr) {
		t.Fatalf("want a connect.Error")
	}
	if got := cerr.Meta().Get(svcerr.ReasonHeader); got != ReasonInternal {
		t.Fatalf("unreasoned pack failure %s = %q, want %q", svcerr.ReasonHeader, got, ReasonInternal)
	}
}
