// Package seeddata materializes deterministic, FK-coherent demo data
// directly into a development database — at RUNTIME, never as a persisted
// seed file in the user's project.
//
// # Why runtime, not a file
//
// The vertical-scaffolding law forbids forge from writing new forge-owned
// (regenerated) files into the user's project space. An earlier design
// persisted db/seeds/gen/*.sql; that is disqualified. Instead this package
// introspects the APPLIED schema (schemadef.ApplyAndIntrospect against the
// real ephemeral-postgres shadow, including the real introspected foreign
// keys), synthesizes deterministic per-cell values, builds a foreign-key
// topological INSERT order, and executes the INSERTs against the target dev
// database. No .sql seed file touches the user project.
//
// # Who may call this, and what keeps it out of production
//
// This package SHIPS. It lives in forge/pkg so that a project's tests can
// seed a foreign-key spine by calling SeedGraph — an ordinary Go function
// against the test's own database — rather than shelling the forge binary or
// importing INSERT SQL frozen at the last `forge generate`.
//
// It used to live under internal/, where the module boundary made it
// unreachable from any generated app, and a flow test that needed a seeded
// spine got a GENERATED file of pre-rendered INSERTs instead: SQL that was
// correct only until the next migration, and that grew a spec per table. The
// unreachability was real but it was not the thing keeping seeds out of
// production, and paying for it in frozen generated SQL was the wrong trade.
//
// What keeps seeds out of production is that nothing in a production system
// calls a seeder. The migrate path — the one component that runs against
// non-dev databases — has no seed code path at all, which is asserted, not
// assumed (TestGeneratedMigrateTemplateHasNoSeedPath). The
// `forge db seed apply`/`reset` CLI additionally refuses any non-dev
// environment with no override flag (see internal/cli). A server binary
// seeds nothing for the same reason it sends no mail: no call site.
//
// Nothing here runs at application startup, and nothing here is wired into a
// generated server. Every entry point takes the database it should act on as
// an argument, so the blast radius of a call is exactly the handle passed in.
//
// # Planning and applying
//
// A PLAN is pure computation: given the schema model and a Config, it decides
// every cell of every row, and it touches nothing. APPLYING executes that
// plan's INSERTs against a database. They are separate functions with
// separate inputs — BuildPlan/BuildPlanFromDB/BuildLivePlan produce a *Plan,
// Apply/Reset consume one — and a caller that only wants to look uses
// Plan.Render or Plan.Statements and never opens a transaction.
//
// They are not separate PACKAGES, because the boundary they would draw is not
// the one that matters. Apply is a loop over the plan's own table order,
// reading the statement each table plan renders and the diamond refusal the
// plan computed; splitting it out would mean exporting that internal
// structure purely to hand it across a directory line, turning a private
// detail into a public API to express a distinction the function signatures
// already express. The rule is worth more than the directory: PLANNING NEVER
// TOUCHES A DATABASE, APPLYING NEVER BOOTS ONE.
//
// Where the schema comes from is the other axis, and it is the one that
// decides which entry a caller wants:
//
//   - BuildPlan — caller already has the []schemadef.Table.
//   - BuildPlanFromDB — caller has a MIGRATED database; the schema is read
//     from it. No shadow, no migrations directory. This is what tests use.
//   - BuildLivePlan — caller has migration FILES; they are applied to a shadow
//     server (whose address is a parameter, not something this package
//     resolves) and introspected. This is what the CLI uses.
//
// # Determinism
//
// Every cell value is a pure function of (salt, table, column, rowIndex)
// plus the column's DECLARED type and constraints — stateless per cell, no
// sequential PRNG. The DECLARED type is not a detail of that list: postgres
// parses it before it evaluates anything else, so a uuid column gets a real
// (deterministic, self-evidently invented) uuid and a timestamp key member
// gets a timestamp, rather than the one shape a key used to be assumed to
// have. Consequences: identical (schema, config) yields
// byte-identical INSERTs; adding a column never reshuffles other columns'
// values (each column's value depends only on its own name); adding rows
// never changes existing rows. Foreign-key values are derived from the
// referenced table's own primary-key function, so every reference resolves
// by construction.
//
// # Constraints that span two columns
//
// Determinism above is per-CELL, so a rule ABOUT TWO CELLS OF ONE ROW is
// invisible to it: drawing each column independently makes such a rule true
// only by coincidence, and postgres then aborts the whole seed. Two shapes
// cover almost every one an application schema actually declares, and each
// gets a pass that PLACES it rather than hoping:
//
//   - an ORDERING CHECK (`expires_at > issued_at`) ranks the columns and steps
//     their values apart — see ordering.go;
//   - a DISCRIMINATED UNION (`(kind = 'a' AND x IS NOT NULL AND y IS NULL) OR
//     (kind = 'b' AND …)`) picks one branch of the OR and satisfies it whole,
//     so a row's columns all come from the same branch — see union.go.
//
// Both are narrow matchers over the constraint text postgres hands back, and
// both bail LOUDLY — naming the constraint and the reason — on the expressions
// they cannot read, rather than writing rows that contradict a rule the schema
// states.
//
// A COMPOSITE UNIQUE index (`UNIQUE (org_id, user_id)`) is the third such
// rule, and the one that is not a CHECK at all. It says nothing about either
// column — each may, and must, repeat — only that the PAIR may not, so
// per-column distinctness is neither necessary nor sufficient. tuple.go deals
// the members out as an ODOMETER over the distinct values each can supply
// (a reference supplies its parent's rows; a data column supplies what the
// one-column UNIQUE machinery would give it), which makes the tuple distinct
// by construction, caps the table when the supplies run short, and refuses in
// the same wording as the two above when no member supplies anything.
//
// # Two paths to one parent
//
// The other place per-cell determinism goes blind is a diamond: when a table
// references a parent both directly and through
// another parent (orders.patient_id, and orders.prescription_id ->
// prescriptions.patient_id), every edge is individually valid and the two
// routes still name different rows. forge detects that from the foreign-key
// graph and REFUSES to seed until the project declares which route is
// authoritative, as a COMMENT on the foreign-key constraint. See diamond.go —
// it carries the reasoning, the vocabulary, and why the declaration lives on
// the constraint rather than in a forge-owned file.
//
// # Domain vocabulary
//
// Built-in synthesis invents values that are OBVIOUSLY invented: a string
// column nothing describes gets SyntheticStringPrefix + its own name + the row
// number. A project teaches the seeder its own vocabulary through the
// user-owned db/seeds/vocab.yaml overlay (see Vocab/LoadVocab/ApplyVocab):
// per-column value pools, validated against the introspected constraints,
// drawn with the same deterministic hash-pick. forge owns the seeding
// MECHANICS; the overlay supplies the domain WORDS.
//
// The overlay is not a nicety, it is the whole declaration surface, so it has
// to be findable: the scaffolded db/seeds/vocab.yaml says so in its header and
// the seeding skill says so in its second paragraph. A database full of
// `sample_*` is forge reporting that nobody told it anything, not forge
// failing.
//
// A pool of ONE is how a project pins an exact value: `users.email:
// ["known@example.com"]` puts that address in every row, and a `uuid`-typed
// column's pin is checked against the canonical UUID spelling before the seed
// runs (see vocabValueProblem) rather than aborting the transaction at INSERT.
// That is the whole of what the overlay decides — every value forge would
// otherwise have invented.
//
// # Semantic types
//
// An entry may also declare what KIND of value a column holds, instead of
// listing values:
//
//	columns:
//	  orders.customer_email:   {type: email}
//	  orders.shipping_country: {type: country_code}
//	  products.sku:            {type: uuid}
//
// The values come from gofakeit (see vocabtype.go), expanded into an ordinary
// pool at load, so everything downstream — validation against the column's
// constraints, the deterministic per-column draw, the UNIQUE assignment —
// treats them exactly like a hand-written list. VocabTypeNames() is the
// supported set, derived from the same table the resolver uses.
//
// This exists because constraint-derivation has a floor. forge satisfies the
// constraints it can INVERT — lengths, IN-lists, tractable patterns — but a
// regex postgres accepts that Go's RE2 cannot compile has nothing to invert,
// and `char_length(country) = 2` is honestly satisfied by "00" even though the
// column means an ISO code. Neither gap is closed by guessing: a wrong fixture
// that happens to pass is how a bad value ships silently. What closes them is
// the author declaring a type, and — for the generated CRUD lifecycle test —
// codegen's generate-time guard, which asks postgres to evaluate each fixture
// against the applied CHECK and REFUSES to emit a file its own migration would
// reject, naming the column, the constraint, and the type to declare.
//
// # What the overlay cannot say, and what stands in for it
//
// KEYS are not overridable. ApplyVocab refuses an entry on a primary-key or
// foreign-key column, because referential coherence is the one thing the
// seeder guarantees by construction and a hand-written key would be a
// reference forge cannot prove resolves. Two consequences worth knowing before
// reaching for the overlay:
//
//   - A project cannot DECLARE the id of a particular row — not its dev
//     principal's, not any other's. Ids come from the key function
//     (deterministicUUID), which is a pure function of (salt, table, row).
//   - A project cannot point one row at another BY NAME. There is no way to
//     write "this order belongs to that user" in vocab.yaml, because both
//     halves are keys.
//
// What stands in for the second is the row-0 spine: every non-self foreign key
// on row 0 resolves to its parent's row 0 (see fkParentRowIndependent), so one
// fully-connected dataset always exists — row 0 of every table belongs to row 0
// of every table it references. Plan.SeedValue(table, column, row) reads any of
// those values back out, which is how a caller obtains the id it could not
// name. That is a discovery mechanism, not a declaration one, and the gap is
// real: a value that has to exist BEFORE the seed runs — a principal id baked
// into an issued token, say — has nowhere in this overlay to come from.
//
// # What the seeder will not infer
//
// No IDENTIFIER says anything to the seeder — not a table's name, not a
// column's. An identifier is evidence of what someone called a thing, never
// of what the thing means, and a generator that reads one is right for the
// vocabulary it was written against and silently wrong everywhere else.
// Which row is an authenticated principal, which entity is "the user", what
// a `last4` column holds, which currency an app trades in, what a JSON
// document contains — all of it is domain knowledge the project declares (in
// the overlay, in a constraint COMMENT, or in db/seeds/custom/).
//
// The rule that decides every case: DERIVABLE → DERIVE, DECISION → DECLARE.
// A column's type, its CHECK vocabulary, its pattern, its length and range
// bounds, its NOT NULL and its foreign keys are all derivable, and synthesis
// is built from every one of them. What noun belongs in the column is a
// decision, it is not in the schema, and forge does not invent it. See
// synth.go for what that costs and what it bought.
package seedplan
