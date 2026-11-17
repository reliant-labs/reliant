// Package dbbrowse is the data layer of a read-only table browser: it
// describes what a database role can read, and compiles a structured browse
// request (a table, columns, filters, a sort, a page) into one parameterised
// SELECT that [Run] executes through [dbquery.Run].
//
// It is what a Supabase-style "Data" tab needs and nothing more: no free SQL
// reaches the database on this path, and nothing here writes.
//
// # The security argument
//
// Three independent layers, each sufficient for what it covers:
//
//  1. The role. The caller connects as the environment's read-only role
//     (forge_ro, see pkg/dbgrants). Postgres refuses every column that role
//     was not granted, a forge:secret column included, whatever SQL reaches
//     it. [Describe] asks the server what that role may read
//     (has_schema_privilege, has_column_privilege) rather than trusting a
//     declaration, so a browser never offers a column the database would
//     refuse: an ungranted column is simply absent from the description.
//  2. The identifier allow-list. Every table and column a [Request] names is
//     looked up in that description, by exact name, and refused with a typed
//     error when it is not there. Only names that came out of the catalog
//     are ever written into SQL, and each is quoted (pgx.Identifier). A
//     request's own spelling of a name never reaches the statement, so an
//     identifier cannot carry SQL.
//  3. Binding. Every filter value is a $n parameter sent over the extended
//     protocol; none is spliced into the statement. Operators come from a
//     closed set ([Op]), and the limit and offset are integers the compiler
//     checked. The compiled statement then runs through dbquery: a READ ONLY
//     transaction with statement, lock and idle timeouts, row and byte caps,
//     and dbquery's lexical guard and [dbquery.Auditor].
//
// # How a value is typed
//
// A value is bound as text with no declared type, and Postgres types it from
// the column it is compared with, exactly as it types a quoted literal:
// `"id" = $1` reads $1 as a uuid when id is one, a timestamptz as a
// timestamptz, an enum label as that enum, '{a,b}' as the column's array
// type, a jsonb document as jsonb. The compiler never spells a type name, so
// no catalog text other than a quoted identifier is ever written into SQL,
// and domains, enums and types outside the search_path need no special case.
// A value the type cannot read is the server's invalid-input error, not a
// different statement.
//
// Two operations do not use the column's type. LIKE and ILIKE compare the
// column's text form ("col"::text), which every type has. And a type with no
// default btree operator class (json, xml, the geometric types: see
// [Column.Sortable]) has no ordering and often no equality, so eq, neq and in
// compare its text form too, and an ordering operator or a sort on it is
// refused.
//
// # Why not pkg/schemadef
//
// schemadef introspects the full applied schema as its owner, for codegen:
// over database/sql, keyed by unqualified table name, with one catalog round
// trip per table. A browser needs the opposite cut: only what one role may
// read, every schema at once keyed by qualified name, and the incoming half
// of every foreign key. Two focused pg_catalog queries do that in two round
// trips, with the privilege checks inside the query, so a column the role
// cannot read never leaves the server.
package dbbrowse
