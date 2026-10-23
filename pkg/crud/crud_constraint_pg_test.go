package crud

import (
	"context"
	"errors"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// These tests trigger REAL postgres integrity-constraint violations through
// the same bun+driver path a generated app uses, then assert mapRepoErr
// classifies each SQLSTATE instead of collapsing it to Internal. They boot
// embedded postgres, so they're skipped under -short.

// setupConstraintSchema creates an owners table and a gadgets table carrying
// one of every class-23 constraint the mapping handles.
func setupConstraintSchema(t *testing.T, db orm.Context) {
	t.Helper()
	ctx := context.Background()
	stmts := []string{
		`CREATE TABLE owners (id TEXT PRIMARY KEY)`,
		`CREATE TABLE gadgets (
			id       TEXT PRIMARY KEY,
			sku      TEXT NOT NULL UNIQUE,
			category TEXT NOT NULL CHECK (category IN ('a', 'b')),
			owner_id TEXT REFERENCES owners(id)
		)`,
		`INSERT INTO owners (id) VALUES ('owner-1')`,
		`INSERT INTO gadgets (id, sku, category, owner_id) VALUES ('g1', 'SKU-1', 'a', 'owner-1')`,
	}
	for _, s := range stmts {
		if _, err := db.Bun().ExecContext(ctx, s); err != nil {
			t.Fatalf("setup stmt failed: %v\n%s", err, s)
		}
	}
}

func TestMapRepoErr_RealPostgresConstraints(t *testing.T) {
	if testing.Short() {
		t.Skip("boots embedded postgres; skipped under -short")
	}
	db := newRepoTestDB(t)
	setupConstraintSchema(t, db)
	ctx := context.Background()

	cases := []struct {
		name       string
		op         string
		stmt       string
		want       connect.Code
		wantReason string
	}{
		{
			name:       "unique_violation → AlreadyExists/duplicate",
			op:         "create",
			stmt:       `INSERT INTO gadgets (id, sku, category) VALUES ('g2', 'SKU-1', 'a')`,
			want:       connect.CodeAlreadyExists,
			wantReason: ReasonDuplicate,
		},
		{
			name:       "foreign_key_violation → FailedPrecondition/reference_missing",
			op:         "create",
			stmt:       `INSERT INTO gadgets (id, sku, category, owner_id) VALUES ('g3', 'SKU-3', 'a', 'ghost')`,
			want:       connect.CodeFailedPrecondition,
			wantReason: ReasonReferenceMissing,
		},
		{
			name:       "check_violation → InvalidArgument/constraint_violated",
			op:         "create",
			stmt:       `INSERT INTO gadgets (id, sku, category) VALUES ('g4', 'SKU-4', 'z')`,
			want:       connect.CodeInvalidArgument,
			wantReason: ReasonConstraintViolated,
		},
		{
			name:       "not_null_violation → InvalidArgument/required_field_missing",
			op:         "create",
			stmt:       `INSERT INTO gadgets (id, category) VALUES ('g5', 'a')`,
			want:       connect.CodeInvalidArgument,
			wantReason: ReasonRequiredFieldMissing,
		},
		{
			name:       "fk_still_in_use_on_delete → FailedPrecondition/reference_in_use",
			op:         "delete",
			stmt:       `DELETE FROM owners WHERE id = 'owner-1'`, // g1 still references it
			want:       connect.CodeFailedPrecondition,
			wantReason: ReasonReferenceInUse,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, execErr := db.Bun().ExecContext(ctx, tc.stmt)
			if execErr == nil {
				t.Fatalf("expected a constraint violation, statement succeeded: %s", tc.stmt)
			}
			// The repo wraps driver errors as fmt.Errorf("... %w", err); mirror
			// that so the test exercises the same errors.As unwrap the real
			// generated code relies on.
			mapped := mapRepoErr(tc.op, "gadget", wrapDriver(execErr))
			cerr := new(connect.Error)
			if !errors.As(mapped, &cerr) {
				t.Fatalf("want connect.Error, got %T: %v", mapped, mapped)
			}
			if cerr.Code() != tc.want {
				t.Fatalf("%s: got code %v, want %v (driver err: %v)", tc.name, cerr.Code(), tc.want, execErr)
			}
			// The reason is what the frontend routes on; a real postgres
			// error must produce it, not just a synthesized one.
			if got := cerr.Meta().Get(svcerr.ReasonHeader); got != tc.wantReason {
				t.Fatalf("%s: %s = %q, want %q (driver err: %v)",
					tc.name, svcerr.ReasonHeader, got, tc.wantReason, execErr)
			}
		})
	}
}

// wrapDriver mirrors how crud.Repo wraps driver errors before they reach
// mapRepoErr (fmt.Errorf("...: %w", err)) so errors.As has to unwrap.
func wrapDriver(err error) error {
	return errWrap{err}
}

type errWrap struct{ err error }

func (e errWrap) Error() string { return "repo op failed: " + e.err.Error() }
func (e errWrap) Unwrap() error { return e.err }
