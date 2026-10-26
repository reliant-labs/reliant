# 13 — P9: the web frontend on forge

Status: **in progress; R4 (hooks) is complete** (§8.1). Owner decisions are in §11. Shared context: `PLAN.md` §4 P9,
`11-svcerr-plan.md` §2.3 and §7 Q3 (markers → reason header).

Every claim below comes from reliant `origin/main` at `cbc5221a`, control-plane `origin/main` at
`f1b3b7e7`, and a scratch run of the **pinned** forge (`1be0d294d2af`, go.mod and forge.yaml agree).
The commands are in §12, so you can re-run any of them.

---

## 0. Summary

1. **PLAN §4 P9's first step can't be done as written.** forge.yaml has no `frontends:` key any more.
   forge builds its frontend inventory from `frontends/<name>/` on disk, overlaid by KCL
   `forge.Frontend` declarations (`forge/internal/config/frontend_inventory.go:13`). reliant has
   `web/` and no `deploy/kcl`, so forge sees no frontend, and `forge generate` writes nothing into `web/`
   today. To declare it we either move `web/` → `frontends/web` or write a KCL declaration (D1).
2. **Declaring it naively breaks the build.** In a scratch copy with `web/` moved to `frontends/web`,
   one `forge generate` took `tsc -p tsconfig.app.json` from **0 to 149 errors**, from four separate
   collisions (§2). Each one is a forge defect, and each goes on the fix-in-forge list (§9) before
   reliant declares anything.
3. **The token bridge already exists.** PLAN says "only `surface-sunken` is bridged". That's stale:
   `web/src/index.css:6-229` maps all 28 of forge's tokens, plus `warning-hover` and `destructive-ink`,
   across **10** schemes (not 7), and scopes `accent` under `.forge-ui`. 16 forge components are
   installed verbatim under `src/components/forge-ui/`. P9's styling work is therefore an audit
   plus component replacement (§5, §6), not a new bridge.
4. **The runtime's error type doesn't fit reliant's code.** `@reliantlabs/forge-web-runtime`'s
   `errorNormalizeInterceptor` throws `ConnectClientError`, which **does not extend `ConnectError`**
   (`forge/web-runtime/src/errors.ts:116`). reliant has 77 `instanceof ConnectError` /
   `ConnectError.from` sites in 51 files. Putting the runtime chain in front of them silently turns every
   error into `Code.Unknown`. Making `ConnectClientError` a `ConnectError` subclass in forge (D5) is
   what lets hook adoption go one service at a time.
5. **Config moves to `config.js`, and Electron gets it from its `app://` handler** (D2, recommended). The
   handler already serves the packaged bundle (`electron/src/app-protocol.js:176`). Having it answer
   `app://bundle/config.js` from the main process's resolved config gives desktop the same mechanism as
   hosted and dev: one bundle, with config supplied beside it.
6. **Error markers move to `x-forge-error-reason` on both servers, with an expand/contract
   rollout** (§7). One server bug is fixed along the way: reliant's api-server CORS list doesn't
   expose `X-Forge-Error-Reason` (`internal/grpc/server.go:207`). That means every `svcerr.WithReason`
   it sends today is invisible to a cross-origin browser, and both the hosted SPA and Electron's
   `app://` are cross-origin.

Risk: **Medium.** The work is wide (web, electron, control-plane KCL, two servers), but every step can
be reversed by rolling forward and verified locally. Nothing touches a schema.

---

## 1. Corrections to PLAN §2 / §4 (measured now)

| PLAN says | Actually (origin/main) | Evidence |
|---|---|---|
| declare `frontends: [{name: web, type: vite-spa, path: web}]` in forge.yaml | no such key; the inventory comes from `frontends/` on disk plus KCL | `frontend_inventory.go:13`, `ResolveInventoryAtLoad` :168 |
| only `surface-sunken` bridged | the full bridge has landed (28 forge tokens + 2) | `web/src/index.css:149-229` |
| 7 color schemes | **10** (`professional-blue` … `pure-black`) | `web/src/themes/professional-themes.css`, `colorSchemeTokenContract.test.ts:14` (the `web-styling` skill also says seven, which is stale) |
| 20 `VITE_*` vars | **21** distinct, read in 12 files | §4.1 |
| 688 vitest files / 10 Playwright specs | **813** / **11** | `find web/src -name '*.test.ts*'`, `ls web/e2e/*.spec.ts` |
| runtime chain includes retry | 0.6 removed retry from `buildRuntimeInterceptors`; React Query owns retries | `forge/web-runtime/src/interceptors.ts:24-33,181` (the `frontend-runtime` skill still describes the old chain) |
| tests under `task test:frontend` | reliant has no Taskfile until P8; today's gate is `make test-web` / `make test-affected LANES=web` | `Makefile:792` |

---

## 2. What `forge generate` writes when web is declared, and what collides

**Dry run:** `git archive` of origin/main into `/tmp/p9/scratch`, then `git mv web frontends/web` (the
cheapest declaration, D1), then the pinned forge's `generate` (1m42s, exit 0: "797 file(s) written, 236
changed"). Every file under `frontends/web/` it touched:

