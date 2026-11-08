# Frontend forge-equivalence checklist

This is a **verified frontend audit**, not a promise that every frontend is now
scaffold-equivalent. Backend migration is tracked separately. Keep product UI,
privacy, desktop integration and streaming semantics; replace duplicated framework
machinery only when the pinned runtime supplies its contract. A starter page is
not a replacement for product UI.

## Baseline and evidence rules

Audited fresh `origin/main` checkouts on 2026-11-08:

| Repo | Main inspected | forge pin | Web runtime manifest |
|---|---|---|---|
| reliant | `fb63f15dd` | `v1.2.1-0.20261108035756-6d8c0ab6008f` | `^0.13.8` (locked/installed 0.13.8) |
| control-plane | `41f9c70a0` | `v1.2.1-0.20261108014235-88cb28c2f5ab` | `^0.13.6` |

Paths below are repo-relative; `cp:` means control-plane. Reverify these rows after
pin updates. A checked row means the **named mechanism** was verified in source,
not that every screen, endpoint or production deployment was exercised.

Read the pinned skills before repeating the audit: `frontend`, `frontend/adopt`,
`frontend/runtime-config`, `frontend-runtime`, `frontend-runtime/transport`,
`frontend-runtime/shell`, `proto/hooks`, `frontend-testing`. Inspect the installed
package's declarations as well: `forge project libraries --json` reports the
resolved runtime, which can be stale until `task deps:web` installs the lockfile.
The published 0.13.8 package exports `installDevLogging`, typed Connect errors,
interceptors, shell/error/toast/session primitives, query/mutation factories and
mock helpers. No new forge capability is needed for the first adoption here.

