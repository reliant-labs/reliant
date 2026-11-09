package orm

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
)

// Constraint-violation classification for hand-written service code.
//
// # Why this is exported
//
// forge tells you to declare real constraints — a UNIQUE on jobs.estimate_id
// so one approved estimate sells exactly one job, a UNIQUE on invoices.job_id
// so one job bills once. Those constraints exist to close a RACE: the database
// is the only thing that can arbitrate two concurrent approvals, and the loser
// comes back as a driver error.
//
// pkg/crud classifies that error correctly, but only on the handler path —
// mapRepoErr runs inside the generated CRUD shim. A domain service calling the
// generated CreateJob directly, which is exactly what the service-layer skill
// prescribes for anything with business logic or a transaction, never reaches
// it and gets the raw driver error instead. Left with no predicate, the caller
// reaches for the one discriminator that appears to work:
// strings.Contains(err.Error(), "already exists") — prose-matching against
// postgres diagnostics, which is the trap forge's own api skill documents. The
// alternative is worse: return the race as a 500 with a postgres string in the
// body. It compiles, the happy path passes, and it only shows up on the second
// concurrent call.
//
// # Why here and not in pkg/svcerr
//
// svcerr is a transport-shaped taxonomy with no database in it — giving it a
// postgres driver dependency so it can answer a postgres question inverts the
// layering. orm is already the driver-facing package (it links lib/pq for
// array support), and the error being classified is one orm handed back. The
// service maps orm's verdict to its own sentinel, which is the same direction
// ErrNoRows -> svcerr.NotFound already flows.
//
// # Why three functions and not a type
//
// Exporting the whole failure struct would publish the SQLSTATE, the driver's
// prose and the offending row values — the diagnostics pkg/crud is careful to
// keep server-side. The predicates answer the question a service actually
// asks, and ConstraintName returns the one field that is safe and actionable:
// an identifier the application typed into its own migration.

// pgDiagnostic is the driver-independent view of a postgres error: the
// SQLSTATE that classifies the failure, and the name of the schema object it
// violated. Generated apps open their pool through jackc/pgx (errors surface
// as *pgconn.PgError); pkg/pgtest and any lib/pq-based caller surface
// *pq.Error. Both carry the same fields under different names.
//
// errors.As does the unwrapping, so every function here works through the
// fmt.Errorf("...: %w", err) that a service is told to wrap with. A classifier
// that only matched the bare error would stop working the moment it was used
// as documented.
func pgDiagnostic(err error) (sqlState, constraint string, ok bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code, pgErr.ConstraintName, true
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return string(pqErr.Code), pqErr.Constraint, true
	}
	return "", "", false
}

// IsUniqueViolation reports whether err is a postgres unique-constraint
// violation (SQLSTATE 23505), including one wrapped with %w.
//
// This is the predicate that turns a lost race into a clean AlreadyExists
// instead of a 500:
//
//	if err := s.db.CreateJob(ctx, job); err != nil {
//	    if orm.IsUniqueViolation(err) {
//	        return Job{}, ErrAlreadyExists
//	    }
//	    return Job{}, fmt.Errorf("create job: %w", err)
//	}
//
// On a table with more than one UNIQUE, pair it with ConstraintName — "some
// unique failed" cannot tell "this estimate already sold a job" from "that job
// number is taken", and a service that guesses is wrong half the time.
func IsUniqueViolation(err error) bool {
	state, _, ok := pgDiagnostic(err)
	return ok && state == "23505"
}

// IsCheckViolation reports whether err is a postgres CHECK-constraint
// violation (SQLSTATE 23514), including one wrapped with %w.
//
// A CHECK is a declared domain invariant — invoices_not_overpaid, say — so
// violating it is the CALLER's bad input, not a server fault: map it to an
// invalid-argument sentinel, not an internal one. It is a separate predicate
// rather than a mode on IsUniqueViolation because the two demand different
// answers from a service; collapsing them would report "already exists" for a
// payment that merely exceeded the balance.
func IsCheckViolation(err error) bool {
	state, _, ok := pgDiagnostic(err)
	return ok && state == "23514"
}

// ConstraintName returns the name of the schema object err violated, or "" if
// err is not a postgres error or postgres named none.
//
// The name is safe to act on and safe to surface: it is an identifier the
// application wrote in its own migration, and postgres reports it as a
// structured field rather than as text inside the message. That is the
// distinction pkg/crud draws too — the constraint name crosses the wire, the
// driver's prose and the offending values do not.
//
// Compare it against the GENERATED constant, never a literal: forge projects
// the applied schema's UNIQUE / CHECK / FOREIGN KEY names to
// db.<Entity>Constraint<Name>, beside the column constants. That is what makes
// renaming a constraint in a migration a compile error at every reader rather
// than a branch that silently stops matching.
//
// A NOT NULL violation has no constraint to name (postgres reports the column
// instead), so this returns "" for one.
func ConstraintName(err error) string {
	_, constraint, _ := pgDiagnostic(err)
	return constraint
}