| Written | Count | Kind | Collision |
|---|---|---|---|
| `src/gen/**` (TS stubs) | 57 modified, `+controlplane/` | forge's TS step, via **its own** `frontends/web/buf.gen.yaml` (new) | **C1: two writers for one directory.** forge runs the frontend's `node_modules/.bin/protoc-gen-es` (v2.10.2, `import_extension=.js`). reliant's root `buf.gen.yaml:58` runs the pinned v2.15.0 with `ts_nocheck=true`. Every stub churns, and the forge copy fails reliant's `erasableSyntaxOnly`: **~95 TS1294 errors**. `make generate-all` and forge would overwrite each other forever. |
| `src/hooks/index.ts` | 1 modified | Tier-1 barrel ("namespaced re-exports", because `useKillProcess`/`useListProcesses` exist in two services) | **C2: a hand-written barrel silently clobbered.** reliant's 17-line `hooks/index.ts` (`useCodexOAuth`, `useFullscreen`, …) carried no forge marker and was overwritten anyway, with no drift refusal and no mention in the summary. **~30 TS2305 errors** in 7 importers. |
| `src/hooks/<svc>-service-hooks_gen.ts` | 35 new (all services except StreamingService) | Tier-1 React Query tables over `@reliantlabs/forge-web-runtime/service-hooks` | **C3: they import `connectClient` from `@/lib/connect`, which reliant doesn't have** (35 × TS2307). They also cover services no browser calls (`ToolsDaemonService`), and there's no per-frontend allowlist. |
| `src/hooks/<svc>-service-hooks.test.tsx`, `src/hooks/use-query-resource.ts` | 35 + 1 new | scaffold-once tests | import `@/lib/test-utils` and `@/lib/use-event`, neither of which exists, so vitest fails on all 35 |
| `src/components/ui/*.tsx` (snake_case) | 35 new | the scaffold's auto-installed component set (`button`, `modal`, `login_form`, `sidebar_layout`, …), reported as "changed outside forge's writers" | **C4: case-insensitive collisions** with reliant's `Button.tsx`, `Modal.tsx`, `Input.tsx`, `Card.tsx`, `Badge.tsx` (**TS1261** ×5, and unusable on a macOS checkout, which is where Electron is built). They also duplicate the 16 components reliant already installed under `forge-ui/`. The starter `login_form` and `sidebar_layout` arrive too. |
| `src/lib/apiurl_gen.ts`, `otel_gen.ts`, `mock-transport_gen.ts`; `src/mocks/*` (6) | 9 new | Tier-1 | harmless. `otel_gen.ts` overlaps reliant's `lib/telemetry.ts`, which already calls `/telemetry` |
| `package.json` | 1 modified | `@reliantlabs/forge-web-runtime` `^0.5.0` → `^0.6.0` | wanted, but it should be a reviewed bump |

Not written: no `config_gen.ts` / `public/config.js` (reliant has no `(forge.v1.frontend_config)`
message), no `index.html` / `vite.config.ts` / `connect.ts` / providers (all scaffold-only), and no
CRUD pages, nav or dashboard (reliant has zero CRUD entities, because every CRUD-named rpc is
`forge:custom`). That last point is fragile. vite-spa *does* get pages (`src/pages/<slug>/`,
`generate_frontend_pages.go:84`) the day an entity appears, and the only `routes: none` opt-out is
KCL `forge.Frontend.routes`, which a disk-discovered frontend has nowhere to state (gap F9-6).

`forge lint --frontend-stores` on the scratch copy: clean.

**Result:** forge fixes F9-1…F9-5 (§9) must land and be pinned **before** reliant declares the frontend.
The gate for the declaring PR is "`tsc -b` and `npx vitest run` unchanged from before the move".

---

## 3. Transport and React Query hooks

### 3.1 Today

`web/src/api/transport.ts` (715 lines) is the single chain for three transports (reliant api,
control-plane, unauth): `timeout → auth → daemon-last-seen → tracing → error-logging → upgrade-modal →
machine-wake → 401-signout` (:17, :691). There are 29 `api/*-grpc.ts` wrappers, 72 files with React
Query keys, and 46 zustand stores. The web also calls **control-plane** services
(`services/controlPlane/*`, which come from vendored `controlplane.v1` protos). forge generates no hooks
for those, because they aren't reliant's services.

### 3.2 Target: owned `src/lib/connect.ts`, runtime-composed