**Status of earlier work:** reliant [#2363](https://github.com/reliant-labs/reliant/pull/2363)
is **merged**, not an outstanding dependency. Current `settings-queries.ts` and
`provider-account-queries.ts` already bind the generated service mutations.
Do not open another adoption/pin PR for it. Neither this audit nor the logging
adoption changes the control-plane pin operator's work.

## Current framework contracts — verified, not migration gaps

- [x] **Runtime values are runtime config.** Both frontends carry generated
  `src/lib/config_gen.ts` and `config_schema_gen.json`. Reliant's `index.html`
  loads blocking `config.js`; cp's `src/app/layout.tsx` loads it through
  `joinBasePath`. Reliant's app code uses `loadConfig()` for API/environment/OTEL
  values. `VITE_APP_VERSION` is a build identity and `VITE_MOCK_API` selects a
  build mode; neither is a per-env config defect. Generated `otel_gen.ts` has
  #1884's deprecated build fallbacks: do not mistake them for hand-written drift.
  cp's `NEXT_PUBLIC_API_URL` / `NEXT_PUBLIC_MOCK_API` property names in
  `loadConfig()` are **runtime document keys**, not `process.env` reads.
- [x] **Runtime config in dev and promoted builds.** Reliant's `vite.config.ts`
  uses published `forgeRuntimeConfig()` for `FORGE_RUNTIME_CONFIG_FILE` and has
  an explicit dev-only OTEL overlay/proxy (`vite-plugin-otel-proxy.ts`). This
  supports another repo running the frontend and disabling an unavailable dev
  collector. cp's `next.config.ts` is intentionally `output: "standalone"`:
  its server-side `src/app/api/[...path]/route.ts` proxies the operator API. It
  is not an accidentally non-static site to convert to static export.
- [x] **Generated clients and query/mutation definitions exist.** Reliant has 35
  `*-hooks_gen.ts` service modules; cp has 43. Both use forge's runtime hook
  factories. Generated queries own protojson keys and support imperative reads;
  generated mutations own their success invalidation. #2363 removed duplicate
  settings/provider clients while preserving credential-failure and cross-service
  refreshes. Those failure refreshes are product behavior, not redundant code.
- [x] **Connect composition uses framework primitives.** Reliant's
  `src/api/transport.ts` composes runtime auth, normalization and tracing; cp's
  `src/lib/connect.ts` uses `buildRuntimeInterceptors`. cp sends HttpOnly session
  cookies with `credentials: "include"`. Reliant routes multiple backends and
  integrates bearer auth; replacing this with the one-backend scaffold would
  break packaged Electron, pre-session RPCs and the public demo.
- [x] **Auth is integrated, not an absent scaffold.** Reliant's
  `src/lib/auth/{provider,supabase-provider,context}.ts[x]`, auth store and
  `components/AuthInitializer.tsx` bridge browser/desktop sessions. cp's
  `src/lib/auth/{native-login,context,route-guard}.ts[x]` uses native operator
  login and a server-validated HttpOnly cookie. `cp:src/app/providers.tsx`
  adapts that identity to `RuntimeShell`; hydration behavior has a dedicated
  `route-guard-hydration.test.tsx`. Do not introduce browser-readable cp tokens.
- [x] **Error/telemetry primitives are already adopted.** Reliant uses
  `reportException` via `src/lib/errorReporting.ts` and `initBrowserTelemetry`
  in `telemetry.ts`; cp initializes generated telemetry and mounts RuntimeShell.
  Reliant's consent gate, console-capture suppression (chat privacy), desktop
  process consent, demo shell, handled-RPC suppression and sampling are product
  policies. Do not replace them with unconditional `initTelemetry()`.
- [x] **One retry owner at the framework boundary.** cp's QueryClient owns query
  retries and mutation error toasts; its transport does not retry writes.
  Reliant's QueryClient owns read retries; `queryRetry.ts` retains its existing
  policy (including legacy non-Connect fallbacks). Imperative generated mutations
  bypass MutationCache, as intended. A blanket error toast on every Reliant
  mutation would duplicate existing product error UI; that is not a cleanup.
- [x] **Generated ownership is intentional.** Both have generated proto, config,
  otel, mocks and hook files; edit their input, never their generated body.
  Reliant's package.json `forge` block selects hooks and `components_dir`.
  Both custom `connect.ts` files are owned composition seams. The pinned
  `frontend` skill incorrectly lists `connect.ts` as regenerated while
  `frontend-runtime` and `frontend/adopt` explicitly call it owned: reported
  as forge bug `cdc57b22-6089-467c-8a00-624402fe1308`. Do not let that stale
  sentence trigger a destructive rewrite.
- [x] **Testing/package/tooling contracts are present.** pnpm lockfiles, Node
  tooling, Tailwind v4, typecheck, Vitest and stable `setTransport` seams exist.
  Reliant needs `pnpm exec tsc -b` (solution references), not root `tsc --noEmit`.
  cp uses `pnpm exec tsc --noEmit`. `src/lib/test-utils.tsx` in both frontends
  provides the transport-level seam. No claim that every test or UI primitive
  matches the newest scaffold verbatim.

## First bounded implementation in this branch

- [x] **Delete the vendored devlog client, use the published runtime.**
  `src/lib/browser-log-forward.ts` was a 518-line copy explicitly waiting for a
  batched-runtime release. Runtime 0.13.8 is already pinned/published and supplies
  its APIs, protocol-v2 batching, loss reporting, v1 negotiation, unload beacon
  and error/stack handling. `browser-log-boot.ts` now imports `installDevLogging`
  from the runtime; entrypoint ordering and the literal dev guard stay unchanged.
  No package/pin change. The copy also missed the runtime's server-trace-id
  annotation, now exercised by the adoption tests.

  Preserve `vite-plugin-browser-logs.ts`: it is the owned **receiving endpoint**,
  not a duplicate browser client. Preserve `clientDiagnostics.ts`,
  `updateDepthStack.ts` and `logger.ts`: they have separate responsibilities.

  Acceptance: installed-runtime tests in `src/lib/__tests__/browser-log-forward.test.ts`,
  `vite-plugin-browser-logs.test.ts`, logger error-detail/one-IPC-copy tests,
  diagnostics, config, routing, retry and telemetry tests; `tsc -b`.
  These test the package/endpoint contract, not a freshly copied implementation.

## Real outstanding gaps (not marked complete)

### 1. Demo still vendors published mock machinery — bounded next slice

- [ ] Replace `src/components/Forge/Demo/demoData/forgeMockCompat.ts` and
  `forgeMockValues/{mock-values,mock-rules}.ts` with installed
  `@reliantlabs/forge-web-runtime/mock-transport` exports.
  **Verified:** installed 0.13.8 exports `mockValue`, `serviceDefaults` and its
  `CreateMockTransportOptions` declares `readOnly`, passthrough `"first"`,
  timeout and `onPassthroughFallback`. Their “wait for release” comments are
  stale. Keep `demoServiceDefaults.ts`'s service selection and scenario data.
  **Dependency:** compatibility proof, not a release/pin. Check descriptor/rule
  versions and abort/read-only semantics before removing the compatibility layer.
  **Acceptance:** `demoData/__tests__/{forgeMockValues,demoData,consoleScenario,busyApp}.test.ts`,
  demo-transport tests, `tsc -b`; denied writes must never reach a backend,
  cancellation must not turn into a mock success, passthrough timeouts must keep
  their fallback notification.

### 2. cp internal-console lacks the scaffold's dev browser-log sink

- [ ] Add owned Next dev endpoint and published `installDevLogging` boot wiring.
  **Verified:** cp `src/app/providers.tsx` initializes telemetry but contains no
  `installDevLogging`; no tracked `/__forge/log` endpoint exists. The pinned
  forge Next providers template calls it in dev and names
  `src/app/%5F_forge/log/route.ts` as the sink. This is a real diagnostic gap,
  not a reason to reinitialize telemetry or change operator auth.
  **Dependency:** none apparent; use the pinned template's base-path contract,
  not a copied hardcoded root path. Preserve `/internal` and NODE_ENV guards.
  **Acceptance:** endpoint v1/v2 parsing, size bounds, production refusal,
  `/internal/__forge/log` addressing and provider installation/teardown;
  targeted cp Vitest and `tsc --noEmit`. Browser-origin proof that one console
  error appears once in `.forge/logs/<env>/frontend_<name>.log` is still needed.

### 3. Remaining unary adapters require method-by-method migration

- [ ] `src/api/git.ts` still calls a Worktree client and `afterWrite` invalidates
  the worktree cache and notifies `gitStatusStore`; generated worktree queries
  already exist in `src/hooks/worktree-queries.ts`. `src/api/fileSystem.ts`
  likewise remains an owned adapter. These are **not wholesale duplicate
  clients**: wire/view conversion, nested-repo context and product refresh signals
  must survive. Review each read for query-cache semantics and each write for
  generated-mutation substitution, without changing external shapes or blindly
  broadening/refetching live filesystem state.
  **Dependency:** no presumed forge feature; establish each operation's cache
  scope/failure semantics first. **Acceptance:** affected git/file/tree/store
  transport tests, nested-repo operations, unchanged method-key factories,
  `tsc -b`. This audit has not classified every hand-written RPC caller.

### 4. Product logger vs runtime logging — owner design decision before extraction

- [ ] `src/lib/logger.ts` still implements object shrinking, console overrides,
  dev filtering, packaged-Electron IPC and periodic console clearing. Runtime
  devlog supplies the dev sink, not this full policy. Current tests pin error
  stack detail and one IPC send; packaged logs must continue without Vite.
  **Options:** (A, recommended) retain the product adapter and extract only a
  measured vendor-neutral bounded serializer if multiple consumers need it;
  (B) design a forge logger with sink/filter/serialization and explicit override
  lifetime APIs, then adapt Electron through callbacks. B adds a public framework
  lifecycle API; it needs owner approval and tests for installation order with
  HyperDX/devlog/diagnostics, no recursion, no duplicate sinks, and disposal.
  Do not delete it merely because it is long or named logger.

### 5. Streaming is product SDK behavior, not missing generated unary hooks

- [ ] Decide whether any generic streaming primitive belongs in forge.
  **Verified current state:** `src/api/{streaming-grpc,chat-streaming-grpc,
  process-output-streaming}.ts` delegate to `@reliantlabs/sdk/client`'s
  `UserStream`, `ChatStream`, `ProcessOutputStream`. `useUpdateStreamConnection`
  controls shell lifecycle; sequence cursors, snapshot/replay/caught-up markers,
  background resume and reducer/cache updates are product behavior. Generated
  unary hooks are not replacements.
  **Options:** (A, recommended) retain SDK domain streams and app adapters;
  (B) propose an additive forge cancellation/reconnect primitive consumed by the
  SDK, leaving sequences/reducers there. B is substantial API design; owner
  approval is required before implementation. **Acceptance:** existing scoped
  `src/api/__tests__/*stream*` recovery/gap/resume tests, shell stream tests and
  no dropped/duplicated messages or duplicate reconnect owner.

### 6. Error/shell policy equivalence remains a deliberate follow-up, not a blind swap

- [ ] Review Reliant's custom `components/ErrorBoundary.tsx` and
  `components/App.tsx` against RuntimeErrorBoundary/RuntimeShell with product
  acceptance criteria. The current boundary collects redacted support reports,
  records local diagnostics, honors consent and handles stale builds; runtime's
  `fallback`, `onError`, `resetKeys` are useful seams but not proof that replacing
  the class preserves report IDs/component stacks, panel layout or recovery.
  Do not mount a second auth provider/error reporter/toast host just to match a
  scaffold tree. **Acceptance:** ErrorBoundary reporting/redaction and stale-build
  recovery tests, auth startup/hydration, panel recovery and exactly-once reports.
- [ ] Audit legacy error rendering/retry fallbacks in bounded caller groups.
  `queryRetry.ts` still parses bare non-Connect HTTP message codes; runtime offers
  `ConnectClientError.retryable`/`reason`/`userMessage`. Preserve supported legacy
  callers until typed evidence allows deprecation. Do not mass-replace `.message`
  with `userMessage` in diagnostic-only logs or product copy.

## Validation record and next operator procedure

This branch changes only the Reliant devlog client adoption and this checklist;
control-plane was inspected but not modified. **10 scoped files / 151 tests,
`tsc -b`, and changed-code eslint passed** against locked runtime 0.13.8. No
production/browser/deploy proof was run; no full frontend/backend equivalence is
claimed. One earlier exit-zero test run emitted Vite dep-scan shutdown diagnostics
(`Request is outdated`); the final scoped run above completed without those.

Tooling follow-up: Reliant's `task fmt:web -- <files>` ignores the file arguments
and runs `eslint . --fix` across the app (reported as
`e78feffa-356b-4d66-a898-96026d492eb5`). It timed out on this loaded host without
unrelated tracked edits; use explicit scoped eslint until that task is fixed.

Commands for the first slice (from `frontends/web`):

```sh
pnpm exec vitest run src/lib/__tests__/browser-log-forward.test.ts vite-plugin-browser-logs.test.ts \
  src/lib/__tests__/logger.errorDetail.test.ts src/lib/__tests__/logger.electronDoubleSend.test.ts \
  src/lib/__tests__/clientDiagnostics.test.ts src/lib/__tests__/configGuard.test.ts \
  src/lib/__tests__/telemetry.test.ts src/lib/__tests__/telemetryEndpoint.test.ts \
  src/lib/__tests__/connect.routing.test.ts src/lib/__tests__/queryRetry.test.ts
pnpm exec tsc -b
```

Use explicit scoped files; `vitest related` over global settings/auth can expand
into almost the whole app. Tests/typecheck and their actual verdict belong in the
PR. An exit-zero test run with diagnostic optimizer output is not browser proof.
Open non-draft PRs with no queue label; stop at the ready PR, do not poll merging.
