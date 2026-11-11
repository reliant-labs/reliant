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

## Live acceptance before feature promotion

This is a runbook, not a claim that a live backend was tested. Record the tested
frontend/backend release identifiers and a pass/fail verdict in the authorized
release record. Do not attach HAR files, credentials, tokens, actual subject IDs,
emails or database row contents to a public PR. Unit tests and the canonical
proto export check do not establish that the running backend serves GetUser.

Use a non-production managed-identity environment you are authorized to inspect,
a known synthetic human user already provisioned in that environment, and an
operator belonging to the owning org. Do not create users, modify grants, add
schema markers or query another database merely to make this check pass. Obtain
the synthetic subject from the existing Identity user list; do not enumerate IDs.
GetIdentityUserRequest.user_id is 1–128 characters; the email search bound is 200.

1. Open that project's environment page, select the **Identity** tab, and select
   **Open identity** for the known synthetic user. The actual route is
   `/forge/p/<project>/environments/<env>?tab=identity&user=<subject>`; use the
   rendered link rather than constructing an API host or authorization header.
2. In browser DevTools → Network, inspect
   `controlplane.v1.IdentityService/GetUser`. The frontend calls the generated
   Connect client through `getIdentityUser(orgId, environmentId, userId)` in
   `services/controlPlane/identity/api.ts`. Verify the request's `orgId`,
   `environmentId` and `userId` are exactly the selected org, the environment's
   control-plane ID and the known synthetic subject—not the identity owner's ID
   or a previously selected environment. Preserve existing browser credentials;
   never copy them into a shell command or public evidence.
3. Require a successful response whose `user.userId` equals the requested subject
   exactly and whose synthetic profile is expected. Require the selected panel
   to show that same identifier. A 200 response for another identifier is a FAIL;
   the UI must not select it. Do not accept a substring email match as evidence.
4. With Network recording, open a single deliberately nonexistent synthetic
   subject (not a scan), then repeat the known synthetic link using a test
   operator without membership in that org. Confirm the backend refuses access
   or reports the missing user without displaying a cached selected identity.
   No fallback ListUsers search, automatic pagination, database Describe/Browse
   request, count or cross-database probe may result from either GetUser failure.
   Ordinary Identity list reads already made by the tab are not a fallback scan.
   A denial scenario needs the unauthorized test operator; do not revoke a real
   user's membership or grant privileges to stage it.
5. If a role-visible **explicitly marked** subject column already exists in this
   test app, open its complete synthetic cell using Data's normal audited reason
   flow and follow **Open in Identity**. Recheck the same org/environment/subject
   binding. Null/empty/hidden/truncated values must have no link. If no such
   column exists, record this row-origin case as NOT RUN, not passed; direct
   Identity GetUser verification above still runs without inspecting app rows.
6. **App data** must first choose/describe only the selected authorized database
   through the existing reason confirmation. Discovery emits no BrowseTable or
   count reads. Opening one direct link sends an EQ value through ordinary
   db:query admission; a refusal stays a refusal. Switching environments must
   not apply the original subject context in the other environment.

Promotion requires the positive scoped GetUser check and denial/identifier
binding checks to pass. If the endpoint is absent, unavailable, denied for the
intended authorized operator, or returns the wrong identity: stop feature
promotion, preserve the failure category privately, contain the optional links,
and fix forward. Do not guess another org/environment, use wider credentials,
scan emails/IDs or treat an older backend as verified.

## Containment and rollback boundary

The owned build-time seam is `VITE_IDENTITY_DATA_LINKS=disabled` (exact spelling).
It hides row/grid **Open in Identity**, Identity **Open identity**/**App data**,
and the optional selected-identity lookup; old `?user=` links trigger no GetUser
or fallback email lookup. An old Data `subject` URL offers no subject links in
this build. Direct Identity administration/listing and ordinary Data admission
remain available. Absence of this variable preserves default-on behavior. This
is not a runtime config toggle: an environment variable change alone does not
alter an already shipped bundle.

For a local containment proof, the repository's actual frontend build command is:

```sh
cd frontends/web
VITE_IDENTITY_DATA_LINKS=disabled npm run build
```

Use the existing reviewed release process to build/promote that contained bundle;
this runbook does not authorize deployment. Verify no optional links and no
GetUser request from an existing selected-identity URL in the rebuilt bundle.
Alternatively keep the last known-good frontend release while stopping promotion;
this does not roll back SQL, grants, or the additive server/proto contract. Do not
remove generated RPCs or change backend authorization as containment. Fix forward
the failing backend/client binding, repeat the authorized acceptance above, then
release a bundle with the variable absent. Older servers with unspecified column
metadata continue to produce no row links; lookup errors never become scans.
