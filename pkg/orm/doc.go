// Package orm is forge's typed data-access layer: a postgres-pinned query
// builder over uptrace/bun (bun's ? placeholders; the raw Exec/Query/QueryRow
// take postgres $1, $2), plus the schema-truth machinery that projects
// entity types out of the applied migrations.
//
// The generated internal/db/<entity>_orm.go consumes it, so most projects
// reach for this package only through generated code. What a hand-written
// caller uses directly:
//
//   - [Client]        the handle; Bun() is the raw engine seam for queries
//     the generated CRUD cannot express.
//   - [Client.RunTx]  a transaction carried in ctx, at the level the caller
//     names — [Serializable] (conflict-retried; lock-free read-check-write)
//     or [ReadCommitted] (one attempt; correctness from a lock). There is no
//     default. Every query made with the ctx it hands fn — generated
//     delegates, stores, pkg/crud, raw SQL — joins it. fn may run more than
//     once; defer side effects with [AfterCommit]. See tx.go and [TxOptions].
//   - [ErrNoRows]     the missing-row sentinel. Match it with errors.Is and
//     let pkg/svcerr map it to a clean NotFound — never leak SQL text.
//   - [IsUniqueViolation], [IsCheckViolation], [ConstraintName] the write-side
//     twins of ErrNoRows: classify a constraint violation a service wrote
//     itself into, so a lost UNIQUE race becomes AlreadyExists instead of a
//     500 carrying driver prose.
//   - <Entity>Columns the declared-column allowlist generated per entity,
//     used to validate an order_by against a closed set.
//   - [WhereILikeAny] the multi-column case-insensitive search predicate
//     behind generated search/query/q filters.
//   - [NullTime]      the tolerant nullable-timestamp scanner.
//
// Placeholders depend on the path. Everything off Bun() — the builders,
// NewRaw, bun.IDB's ExecContext — takes bun's `?`. [Client.Exec],
// [Client.Query] and [Client.QueryRow] (and their [Tx] twins) are
// database/sql: the SQL reaches postgres as written, so they take postgres's
// $1, $2, …, and `?` there is the jsonb operator. A raw statement with
// arguments written with `?` is refused with [ErrQuestionPlaceholders]
// instead of failing as a 42601 syntax error.
//
// Schema truth flows one way: db/migrations/*.up.sql is the source, and the
// entity types, ORM and CRUD wiring are PROJECTIONS of the applied schema
// (introspect.go / differ.go / migration.go). There is no entity annotation
// to declare — an entity is a table plus matching CRUD RPCs in a service
// proto.
package orm
