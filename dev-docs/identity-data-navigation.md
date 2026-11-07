# Explicit Identity ↔ Data navigation

The console links only columns returned by DescribeDatabase with the additive
`DatabaseTableColumn.identity_subject` enum (field 6: unspecified, id, email).
The server projects `schemadef.Column.IdentitySubject()` from role-visible
catalog comments. An old server, invalid/conflicting marker or unmarked column
produces no link. In particular, `users.external_id` is not guessed: our product
uses Supabase, not the environment's managed identity.

A complete, nonempty string subject opens Identity in the same project and
actual environment. Nulls, objects/arrays, controls, oversized values and cells
cut by the server are not identity keys. ID links use GetUser with org,
environment and user id. Email links use the existing email search: only a
complete result set with one normalized exact match selects an identity;
substring, ambiguous and incomplete matches stay a filtered list with explicit
user selection. Lookup refusals never trigger a scan or fallback.

“App data” opens Data with an environment-id-bound `subject` context. It reuses
Data's ListBrowsableDatabases/Describe admission, database choice and audited
confirmation/reason dialog. The reason stays in tab session storage, never in
the URL. One selected database is described; no databases are silently probed,
and discovery never reads rows or counts. A refusal is the existing Data error.
Direct marked columns yield EQ-filter links, at most 25 from the first 200
tables and 200 columns per table. Other databases are selected explicitly.
Opening one link uses ordinary BrowseTable/db:query admission; Related handles
descendants. There is no identity path graph, join or cross-database inference.

Route state (`user`, `q`, `subject`, database/table/filter/sort/columns/offset)
is shareable and validated. A subject context cannot be applied in another
environment. Existing environment tabs remain unchanged.