| Concern | Today | Target |
|---|---|---|
| bearer token | `authInterceptor` + `api/authProvider.ts` | runtime `authInterceptor` via `buildRuntimeInterceptors({ getToken: () => getAuthTokenProvider().getToken() })`. reliant uses bearer tokens (Supabase / API key), not forge's cookie session. That's fine, because `getToken` is optional by design. |
| traceparent / spans | hand `tracingInterceptor` (OTel API) | runtime `traceInterceptor` (propagates the HyperDX browser span reliant already starts via `/telemetry`). Delete reliant's. |
| error normalization | none (raw `ConnectError` everywhere) | runtime `errorNormalizeInterceptor`, **after F9-5** (`ConnectClientError extends ConnectError`) |
| retry | `lib/queryRetry.ts` (React Query) | unchanged. That's the runtime's rule too. |
| reporting | `errorInterceptor` + `rpcErrorPolicy.ts` → Sentry | stays, fed by `onError` (it receives the normalized error, so policy keys on `.reason`/`.code`) |
| deadline, daemon-last-seen, upgrade modal, machine-wake, 401 sign-out | reliant interceptors | **stay reliant-owned**, composed around the runtime chain in the same order (timeout stays outermost) |
| per-service routing | three `getTransport*` functions | `connectClient(svc)` routes on `svc.typeName`: `DaemonRegistryService`/`TokenService` → the control-plane transport in cloud mode (today's `grpc-client.ts:161`), unauth set → unauth transport, everything else → the reliant api. That's the seam the generated hooks call. |

`connect.ts` is owned (the vite-spa template writes it once). reliant writes it by hand on top of
`transport.ts`, and then `transport.ts` shrinks to the reliant-specific interceptors.

### 3.3 Hook adoption, one service at a time

The rule for each service lane: replace that service's hand query keys with `<svc>Keys`. Delete
pass-through wrappers in `api/<svc>-grpc.ts`. Move any zustand-held server state for the service onto
the hooks (`forge lint --frontend-stores` must stay clean). Point stream-driven invalidation
(`store/globalUpdatesStore.ts`) at `<svc>Keys.all`. Also: a service's reads and writes migrate **in
the same PR**. Generated keys (`[service, entity?, method, protojson(req)]`) and the hand keys don't
invalidate each other, so a half-migrated service shows stale data.

Order: leaf-first, low fan-out, unary, single transport. Measured importers of each `api/<svc>-grpc.ts`:

| Wave | Services (importers) | Why here |
|---|---|---|
| **A, pilot** | Question (12), Plan (3), Approval (3), Task (2), System (3), Repo (7), Terminal (5) | small, read-mostly, no zustand cache. Proves `connect.ts`, keys and error typing end to end. |
| **B** | Preset (32), ProviderAccount (15), Catalog + input-options (17), Connection (10), MCP (11), Attachment (7), Settings (10), Background + PackageCommands (16, the namespaced-barrel pair) | moderate fan-out, self-contained caches |
| **C** | Project (21), Worktree (28, `worktreeStore`), Run (19), Workflow (44), Trigger (45) | zustand-backed and stream-invalidated, so each one is its own lane |
| **D, last or never** | Chat (35, streaming-coupled), FileSystem (daemon-routed, large payloads), Forge (7, deploy authz), Daemon (9), DaemonRegistry + Token (control-plane transport in cloud mode) | decide per service after C. Hand-rolled is fine where streaming owns the cache. |
| excluded | StreamingService (no hooks generated), ToolsDaemonService / Scenario / Account (no browser use) | need the F9-6 allowlist so their hooks aren't emitted at all |

---

## 4. Runtime config: the `VITE_*` vars and Electron

### 4.1 The 21 vars

| Group | Vars | Fate |
|---|---|---|
| per-env endpoints | `API_URL`, `GRPC_URL`, `CONTROL_PLANE_API_URL`, `GATEWAY_URL`, `APP_URL`, `DISABLE_TLS` | `ReliantWebConfig` fields (typed), values from runtime_config. `GRPC_URL` and `API_URL` collapse into one `API_URL` (they're equal in every env, `env.k:624-625`). |
| identity | `SUPABASE_URL`, `SUPABASE_ANON_KEY` (publishable), `AUTH_MODE` | typed config |
| billing | `STRIPE_PUBLISHABLE_KEY` | typed config (keeps `isStripeConfigured()` = "absent means off") |
| telemetry | `SENTRY_DSN`, `SENTRY_ENABLED`, `SENTRY_TRACES_SAMPLE_RATE`, `SENTRY_REPLAYS_SESSION_SAMPLE_RATE`, `SENTRY_REPLAYS_ERROR_SAMPLE_RATE`, `OTEL_ENDPOINT`, `TELEMETRY_SINKS` | typed config. The web DSN and the Electron DSN become two per-env values, not a build-time swap (`env.k` `reliant_desktop_release_config`). |
| product | `DOWNLOAD_BASE_URL`, `CLI_DEFAULTS_BAKED` | typed config |
| dev-only | `GRPC_PORT` (`scripts/dev.sh:366`), `AUTO_ANON_SIGN_IN` | **delete** `GRPC_PORT` (`apiurl_gen.ts` / `api-base.ts` and `/_api` replace it). `AUTO_ANON_SIGN_IN` becomes a typed boolean that defaults to false. |

Readers: `api/grpc-client.ts`, `api/grpc-unauth.ts`, `lib/{constants,cli-commands,supabase,protocol,sentry,telemetrySinks}.ts`,
`services/controlPlane/config.ts`, `components/{Billing/stripe.ts,ReliantDownloadOptions.tsx,AuthGuard.tsx}`.
All of them read one `loadConfig()` (`src/lib/config_gen.ts`). `index.html` gets
`<script src="%BASE_URL%config.js">` ahead of the theme script, and the CSP needs nothing because
`'self'` already covers it.

**Where values come from.** reliant has no envs; control-plane declares reliant-web
(`deploy/kcl/lib/frontends.k:45` for dev, `lib/reliant_web.k` for prod-web hosted). So the
**schema** lives in reliant (`proto/reliant/config/v1/web_config.proto`,
`(forge.v1.frontend_config)`, which gives defaults plus `config_gen.ts`). The **values** live in
control-plane, as each env's `forge.Frontend.runtime_config`, replacing `env.reliant_web_vite_env`.
- **Hosted (prod-web, `forge.OnHosted`)**: the control plane writes `config.js` after every StaticSite sync. That
  already works for a source-pinned frontend, and the release artifact carries none.
- **Dev (`forge.OnHost`, `path = "../reliant/web"`)**: **broken today, forge gap F9-9.** `writeFrontendRuntimeDocs`
  skips any frontend whose directory is outside the project (`frontend_config_refresh.go`, `fe.Dir` → `!ok`),
  so the dev SPA would 404 `config.js`.
- **Key drift across repos**: control-plane can't check its `runtime_config` keys against reliant's
  schema, so a typo silently falls back to the default (F9-10).

### 4.2 Electron: three ways to get config.js

Facts: packaged builds load `app://bundle/` from `registerAppProtocol` (`app-protocol.js:176`). Dev loads
the Vite dev server (`window-manager.js:94`). The preload already posts `RELIANT_CONFIG` asynchronously
(`preload.js:411`), with URLs that come from the main process (`backend-manager.js`), and `main.tsx`
blocks up to 15s waiting for it. The release build bakes `electron/release.config.json`'s `vite` map
(rendered from control-plane's `reliant_desktop_release_config`).

| Option | How | Pro | Con |
|---|---|---|---|
| **A. `app://` synthesizes `config.js`** (recommended) | `protocol.handle` answers `/config.js` with `window.__FORGE_CONFIG__ = <json>`, built in main from `build-config.js` (the projection of `release.config.json`, now a `web` map instead of `vite`) overlaid with main-process runtime values (the `RELIANT_*` env a user set to point at a self-hosted server) | the same mechanism as hosted and dev, and synchronous before the bundle runs. A self-hosted override needs no rebuild. The URL half of `waitForConfig` goes away. | about 30 lines in `app-protocol.js` plus a test |
| B. write a static `config.js` into the packaged `web/` at package time | release generator writes `dist/config.js` | trivial | runtime overrides can't reach the renderer, and it's a second copy of what main already knows |
| C. preload `contextBridge.exposeInMainWorld('__FORGE_CONFIG__')` | preload | no file | collides with dev's real `config.js`: `exposeInMainWorld` defines a read-only property, so the script's assignment throws in strict mode. It also needs sync IPC. |

What's left in `window.RELIANT_CONFIG` after A: host facts only (`isElectron`, `platform`, `isDev`,
`daemonPort`, `temporalUIPort`). Those aren't per-env configuration and they stay asynchronous.

---

## 5. Token bridge: the mapping table

forge's vocabulary is the 28 `--color-*` names in `forge/internal/templates/frontend/vite-spa/src/index.css`
(the same set as the Next.js `globals.css`). Every forge component class resolves to one of them, with
one exception: `warning-hover` (`pkg/components/components/ui/confirmation_dialog.tsx`), which **forge's
own themes don't declare**. That's gap F9-7. reliant patched it locally.

| forge token | reliant source (`web/src/index.css`) | note |
|---|---|---|
| `surface` | `--card` | primary surface |
| `surface-muted` | `--background` | **never `--muted`**: `--muted` lifts in dark and recesses in light (the muted-inversion trap, :97) |
| `surface-sunken` | `--background` | the inset; elevation rule in `forge-ui/card.tsx` |
| `border` / `border-strong` | `--border` / `--input` | |
| `ink` / `ink-muted` / `ink-subtle` | `--foreground` / `--muted-foreground` / `--muted-foreground / 0.72` | a third rung derived from the second |
| `accent`, `accent-hover`, `on-accent`, `accent-surface`, `accent-border`, `accent-ink` | `--forge-accent-src` → `--accent` globally, **`--primary` inside `.forge-ui`** | the one scoped token: reliant's `bg-accent` (~159 sites) is a muted tint, forge's is the action color |
| `danger`, `danger-hover`, `on-danger`, `danger-surface`, `danger-border`, `danger-ink` | `--destructive` (+ `/0.85`, `/0.12`, `/0.38`), `--destructive-foreground`, `--destructive-ink` | |
| `success`, `success-surface`, `success-border`, `success-ink` | `--success` (+ alphas), `--success-ink` | |
| `warning`, `warning-hover`*, `on-warning`, `warning-surface`, `warning-border`, `warning-ink` | `--warning` (+ alphas), `--warning-foreground`, `--warning-ink` | *undeclared by forge itself (F9-7) |

P9 work here:
- **S0:** extend `colorSchemeTokenContract.test.ts` so it collects every token class used under
  `src/components/forge-ui/**` and asserts each one is declared in the `@theme` block. A future `forge component install`
  that brings a new token then fails a test instead of rendering transparent.
- Fix the `web-styling` skill (it says seven schemes; there are ten).

## 6. Component replacement inventory

`src/components/ui/`: 22 components. `forge-ui/` has 16 forge installs (identical to the pin except `link.tsx`,
which is reliant's intended tanstack-router seam, and `mono.tsx`, which reliant changed to export `monoLength`). Each row lists
importers.

| reliant `ui/` | importers | forge equivalent | Verdict |
|---|---|---|---|
| `StatusIndicator` | 0 | `status` | **delete** (dead) |
| `DenialReasonModal` | 0 | — | **delete** (dead) |
| `Card` (+barrel) | 2 + 8 | `forge-ui/card` (59 already) | **S1** migrate, delete |
| `Badge` (+barrel) | 3 + 7 | `forge-ui/badge` (21 already) | **S1** |
| `ErrorAlert`, `ErrorSummary` | 1, 1 | `alert_banner` | **S1** |
| `Progress` | 3 + 2 | `progress_bar` | **S1** |
| `Toggle` | 15 | `toggle_switch` | **S2** |
| `Input` (+`Textarea` 3) | 10 | `input` + `label` + `form` (no textarea) | **S2** input. Textarea: upstream to forge or keep. |
| `Modal` | 56 + 2 | `forge-ui/modal` (8 already) | **S2**, after an a11y parity check (focus trap, Escape, nested modals in `ImagePreviewModal`) |
| `Dropdown` | 6 | `dropdown_menu` | **S2** |
| `Button` | 100 + 12 | `button` (`primary/secondary/outline/ghost/danger`, sizes, loading) | **S3**. Variant map: `destructive→danger`. `default`/`accent`/`gradient` have no forge twin (D6). |
| `Tooltip` | 116 | `tooltip` (forge#1319, #1324) | **R5 done** (#1523): upstreamed, 226 sites migrated, deleted. |
| `SearchableDropdown`, `MultiSelectDropdown` | 2, 3 | `combobox` (forge#1323) | **R5 done** (#1532): upstreamed, migrated, deleted; `components/ui/` is gone. |
| `ContextMenu`, `HelpPopover` | 8, 6 | `context_menu`, `popover` point anchor (forge#1322) | **R5 done** (#1531): upstreamed, migrated; HelpPopover is a `workflow/` composition over `popover`. |
| `FileIcon`, `ImagePreviewModal`, `RunStatusIndicator`, `ConfigScopeSelector` | 8, 6, 11, 2 | domain | move out of `ui/` into their feature directories (`ui/` is for primitives) |

Installs keep going to `src/components/forge-ui/`, not `ui/` (C4). Every replaced site is wrapped in `.forge-ui` (or
an ancestor is), otherwise forge's `accent` paints as reliant's muted tint (`index.css:90`).
Reliant fixes that apply to everyone go upstream, as the card's elevation rule did: the `monoLength` export,
and a tanstack-router `link` variant for the vite-spa scaffold (F9-13).

---

## 7. Error markers → `x-forge-error-reason` (server and web together)

P7a (L0–L5, #1267…#1296) made handler errors svcerr-classified, with marker text kept in the
constructor argument. P9 finishes the job (`11-svcerr-plan.md` §7 Q3): **a stable reason on every error
the UI branches on, and no UI branching on prose.**

### 7.1 Inventory

| # | Web reader | Today keys on | Server producer | Reason (new) |
|---|---|---|---|---|
| 1 | `lib/daemon-errors.ts:43` `isDaemonConnectingError`, `:67` `isDaemonPendingError`, `machineWakeInterceptor` | text `no daemon connected` (+ `still starting`/`suspended`) | reliant `toolexec/daemon_router.go:38,67` (`ErrDaemonPending`, `ErrDaemonSuspended`), `daemon_router_nats.go:464,466`, `machinewake/machinewake.go:147` | `daemon_starting`, `daemon_suspended`, `daemon_waking`. "connecting" means any of the three. |
| 2 | `lib/daemon-errors.ts:53` `isNoMachineError` | `no daemon available` | `toolexec/daemon_router.go:83` (`ErrNoDaemon`), `handlers/worktree/worktree_placement.go` | `no_daemon` |
| 3 | `lib/chatSendRouting.ts:39` | FailedPrecondition + `chat has not started` | `handlers/chat/chat_send.go:679,1123` | `chat_not_started` |
| 4 | `components/workflow/WorkflowBuilderPage.tsx:532` | `workflow was modified since` | `handlers/workflow/workflow.go:618,1030` | `stale_revision` |
| 5 | `lib/daemon-resume.ts:69` `isAlreadyResumedError` | FailedPrecondition + `not suspended` | **control-plane** `svcdaemon/service.go:991` (hand `connect.NewError`) | `daemon_not_suspended` |
| 6 | `lib/daemon-resume.ts:28` `isQuotaResumeError` | text `resource_exhausted`/`compute limit` | control-plane, which already sets the reason `compute_budget_exhausted`/`no_compute_subscription` | delete the text arm and read the reason |
| 7 | `services/forge/deployAuthz.ts:62-90` | text `permission denied`/`permissions do not include`/`no control-plane credential`/`forge login`, plus a regex for the scope | control-plane `handlers/access_token/{exchange.go:140,service.go:665}`, reliant `handlers/token/token_exchange.go:119` | `scope_missing` (scopes in `x-forge-error-scopes` metadata, which beats parsing prose), `no_control_plane_credential` |
| 8 | `services/controlPlane/observability/errors.ts:77,109` | Unavailable + `not configured`; `not a member` | control-plane observability / orggrants | `observability_not_configured`, `not_org_member` |
| 9 | `OnboardingFlow/steps/GitHubConnectStep.tsx:35` | `no git credential found` | control-plane `gitcredential/gitcredential.go:735,838`, which reliant `token_git.go:22-28` already names `git_credential_not_connected` | `git_credential_not_connected` |
| 10 | `Projects/ProjectPicker.tsx:96`, `ProjectPickerModal.tsx:119` | `already exists`/`409` | P7a-classified AlreadyExists | branch on `.code === "already_exists"` (no new reason) |
| 11 | `store/processStore.ts:160` | `not running`/`already stopped` | `handlers/package_commands/package_commands.go:235` and background kill | `process_not_running` |
| 12 | 7 `x-reliant-reason` readers (`rpcErrorPolicy.ts:115`, `upgradeInterceptor.ts:38`, `accountRequired.ts`, `billingUtils.ts:539`, `diskGrow.ts:121`, `daemon-resume.ts:56,68`, `forgeApiDisabled.ts`, `useOnboardingQueries.ts:40,62`) | header `X-Reliant-Reason` | control-plane `svcdaemon/connecterr.go:17,57`, `billing/connecterr.go`, `accountgate`, `llmproxy/billing.go:96,225` (plain HTTP 429), reliant `internal/grpc/forge_api_disabled.go:37` | same values, under `svcerr.ReasonHeader` |

Out of scope: Supabase auth (`authErrors.ts`, `ForgotPassword.tsx`, `AuthScreen.tsx`); browser and network
errors (`lazyRoute.tsx`, `toast-manager.ts`); daemon-reported text (`useRepoChanges.ts:238` "gh not installed",
`daemonerr` keeps it per 11 §7 Q1); and the `isDaemonPendingError` *tool-result-text* fallback, because
the LLM still sees `daemonoffline.ErrorSubstring` in tool text. Only RPC-error readers move.
`FileViewerTab.tsx:140` (matching `404`/`403`/`415` in text) switches to `.code` in the same pass.

### 7.2 Design

- **Server (both repos):** `svcerr.WithReason(err, "<reason>")` at the producer, so it rides
  `x-forge-error-reason`. In reliant, `daemonerr.Error` stamps the daemon reasons. The `X-Reliant-Reason` producers
  in control-plane become `svcerr.WithReason`. `llmproxy`'s HTTP 429 sets the header under
  `svcerr.ReasonHeader`. The `X-Reliant-Upgrade-URL` header is always `/billing/plans`, so it's **deleted**:
  the web routes by reason. Reason constants for the UI live in one place per repo; the web's
  copy is a closed TS union in `src/lib/errorReasons.ts`.
- **CORS:** reliant `internal/grpc/server.go:207` adds `X-Forge-Error-Reason`. Better still, it adopts forge's
  `CORSMiddleware`, which exposes it already. control-plane `serve.go` already widens expose-headers.
  `TestCORSExposesEveryHeaderTheWebClientReads` (`cors_web_client_test.go:97`) picks up the new read
  automatically. That's the guard.
- **Web:** every reader becomes `normalizeError(err).reason === …` (or `.code`). `rpcErrorPolicy`
  classifies on reason.
- **Rollout, expand/contract:** web-1 reads `x-forge-error-reason` **and** falls back to the old header and
  markers → servers switch (control-plane and reliant in parallel) → web-2 deletes the fallbacks, and
  `daemon-errors.ts` loses its text arms. Old Electron builds in the field degrade to a plain toast,
  which is recoverable (D3).

---

## 8. Lanes (disjoint files) and order

```
F (forge, parallel): F9-1 F9-2 F9-3 F9-4 F9-5 F9-6 F9-7 F9-9 … ─► pin forge in reliant + control-plane
                                                                  │
R0 declare (serial, alone) ◄──────────────────────────────────────┘
  ├─► R1 transport/connect.ts ─► R4 hooks wave A ─► B (parallel per service) ─► C (one lane per service) ─► D
  ├─► R2 config: R2a web+proto ─┬─► R2c control-plane KCL (needs F9-9, F9-10 pinned)
  │                             └─► R2b electron
  ├─► R3 reasons: R3w1 web-expand ─► R3s-rel ∥ R3s-cp ─► R3w2 web-contract
  └─► R5 styling: S0 ─► S1 ∥ S2(per primitive) ─► S3 Button
```
R3s-rel and R3s-cp are server-side and **don't need R0**. They can start right after R3w1, which doesn't need
R0 either (it touches no generated file). Start R3 first.

| Lane | Repo | Owns (files) | Depends on |
|---|---|---|---|
| F9-x | forge | §9, each in its own package | — |
| **R0 declare** | reliant + control-plane | `git mv web frontends/web` and every path reference: reliant `Makefile`, `buf.gen*.yaml` `out:`, `.air*.toml`, `.dockerignore`, `.gitignore`, `electron/**` (packaging path, `navigation-policy.js`, `preload.js`, tests), `.github/workflows/{pr-ci,release,license-gate}.yml`, `.github/scripts/*`, `tools/test-affected/*`, `scripts/*`, `internal/grpc/cors_web_client_test.go`, `internal/buildmode/buf_plugin_pins_test.go` (206 refs, ~60 files). control-plane: `deploy/kcl/lib/frontends.k:50` path, `lib/reliant_web.k` `subdir`, pin scripts. Then `make forge-generate-check` and commit the generated hooks. | F9-1…F9-4 pinned |
| R1 transport | reliant | `frontends/web/src/lib/connect.ts` (new, owned), `src/lib/test-utils.ts`, `src/api/{transport,grpc-client,grpc-unauth}.ts`, `src/services/controlPlane/client.ts` | R0, F9-5 |
| R2a config (web) | reliant | `proto/reliant/config/v1/web_config.proto`, `frontends/web/index.html`, the 12 reader files in §4.1, `src/lib/configReady.ts`, `src/main.tsx` | R0 |
| R2b config (electron) | reliant | `electron/src/{app-protocol,preload,backend-manager}.js`, `electron/release.config.json` shape, release generator, `.github/workflows/release.yml` | R2a |
| R2c config (KCL) | control-plane | `deploy/kcl/lib/{env,frontends,reliant_web}.k`, `deploy/kcl/dev/main.k` (the reliant-web overlay), `reliant_desktop_release_config` | R2a, F9-9/F9-10 pinned |
| R3w1 / R3w2 | reliant | `frontends/web/src/lib/{errorReasons,daemon-errors,daemon-resume,chatSendRouting,accountRequired,forgeApiDisabled}.ts`, `src/api/{rpcErrorPolicy,upgradeInterceptor}.ts`, `src/services/forge/deployAuthz.ts`, the components in §7.1 | none (w1); servers (w2) |
| R3s-rel | reliant | `internal/daemonerr`, `internal/toolexec/daemon_router*.go`, `internal/machinewake`, `internal/handlers/{chat,workflow,worktree,package_commands,background}`, `internal/grpc/{server,forge_api_disabled}.go` | R3w1 shipped |
| R3s-cp | control-plane | `internal/svcdaemon/{connecterr,service}.go`, `internal/billing/connecterr.go`, `internal/accountgate`, `internal/llmproxy/billing.go`, `internal/handlers/access_token/*`, observability, `internal/gitcredential`, `internal/convert/convert.go` | R3w1 shipped |
| R4-<svc> | reliant | `src/api/<svc>-grpc.ts` + its consumers + its store | R1. Waves are disjoint **by service**; two lanes touching one store serialize. |
| R5 S0/S1/S2/S3 | reliant | `src/index.css`, `src/components/ui/*`, `forge-ui/*`, call sites per primitive | R0 (for install-dir behaviour); S0 first |

Critical path: F9-1…F9-5 → pin → R0 → R1 → R4 waves. R3 and R5-S0/S1 run alongside from day 1.

### 8.1 R4 status: complete

R4 is done. Every reliant.v1 service the web reads either has generated hooks and keys, or stays
hand-written for a stated reason. `src/api/grpc-client.ts` is deleted: hand-written code calls
`connectClient(<Service>)` from `src/lib/connect.ts` directly, the same seam the generated hooks use.

- **Merged:** wave A #1385; B1 #1437, B2 #1472; C: Run #1508, Project #1513, Worktree #1514, Trigger #1515,
  Workflow #1516; D (plan `16-p9-r4-wave-d-plan.md` #1518): D0 #1521, D1 #1533, D2 #1562, D3 #1537,
  D4 #1544, D5 #1567, D6 #1569, D7 #1576, and DZ #1582, which deleted `grpc-client.ts` and
  `daemon-grpc.ts` and removed the caller-less wrappers.
- **Stayed hand-written** (D4's "acceptable where streaming owns the cache", decided per service in
  16 §0):
  - the chat core (`hooks/chat-model.ts` / `chat-actions.ts`): the stream writes it in place;
  - the stream (`api/streaming-grpc.ts`, `api/process-output-streaming.ts`);
  - the file tree and content (`api/fileSystem.ts`): per-machine, lazy, and the editor buffer is client state;
  - `api/grpc-unauth.ts`: pre-session, kept apart for the import graph;
  - the DaemonService OAuth helper, now in `lib/oauth-local.ts`;
  - the shrunk `api/settings-grpc.ts` (owner decision S, option C).

  Small module caches for non-React code (`node-metadata`, `cel-completion-service`, `catalog-models`)
  and `api/scenarios.ts` (no wave scheduled ScenarioService) also stay. The full list and reasons are in
  the Status section of `16-p9-r4-wave-d-plan.md`.
- **Forge items delivered:** #1294 (select-typed data, `use<Rpc>Infinite`, full-message `mockTransport`),
  #1312 (`lint --frontend-stores` across every frontend), #1316 (stale starter rows on a hook-kind flip;
  `forge:query-scope`), #1318 (`<svc>Queries` for code outside React), #1320 (`<svc>Keys.<rpc>.all`
  method-scope prefix), #1321 (adopted frontends raised to the web-runtime floor).

---

## 9. Forge gaps (fix in forge, not around it)

| # | Gap | Evidence | Needed by |
|---|---|---|---|
| F9-1 | `generate` auto-installs the scaffold's 35-component starter set into an **adopted** frontend's `src/components/ui/`, including `login_form`/`sidebar_layout`, with no case-insensitive collision check | §2 C4 (5× TS1261) | R0 |
| F9-2 | the hooks barrel `src/hooks/index.ts` overwrites an **unmarked, hand-written** file without a drift refusal, and the summary doesn't list it | §2 C2 | R0 |
| F9-3 | the TS stub step is a **second writer** for `src/gen`: it adds `frontends/<fe>/buf.gen.yaml` and runs whatever `protoc-gen-es` is in the frontend's `node_modules` with fixed opts. It should use the project's own es plugin pin and opts (or a "project-owned stubs" mode) and refuse two writers on one directory. | §2 C1 (~95 TS1294, 57 files churned) | R0 |
| F9-4 | generated hooks and hook tests import `@/lib/connect`, `@/lib/test-utils`, `@/lib/use-event`, which only the scaffold writes. In an adopted frontend, generate should scaffold those owned seams once (or refuse, naming them). The `connect.ts` template should document per-service transport routing. | §2 C3 | R0 |
| F9-5 | web-runtime `ConnectClientError` should **extend `ConnectError`** (keeping `code: Code`, metadata, details and `rawMessage`, and adding `reason/retryable/status/traceId`, with the string code renamed `codeName`). Today normalizing breaks every `instanceof ConnectError` consumer, so an existing app can't adopt it incrementally. | `errors.ts:116`, reliant's 77 sites | R1 |
| F9-6 | **per-frontend codegen knobs with no deploy env to hold them**: a hooks/services allowlist (reliant doesn't want `ToolsDaemonService` hooks) and `routes: none`. Today they're KCL-only, and a disk-discovered frontend can't say them. Recommendation: a `"forge"` block in the frontend's own `package.json` (`routes`, `hooks`, `components_dir`) — colocated, declarative, and not topology. | §2, `frontend_inventory.go` | R0 (routes), R4 (hooks) |
| F9-7 | `confirmation_dialog.tsx` uses `warning-hover`, which neither forge theme (`vite-spa/src/index.css`, `nextjs/globals.css`) declares. Add the token, and add a forge test that every token class in `pkg/components` is declared by each template theme. | §5 | S0 |
| F9-8 | raw palette classes in library components (`ui/avatar.tsx` ×10 `bg-*-500`, `ui/detail_view.tsx` ×31). The skill calls this a forge bug, so tokenize (a categorical `cat-1..8` ramp for avatar). | `rg` §12 | S2 |
| F9-9 | the runtime config document for a frontend whose code is **outside the project** (sibling path in dev, source pin when building) is never written: `writeFrontendRuntimeDocs` skips `!fe.Dir`. Serve it without writing into the other repo: render to `.forge/runtime-config/<fe>/config.js`, and have the dev server serve it (env `FORGE_RUNTIME_CONFIG_FILE` + a web-runtime vite plugin). | `frontend_config_refresh.go` | R2c |
| F9-10 | cross-repo `runtime_config` keys aren't validated against the frontend's typed schema. Have the frontend's repo emit a schema beside `config_gen.ts` and check it at render. | §4.1 | R2c |
| F9-11 | skill and doc drift: `frontend-runtime` describes retry in the default chain; `PLAN`-era guidance describes forge.yaml `frontends:`; `apiurl_gen.ts`'s header says "VITE_API_URL always wins" | §1 | — |
| F9-12 | component catalog gaps that reliant uses heavily: tooltip (116 sites), popover, combobox/multiselect, context menu, textarea | §6 | S2/S3 |
| F9-13 | the vite-spa scaffold should install the tanstack-router-aware `link` (reliant's `forge-ui/link.tsx`), and export `monoLength` from `mono` | §6 | S1 |

F9-1…F9-4 are a single theme: **adopting an existing frontend** is a path `generate` doesn't model yet.
`project new --in-place --kind site` handles it for static sites (`DiscoverRootFrontend`), but not for a frontend inside a Go
project. The durable fix is one "adopted frontend" mode, which writes only Tier-1 codegen plus the owned seams
that are missing, never the starter set. Don't treat them as four patches.

---

## 10. Verification

- **Type and unit:** `npx tsc -b` (0 errors on main, 149 after a naive declaration, so the R0 gate is 0).
  `make test-affected LANES=web` while iterating, and the full `npx vitest run` once for R0/R1. The 35 generated
  `*-service-hooks.test.tsx` must pass (they need F9-4's `test-utils`). `task test:frontend` replaces
  these once P8's Taskfile lands. R0's move makes the forge Taskfile template's
  `FRONTEND_DIR: frontends/<name>` true.
- **Transport:** `api/__tests__` transport-order tests are re-pointed at `connect.ts`. New tests: a
  `ConnectClientError` from the chain is still `instanceof ConnectError` with `.code` intact (F9-5 in place);
  and `connectClient(TokenService)` routes to the control-plane transport in cloud mode.
- **Reasons:** Go wire tests per producer, shaped like `token/token_git_wire_test.go`: the response carries `x-forge-error-reason`.
  The reliant CORS test above. A control-plane test that every former `X-Reliant-Reason` site now sets
  `svcerr.ReasonHeader`. Vitest per reader with a `ConnectError` built from metadata only, and no message.
- **Config:** a vitest for `loadConfig()` defaults. An electron test (`electron/test/`) that
  `app://bundle/config.js` returns the main process's resolved document and that a `RELIANT_*` override reaches it.
  `forge env render dev` in control-plane shows reliant-web's runtime_config. Smoke: `forge env up dev`, then
  `curl <web>/config.js`. Packaged smoke: `scripts/build-electron-prod-local.sh`, then open it and check that
  `window.__FORGE_CONFIG__` has prod endpoints.
- **Visual, across the 10 schemes × light/dark:** a new Playwright `e2e/color-schemes.spec.ts` drives
  `/forge-token-sandbox` (already DevOnlyRoute, `routes.tsx:497`) and three real surfaces (Settings,
  Worktrees panel, Inbox), setting `data-color-scheme` + `.dark` and taking
  `toHaveScreenshot` baselines captured **before** S1. Each S-wave PR diffs against them. Also
  `take_snapshot` + `take_screenshot` via Chrome DevTools for the PR body, and the forge
  `frontend/design/verify` overflow probe on the replaced surfaces. The existing 11 Playwright specs
  stay green, `npm run lint:css` passes, and the extended `colorSchemeTokenContract.test.ts` (S0) passes.
- **Gates per repo:** reliant `make forge-generate-check` is a no-op after R0. control-plane `task ci` for
  R2c/R3s-cp. forge's own gate for F9-x. Nothing deploys to prod as part of verification.

**Recovery (roll forward):** no schema changes. A broken transport or hook wave rolls forward with a revert PR
for that one service; the waves are per service so this stays small. The R3 expand/contract means no deploy order
breaks a running client. A broken R2 config falls back to `config_gen.ts` defaults (prod's literal
endpoints are the proto defaults, so a missing `config.js` doesn't strand prod), and the fix is a KCL change.

---

## 11. Owner decisions (recommendations)

1. **D1: how to declare the frontend.** (a) `git mv web frontends/web` (**recommended**: forge's
   convention, discovered at load, so `forge lint`'s frontend lanes and the Taskfile template cover it.
   206 mechanical refs plus control-plane's path/subdir, in lockstep with `task pin:reliant`. reliant
   owes no backwards compatibility). (b) A reliant `deploy/kcl` env declaring `path = "web"`: smaller diff, but
   it's a deploy env written only for codegen, and only rendering commands see it, so `forge lint`
   doesn't. (c) Make forge admit a custom in-repo path at load (folds into F9-6's package.json block).
   That's viable, but it's a new forge surface whose only user would be reliant.
2. **D2: Electron config.js.** **Option A** (`app://` synthesizes it from main's resolved config) over B
   (static file at package time) and C (contextBridge, which collides with the dev `config.js`).
3. **D3: reason-header rollout.** **Expand/contract** (web reads both → servers switch → web drops
   the fallback) over a flag day. Old desktop builds degrade to a plain toast. Also delete
   `X-Reliant-Upgrade-URL` (it's constant).
4. **D4: hooks scope.** Adopt per service through waves A–C. Wave D (Chat, FileSystem, Forge,
   Daemon/DaemonRegistry/Token) is decided after C, and staying hand-rolled is acceptable where
   streaming owns the cache.
5. **D5: `ConnectClientError extends ConnectError` in forge** (**recommended**, F9-5) over migrating all 77
   reliant sites to `normalizeError` in one PR before R1.
6. **D6: Button variants.** forge has no `default`/`accent`/`gradient`. Recommended: `default→secondary`,
   `accent→primary`, and keep `gradient` as a reliant-only marketing variant outside `ui/`. This is a design call.
7. **D7: telemetry.** Delete reliant's `tracingInterceptor` in favour of the runtime's `traceInterceptor`
   (HyperDX is already the runtime's). Web Sentry stays until the separate ClickStack-replaces-Sentry
   work (`env.k:552`).

---

## 12. Reproducing

```bash
# forge at reliant's pin (in your own forge worktree — never a shared checkout)
git -C <forge-wt> reset --hard 1be0d294d2af && (cd <forge-wt> && go build -o /tmp/p9/forge ./cmd/forge)
# scratch copy with the frontend declared by the convention path
git -C <reliant-wt> archive origin/main | tar -x -C /tmp/p9/scratch
cd /tmp/p9/scratch && git init -q && git add -A && git commit -qm base
(cd tools/protoc-gen-es && npm ci) && cp -r <reliant-main>/web/node_modules web/
git mv web frontends/web && git commit -qm move
PATH=<dir with buf v1.57.0>:$PATH GOWORK=off GOTOOLCHAIN=$(sed -n 's/^toolchain //p' go.mod) /tmp/p9/forge generate
git status --short --untracked-files=all | grep -v node_modules        # §2 table
(cd frontends/web && npx tsc -p tsconfig.app.json --noEmit | grep -c 'error TS')   # 149
/tmp/p9/forge lint --frontend-stores                                   # clean
# token vocabulary vs component usage (F9-7) and raw palette (F9-8)
rg -o -- '--color-[a-z-]+' <forge>/internal/templates/frontend/vite-spa/src/index.css | sort -u
rg -o '\b(bg|text|border)-(accent|danger|success|warning|ink|surface|border|on)[a-z-]*' <forge>/pkg/components/components
rg -c '\b(bg|text)-(blue|gray|green|purple|pink|indigo|teal|orange|cyan)-[0-9]{3}' <forge>/pkg/components/components/ui
# web census
rg -o --no-filename 'import\.meta\.env\.VITE_[A-Z_]+' web/src | sort -u          # 21
rg -c 'instanceof ConnectError|ConnectError\.from' web/src -g '!*.test.*' -g '!gen/**'   # 77 in 51 files
rg -n 'x-reliant-reason' web/src -g '!*.test.*'
```
