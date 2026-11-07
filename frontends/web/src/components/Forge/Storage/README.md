# Storage browser

`/forge/p/<project>/storage` follows the console's environment switcher. Its
validated search keys are `env`, `bucket`, `prefix`, `object`, and `token`.
`object` is the full key, not a basename. Prefixes and keys are not path-normalized;
spaces, repeated slashes, percent signs, and Unicode survive links unchanged.
Changing bucket or prefix clears the opaque page token and selected object.

The dev-only `/forge-storage-preview` route mounts the real browser in the console
chrome against `__fixtures__/fixtureStorageApi.ts`. `env=prod` exercises audited
access; `case=deny|denied|empty|shape|rate|slow` selects other states. `dark=1` and
`scheme=pure-black` select appearance. Downloads in this harness only increment a
counter; they never navigate to the fixture URL.

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
  Its existing `forge.data.access.<environmentId>` keys are preserved. Storage
  uses `forge.storage.access.<environmentId>`: a reason for reading rows does not
  silently authorize a different reason for reading files.
- Keep Data and Storage adjacent in ConsoleShell, before Configure. This branch
  adds only Storage because Data's page and route have not landed here.
- Combine the overlapping stable error reasons and `ENV_IN_SENTENCE` verb sets.
  Share the already-exported StateFrame; retain surface-specific error copy.

No object key or prefix is rendered from Activity detail: bucket opens show the
reason, downloads show reason and size. Signed download URLs are handed directly
to navigation inside the mutation function; the mutation returns void so its
cache never retains the URL.
