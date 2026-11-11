# Storage browser

`/forge/p/<project>/storage` follows the console's environment switcher. Its
validated search keys are `env`, `bucket`, `prefix`, `object`, and `token`.
`object` is the full key, not a basename. Prefixes and keys are not path-normalized;
spaces, repeated slashes, percent signs, and Unicode survive links unchanged.
Changing bucket or prefix clears the opaque page token and selected object.

The dev-only `/forge-storage-preview` route mounts the real browser in the console
chrome against `__fixtures__/fixtureStorageApi.ts`. `env=prod` exercises audited
access; `case=deny|denied|empty|buckets-down|shape|rate|loading|slow` selects other states. `dark=1` and
`scheme=pure-black` select appearance. Downloads in this harness only increment a
counter; they never navigate to the fixture URL.

## Source and verification

The public clients are regenerated with `task contract:controlplane:sync` from
control-plane **0d897467b06f718c4b5e61af6096106e6e000847**, the actual merge
commit of CP #2150 (2026-11-08 20:25:32 UTC), not its reviewed branch head.
`task contract:controlplane:check` reproduces that export and verifies main
ancestry. The exporter uses CP's public API allowlist only; no private
implementation is copied. The additive Config, Identity and Data contracts in
that source remain intact.

A real Chromium fixture check observed internal wide-table scrolling, no page
overflow and 44px interactive targets at 320px and 390px. Literal pixels are
deliberate: the compact console's 15px rem makes `min-h-11` only 41.25px.
The attempted new browser harness was removed after repeated live-Vite runs
proved unreliable; it is not a passing automated gate. Deterministic component
tests cover empty/denied/audited states and a deferred-API loading-to-empty
transition. These are client proofs, not evidence of live provider reads or
server grant enforcement.

Server design and release-owner acceptance/recovery remain in CP's
`docs/storage-browser.md` and `docs/storage-browser-rollout.md` at that source.
No deployment, reader IAM, production bucket enablement or grants are changed
by this UI. Production functionality is not claimed before an authorized release
and its live acceptance checks.

### Public release-owner acceptance and recovery plan

These steps require a separately approved non-customer test organization,
environment, synthetic bucket/objects and tester identities. This plan grants no
access. Record release digest, SOURCE, timestamps, request/audit IDs and sanitized
outcomes only; never paste object contents, signed URLs, tokens or credentials.

1. After the pipeline deploys a coherent compatible release, the release owner
   runs its observability health check. A green health check is not a storage
   authorization or content-flow proof; unknown/insufficient data is not passed.
2. With a user granted only the intended `storage:read`, select the approved
   hosted environment. Confirm listing, metadata, literal text/JSON and allowed
   raster preview. Explicitly download a synthetic object; correlate the audit
   record for URL issuance (not completion of the subsequent transfer). No URL
   is logged, copied to telemetry or retained as evidence.
3. In audited policy, cancel before reading: no content request. Supply a reason,
   correlate actor/environment/reason/outcome in audit and first bucket Activity.
   In the same tab switch A→logout→B and directly A→B: B sees no A content, must
   confirm independently and generates a fresh bucket-open Activity. Delayed A
   replies/download issuance must not appear or navigate after the switch.
4. Repeat as an ordinary member without the grant, after revoking a test grant,
   with deny/unknown policy, another test organization/environment and a
   direct-provider/unready bucket. Every fresh request must refuse without
   provider content/URL. Test only approved synthetic IDs, never customer IDs.
5. Confirm older-server Unimplemented explains unavailability without widening
   access; existing supported clients still perform their prior allowed actions.
   In an owner-approved isolated fault-injection environment, audit-write failure
   must withhold contents/URLs. If that environment is unavailable, record this
   live check as NOT EXECUTED, not passed.

Stop rollout immediately on unsafe content, cross-tenant access, missing audit,
new scope/parser refusal or reader-identity errors. The release/incident owner
contains by disabling the viewer/Storage endpoints and revoking the affected
storage grants/reader binding using authorized controls; never broaden scopes or
borrow ambient infrastructure credentials to restore the UI. Disabling the UI
alone does not block direct API requests. Already delivered contents cannot be
retracted; minted URLs remain bearer capabilities until expiry, so stopping new
issuance is not instant revocation. Preserve sanitized incident IDs, not URLs.

Prefer a compatible fix-forward/coherent release with Storage unavailable until
the same positive/negative checks pass. Only the release owner may choose a
previously verified coherent serving version before new-scope credentials have
been issued. After issuance, do not blindly roll binaries back to parsers that
reject those scopes. Keep additive contracts/schema, audit records and grant
history; no down migration, scope stripping or credential rewriting. Re-enable
only after health AND live authorization/audit acceptance are proven and approved.

### Reader lifetime

The consumer uses only a stable authenticated user ID and an opaque in-memory
epoch, never tokens/emails. Every identity change rotates the epoch, removes
legacy unscoped reasons/markers, remounts Storage state and cancels/removes its
queries. New keys include principal+epoch; late replies check the captured epoch
before returning data, marking a bucket or navigating a download. Same-user token
refresh does not rotate; explicit API-key replacement does. Reload intentionally
starts a new epoch and asks again. Null/anonymous readers cannot confirm or read.
Demo uses an auth-free fixture twin; production never imports demo identity.

## API gaps

The vendored StorageBrowserService is authoritative:

- `StorageObject` in a listing has no content type. The table explicitly says
  **Open for type**; GetObjectMetadata displays the stored type in the object pane.
  We do not guess from extensions or issue up to 200 extra audited HEAD requests
  for each listing page. A filled content-type column needs API support or an
  explicit decision to pay for those additional reads.
- The current preview contract refuses oversized text with `too_large`, rather
  than returning truncated bytes. There is no `truncated` flag. The UI defensively
  detects returned bytes shorter than `size` and shows **Cut at 1 MiB**. The fixture
  follows the current cap by default; `truncatedText: true` explicitly exercises
  that defensive state, not a promise the current server makes.
- The bucket-list request has only `environment_id`; it stops before policy
  admission and reads no contents. Confirmations and reasons are sent on all
  subsequent content reads and downloads, not on that discovery call.

## Unify when Data lands

The reference was `origin/feat/data-browser` (PR #2339), read but not imported.

- Move Data's reason dialog onto `../AuditedAccess/AccessReasonDialog` with
  `subject="data"`, its description and `testIdPrefix="data"`.
- Replace Data's session helper with `accessSession-model` using namespace `data`.
  Legacy environment-only keys are discarded, not migrated as authority. Both
  surfaces use reader+epoch-scoped keys: a reason for reading rows does not
  silently authorize a different reason for reading files.
- Keep Data and Storage adjacent in ConsoleShell, before Configure. This branch
  adds only Storage because Data's page and route have not landed here.
- Combine the overlapping stable error reasons and `ENV_IN_SENTENCE` verb sets.
  Share the already-exported StateFrame; retain surface-specific error copy.

No object key or prefix is rendered from Activity detail: bucket opens show the
reason, downloads show reason and size. Signed download URLs are handed directly
to navigation inside the mutation function; the mutation returns void so its
cache never retains the URL.
