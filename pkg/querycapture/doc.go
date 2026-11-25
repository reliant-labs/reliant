// Package querycapture records the shape of every SQL statement a process
// runs, and who ran it, so the statements can be explained later
// (`forge db analyze queries`). It is off unless FORGE_QUERY_CAPTURE_DIR is
// set:
//
//	FORGE_QUERY_CAPTURE_DIR=/tmp/qc task test:short
//
// # What is recorded
//
// Each process appends to one file of its own, <pid>-<random>.jsonl in that
// directory, opened O_APPEND on its first record, one JSON [Record] per line.
// A record is written the first time the process runs a statement of a given
// shape (its fingerprint) from a given caller (file and line); repeats are
// deduplicated in memory, so a file holds one line per distinct pair, not one
// per execution. Only statements EXPLAIN can plan are recorded
// ([Explainable]): a test's CREATE TABLE, a SET or a BEGIN is not a query
// shape. [Read] merges every file of a directory into [Capture]: one
// [Shape] per fingerprint, with every [CallSite] that ran it.
//
// Capture never fails the application's query. The first error (a directory
// that cannot be created, a full disk) is printed to stderr once and capture
// stops for the rest of the process.
//
// # The SQL, and why literals stay
//
// The SQL recorded is the text sent to Postgres, normalized ([Normalize]):
// comments dropped, whitespace collapsed, `IN (…)` lists of constants and the
// rows of `VALUES (…), (…)` cut to their first element, and $n placeholders
// renumbered densely. The result can be explained with
// `EXPLAIN (GENERIC_PLAN)`, which plans a statement with unknown parameters.
//
// Under pkg/orm the text is the BOUND statement: forge sends strings, times,
// bytes, JSON, model column values and Arg values as $n parameters
// (pkg/orm/bind.go, pkg/crud/bind.go), so those arrive as placeholders.
// What forge leaves in the text stays a literal: NULL, TRUE/FALSE, integers
// passed to a `?` (LIMIT 10, IN (1, 2)), text written into the SQL, and every
// value of a statement run with orm.InlineValues or through db.Bun()'s raw
// ExecContext/QueryContext/QueryRowContext (which bun formats inline).
//
// Literals are deliberately not turned into placeholders. The planner uses a
// literal at plan time: `WHERE status = 'open'` can use a partial index
// `WHERE status = 'open'`, and `WHERE status = $1` cannot. Replacing it would
// make the plan Phase 3 explains a different plan from the one the
// application gets. The fingerprint, by contrast, masks every string and
// numeric literal, so the same statement run with many inline values is one
// shape (and one record per caller), whose SQL is the first one seen.
//
// # The caller
//
// The caller is the first stack frame outside the database stack: the Go
// runtime, database/sql, bun, pgx, otelsql/OpenTelemetry, and forge's own
// pkg/orm, pkg/crud, pkg/observe/dbtrace and this package (their _test
// packages are callers). Generated code (a file named *_gen.go, such as a
// generated store method) is skipped too, and the first generated frame is
// kept as [Record].Generated: the caller is the hand-written code above it —
// the repo extension, handler or test that asked. When nothing but generated
// code is above the database stack, the generated frame is the caller.
//
// # Coverage, and the gap
//
// Captured: every statement a pkg/orm Client or Tx runs (bun query builders
// on Bun(), BunDB() and Tx.Bun(); the raw Exec/Query/QueryRow passthrough
// pkg/crud uses for by-PK statements; db.Bun()'s raw ExecContext family), and
// every statement on a native pgx connection traced with
// dbtrace.NewPgxTracer.
//
// Not captured: SQL run on a *sql.DB directly, without an orm.Client (for
// example testkit's or serverkit's pool used by hand). The only hook every
// such statement passes is the database/sql driver, which forge's pools wrap
// with otelsql; capturing there means owning a full driver.Conn wrapper and
// every optional interface pgx's connection implements, which is not worth
// the risk to every query of every app. Run such SQL through the Client's
// Exec/Query (or db.Bun()) to have it captured.
//
// Queries no test runs are invisible. That is the intended incentive.
package querycapture
