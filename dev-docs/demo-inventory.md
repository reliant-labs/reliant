# Public demo inventory fixtures

The public demo (`demo.reliantlabs.io`) is the standalone `WEB_BUILD_TARGET=demo`
bundle. It has no sign-in, no daemon, no Supabase client and no application
backend. Its visitor is a fixed, signed-in sample person (Alex Demo) with no
credential, so the app's own pages (chats, Inbox, Workflows, Connectors,
Sandboxes) render as they do for a signed-in user. Its one Connect transport
answers every RPC from browser-local mock scenarios, and refuses writes.

## One synthetic shop, one story

Every answer the demo gives comes from one module,
`fixtures/shop/shopStory.ts` (the shop fixture pack, `src/fixtures/shop/`): the cast (jon, maya, priya, and
CI as `github-actions`), shop's customers, its services (`api`, `worker`,
`inventory` and the static storefront `shop-web`; Postgres, NATS and the
payment provider are dependencies, never services), the release ledger (each
release one commit, each promotion one promoter and one time), the one
incident (prod's v1.8.3 payment timeouts on api), the traffic model every
aggregate is computed from (`demoStats`), and the database's tables. `demoNow()`
anchors every relative time to the page load.

`fixtures/shop/model/shopApi.ts` assembles the story's model, in the shape of
the console's `ObservabilityApi`, from one module per area (telemetry via
`busyApp.ts`, graph, workloads, panels, delivery, issues and release health,
alerting, sessions, dashboards, metrics). It is the authoring format, not what
pages read: the handlers (`handlers/observability.ts`, `alerting.ts`,
`delivery.ts`, `issues.ts`, `user.ts`, `rum.ts`, `dashboards.ts`) encode it on
the wire, and in the demo (the only place `/forge/demo` runs; the app's
leaves for it) the console's real client (`controlPlaneObservabilityApi`)
decodes it, so no page reads the model directly.
`__tests__/realClient.test.ts` holds every read the real client makes to the
model's answer. `handlers/observabilityBilling.ts` answers observability billing
Unimplemented: the demo has none. `handlers/console.ts` answers Releases, Environments, Secrets, Domains
and Activity from the same ledger. Nothing under `src/fixtures/` imports the
console's unit-test fixtures (`Observability/__fixtures__`);
`__tests__/coherence.test.ts` fails if one does, or if any page's answer
contradicts the story.

`model/security.ts` and `model/tests.ts` hold the Security and Tests data in
the same ledger; `handlers/security.ts` and `handlers/tests.ts` answer the
control plane's VulnerabilityService and TestsService RPCs from it over the
demo transport, so the pages' default API clients run unchanged.
Security: each release's SBOMs for `api`, `worker`, `inventory` and
`shop-web` (from its lockfile), the findings in the release each environment
runs, prod's enforced gate and staging's audited one, an exemption with a
reason and an expiry, and the connect-go advisory v1.8.3's dependency bump
fixed. Tests: CI's runs on each release commit, the coverage trend with the
releases marked, maya's failed branch run that reproduced the incident, and
`TestCreateOrder_RetryOnTimeout`, the flaky test v1.8.3's payment retry
introduced. The Coverage page's source view reads one bundled file (v1.8.4's
`internal/orders/charge.go`) and never fetches.

The demo leaves out what the shop has nothing to show on: Usage (a sample app
has no hosting bill). It has no nav row and no tip, and its URLs land on the
Overview (`DEMO_HIDDEN_PAGES`, `Demo/demoPath.ts`). Speed insights is served:
`handlers/speedInsights.ts` answers its three SQL panels (the score and vitals,
the routes table, the series) over the shop's routes, on desktop and mobile.

`handlers/inventory.ts` answers the generated `AppDatabaseService` browse RPCs
and `IdentityService` reads. Every normal environment declares one database,
`shop`: the selector must not invent extra connections. The tables are
`public.customers`, `public.products`, `public.stock`, `public.orders` and
`public.order_items` (`shop/database.ts`, relative to the page load; only prod
has `payment_failed` orders, all since the incident). Primary and foreign keys
are explicit in both directions. A customer's `cus_*` primary key is
deliberately different from their `sub_*` identity-provider subject. Emails are
reserved example.com addresses.

Browse requests use the real generated contract: column projection, ANDed typed
filters, SQL null semantics, LIKE/ILIKE wildcards, prioritized sorts, offset
paging and optional counts. They never evaluate SQL or call a backend. Prod
requires a matching confirmation and nonempty reason, to demonstrate the audited
workflow; `demo-local-read` identifies a synthetic read, not an audit server row.

`handlers/config.ts` answers `GetWorkloadConfig` with immutable **fake** bundle
identities/digests. YAML and objects are projections of the same redacted tree.
The fixed `[withheld]` markers reveal neither values nor secret-derived hashes.
Bundle selectors are scoped to their environment, and historical release reads
produce a meaningful timeout change. `shop/workloads.ts` answers the
observability workload rows from the same current-promotion ledger used by
config and release history.

## One shell, one nav

The demo has no frame of its own. Every page, the demo's own (`/forge/demo?section=…`),
the console's (`/forge/p/shop/…`) and the app's Agents pages, renders in the signed-in
app's shell (`Layout/shell/AppShell.tsx` `AppShellFrame`, since #3519):

- **The frame** is drawn for a public, read-only host (`AppShellFrame publicPreview`)
  under a fixed session (`demoShellRoutes.tsx` `DEMO_SESSION`, through the session port
  `lib/session`, never the auth store): the shop story's Alex Demo
  (`fixtures/shop/user.ts` `APP_USER`), signed in, with no token (`getToken` resolves
  null). It is a name to show, never a credential: no sign-in code is in the bundle.
  The frame loads as its own lazy chunk (`Forge/Demo/DemoShellFrame.tsx`), so it stays
  out of the entry chunk.
- **The nav** is the app's nav column with its live chat slots
  (`Layout/ChatShellNav.tsx`, the code the app's `Layout/Sidebar.tsx` also draws) and the
  demo's config, `Forge/Demo/demoNavConfig.ts`: the app's own items (`navConfig/items.tsx`)
  in the app's order, less the items named in `DEMO_EXCLUDED_IDS` (`usage-billing`,
  `slot-help`, `settings`: account-only), plus Home and "How it was built".
  `demoNavConfig.test.ts` pins it as `hybridNav` minus exactly that list, the same item
  objects in the same order, so an item the app adds shows up in the demo or the test
  fails. The pages the demo does not serve (`DEMO_HIDDEN_PAGES`) are pruned from it
  (`navConfig/prune.ts`). Inbox and Agents are in it: the chat list, New chat, the Inbox
  count and the Workflows pill read the shop's data.
- **The workspace switcher** names the sample org, "Acme · Starter"
  (`hooks/useOrgs.demo.ts`, `components/Orgs/orgPlan.demo.ts`). There is one org and
  nothing to switch to.
- **Find** opens the demo's command palette (`Forge/Demo/DemoCommandPalette.tsx`): the
  app's palette less the commands for Usage & billing and Settings.
- **The app-only parts** the demo leaves empty are the Setup Guide (`guide`) and the
  app's modal layer.
- **The console's chrome** is `Forge/Console/ConsoleFrame.tsx` (the breadcrumb and the
  environment scope over the page), the same as in the app. The demo's own additions
  (its agent cards, plain issue copy, section crumbs) are a `LedConsoleHost`
  (`demoBundle/DemoConsoleProviders.tsx` `DemoConsoleHost`).
- **One anchor set.** The tour and the tips ring the shell's own anchors
  (`Onboarding/JOURNEYS.md`, "Anchors"), so the demo and the app share them.

After #3574 the console has no second frame: `ConsoleShell`, its grouped nav and the
led console's nav half are deleted (until it merges they are unreachable code), and
the dev `__preview__` pages wear the same shell (`Forge/__preview__/PreviewShellFrame.tsx`).

## The app's Agents pages in the demo

`src/demoAgentsRoutes.tsx` mounts the app's own pages under the demo's unguarded layout,
at the paths `routes.tsx` serves them at (each a lazy chunk, so the first page never
downloads the chat client):

| Path | Page |
|---|---|
| `/project/$projectId`, `/c/$chatId`, `/c/$chatId/t/$threadId`, `/new` | the chat pages (`App.tsx`): a project's chats, a transcript, one thread, the new-chat view |
| `/inbox` | the Inbox |
| the Workflows area (`workflowsAreaRoutes.tsx`) | the library, a workflow, runs, a run |
| `/connectors` | integrations and MCP servers |
| `/settings/machines[/$machineId]`, `/machines` | Sandboxes: `Settings/cloud/machines.tsx`'s `MachinesSection` mounted alone; the rest of Settings is the account's and is not in the demo |

`/` stays the demo's landing. Agents in the nav goes to `/project/<shop project id>`.
There is no `/o/$org` tree. The pages are the app's own, so they are the app's code
running against sample data: any module they reach that needs sign-in, a token or
billing has a demo twin (`x.demo.ts(x)` beside `x.ts`, `demoSubstitutes()` in
`vite-plugin-demo-bundle.ts`) answering what the demo needs: nobody signed in, no
funding prompt, no billing, writes impossible. Examples are `api/transport.demo.ts` (no
interceptor chain; every RPC goes to the demo's one transport), `useCloudBillingQueries.demo.ts`
and the `Billing/*.demo.ts(x)` pieces.

## The shop's app layer, read-only

The demo's data is the shop pack plus its **app layer** (`fixtures/shop/app.ts`
`shopAppPack`): the reliant services' answers (projects, worktrees, chats and their
transcripts, the open streams, workflows and runs, connectors, settings, machines),
billing and the account. `Forge/Demo/demoTransport.ts` serves it through
`createShopAppTransport`, read-only: only an RPC whose proto declares
`NO_SIDE_EFFECTS`, plus the packs' named `answers` (`APP_LAYER_ANSWERS`), is answered.
Every other RPC is refused with the read-only message, which the read-only layer
(`ObsReadOnlyProvider`) shows as a toast. `window.__demoRpcs` records what was refused
and what had no sample answer.

With `DEMO_DATA_SOURCE=remote` and a `DEMO_DATA_URL`, an RPC the app layer answers
(`appLayerMethods()`) is still answered in the page. Every other RPC goes to the
gateway, which is asked only for `NO_SIDE_EFFECTS` reads, and falls back to the sample
data after `REMOTE_TIMEOUT_MS`.

`fixtures/shop/__tests__/appLayer.test.ts` pins the boundary: the demo **pack**
(`fixtures/shop/index.ts`) never imports the app layer, and the app layer never reaches
fixture mode's install or its fake Supabase and session (`fixtures/install.ts`,
`outside.ts`).

## The same pack in the production build

The shop pack also serves the **production** build, through fixture mode
(`FIXTURES_SCENARIO` in config.js, `src/lib/fixturesConfig.ts`). Fixture mode adds
`shop/app.ts` on top of `shopPack` too (the same app layer the demo serves), but installs
it with `src/fixtures/install.ts` and a fake Supabase (`outside.ts`) so the real sign-in
code accepts a fixed session. It serves two surfaces:

- the visual harness, `e2e/visual`;
- the hosted preview env, `deploy/kcl/preview`. See `preview-env.md` for what it
  declares, where it is hosted (a control-plane prod frontend), and the tests
  that prove its sign-in code is inert.

The demo bundle never loads fixture mode's install, its fake Supabase or its session,
and its bundle guard fails if it does. The difference between the two is the guard's
line: the preview is prod's bundle (sign-in code present, provably inert); the demo is
a bundle with no sign-in code at all.

## Verify the real target

From `frontends/web`:

```sh
pnpm install --frozen-lockfile
pnpm exec vitest run src/components/Forge/Demo src/__tests__/demoShellRoutes.test.tsx src/fixtures/shop/__tests__/appLayer.test.ts vite-plugin-demo-bundle.test.ts
WEB_BUILD_TARGET=demo DEMO_BUNDLE_REPORT=/tmp/demo-bundle.json pnpm build
WEB_BUILD_TARGET=demo pnpm exec vite preview --port 4173
# Another terminal; screenshots stay outside the repository:
SCREENSHOT_DIR=/tmp/demo-inventory node scripts/demo/smoke-demo-inventory.mjs http://localhost:4173
```

The build's bundle guard is authoritative (see "The bundle guard" below); never
disable it to admit a sign-in or account module. The browser smoke loads an actual
release detail, opens config values and compares history, rejecting external/authenticated
RPC requests and uncaught errors. `scripts/demo/smoke-demo-site.mjs` (the site smoke,
`forge-demo-cutover.md`) also walks the Inbox and the Agents pages and opens a chat
transcript. The demo twin export-parity test covers the Vite
development linker too: Rollup dead-code elimination can hide missing transitive
exports that break development route loading.

Working deep links (once the reviewed demo release is deployed):

- `/forge/p/shop/releases/v1.8.3?env=prod`
- `/forge/p/shop/workloads/dev/api?env=dev&tab=config`

## The bundle guard

`frontends/web/vite-plugin-demo-bundle.ts` fails `WEB_BUILD_TARGET=demo` when a module
the demo must not contain reaches any chunk. The line it draws is **no sign-in or account
code**, not "no Reliant app code": chat, Inbox, Workflows, runs, Projects and the Machines
views are allowed, because the demo mounts them. The forbidden list is `DEMO_FORBIDDEN`
(one `demoForbiddenRule` per module id):

- Supabase (`node_modules/@supabase/`, `src/lib/supabase.ts`) and `src/lib/auth/`
  (except the import-free `apiKeyUser.ts`), the auth store and provider
  (`store/authStore.ts`, `api/authProvider.ts`), and the sign-in screens and guards
  (`components/Auth/`, `AuthGuard`, `AuthInitializer`, `AuthLayout`, `AuthScreen`);
- the app's roots and route tree (`routes.tsx`, `components/App.tsx`, `components/Root.tsx`),
  which mount sign-in; the demo has its own tree;
- the claim flow (`components/Claim/`) and all of `components/Billing/`;
- the Settings page (`components/Settings/`) and its route files
  (`settingsSectionRoutes.tsx`, `settingsConnectorRoutes.tsx`), except a named list
  of files: chat display preferences, the provider usage chip's display pieces,
  Connectors' integrations and MCP tab, the project panel's details card and
  Sandboxes' machine list and cards (`Settings/cloud/machines.tsx` and its pieces);
- Reliant's onboarding (`components/OnboardingFlow/`, the wizard and the checklist),
  except three chat-setup decision modules (`onboardingGatePolicy.ts`, `usableDaemon.ts`,
  `useChatSetup.ts`).

A module with a demo twin (`x.demo.ts(x)`) is the demo's own code and never matches.
When an allowed module imports a forbidden one, the build names each crossing
(`importer → banned module (why)`); `DEMO_BUNDLE_REPORT=<file>` has the importer chain.
Cut a crossing in this order: (1) a module that only needs "is someone signed in, and who"
reads the session port, `@/lib/session`; (2) otherwise add a twin of the highest module
that keeps the rest of the UI intact. Never widen an `except` list to get past one. The
guard's own header says why the line is where it is: a public page carrying credential
handling is one config mistake from using it, and can no longer be proved inert by
reading the bundle.

## Bundle size

The demo bundle has a size budget, `frontends/web/scripts/demo/bundle-budget.json`.
`frontends/web/scripts/demo/bundle-size.mjs` measures a build from its
`DEMO_BUNDLE_REPORT` and its `index.html`:

- **initial**: the entry script plus every `modulepreload` and stylesheet link
  in the built `index.html`, the build's own `assets/` files only (not the
  host's `/config.js`). This is what the first page load fetches.
- **total**: every JS and CSS file the build emits.

Gzip is level 9, and brotli uses zlib's defaults. The budget caps initial gzip
and total gzip.

```sh
task demo:bundle-size                                   # build, measure, fail over budget
task demo:bundle-size -- --report-only                  # the table alone, for a PR body
task demo:bundle-size -- --report-only --json-out /tmp/before.json   # on main, then on the branch:
task demo:bundle-size -- --report-only --before /tmp/before.json     # before, after, and the change
```

It builds the demo as forge ships it (`WEB_BUILD_TARGET=demo`) into a temporary
directory, so `dist-demo/` is left alone. **It gates the release gate only**
(its `demo-size` step). It never runs in the merge gate or the merge queue. A
PR that moves the demo pastes its before/after table into the body. The bundle
guard (`vite-plugin-demo-bundle.ts`, above) is separate: it decides which
modules may be in the bundle, and this script measures only its size.

**The widened demo (chat, Inbox, Workflows, Connectors, Sandboxes).** The app's chat
client is far larger than the console, so the ceilings were re-derived on purpose, not
nudged. The rule is what held the line: every Agents page is a lazy chunk
(`demoAgentsRoutes.tsx`), so the *initial* ceiling stays near the console-only
baseline, and the chat client moves only the *total* ceiling. The table below is the
budget file's new baseline.

This branch (on main 9098331dd2), 2026-11-27, `task demo:bundle-size -- --report-only --before <main's --json-out>`:

| Demo bundle | Initial raw | Initial gzip | Initial brotli | Total JS+CSS raw | Total gzip |
|---|---|---|---|---|---|
| before (main 9098331dd2) | 1,783.8 KiB (32 files) | **486.1 KiB** | 416.6 KiB | 20,154.3 KiB (290 files) | **5,105.2 KiB** |
| after | 1,850.8 KiB (81 files) | **519.8 KiB** | 449.5 KiB | 23,044.9 KiB (445 files) | **5,987.1 KiB** |
| change | +66.9 KiB | +33.7 KiB | +32.9 KiB | +2,890.6 KiB | +881.9 KiB |
| new budget | | ≤ 550 KiB | | | ≤ 6,050 KiB |

The initial load grew 7%, not 0: the Agents route definitions
(`demoAgentsRoutes.tsx`, `workflowsAreaRoutes.tsx`, `sandboxesAliasRoute.tsx`) and
the fixed visitor are in the entry, the `vendor-ui` group carries every lucide
icon the whole graph imports (about 90 more, used only by the lazy Agents pages),
and rolldown re-split the shared chunks into more, smaller files (gzip is counted
per file). Taking lucide out of `vendor-ui` for the demo saved only ~8 KiB of
initial gzip and doubled the file count (556), so it was not kept.

The console-only baseline, before the Agents pages (kept for the comparison):

Main 355d77cbc, 2026-11-24 (clean `pnpm install --frozen-lockfile`):

| | Initial raw | Initial gzip | Initial brotli | Total JS+CSS raw | Total gzip |
|---|---|---|---|---|---|
| main 355d77cbc | 1,778.0 KiB (34 files) | **484.4 KiB** | 414.6 KiB | 20,029.4 KiB (287 files) | **5,064.0 KiB** |
| budget | | ≤ 510 KiB | | | ≤ 5,120 KiB |

`vite build --mode demo` measures 0.1 KiB more initial gzip (484.5 KiB),
because `import.meta.env.MODE` differs.

**Moving a ceiling.** Each ceiling is the baseline plus a small margin: initial
gzip about +5% and total gzip about +56 KiB, rounded up to 10 KiB. The budget
file records the baseline commit, its numbers and the rule. To move a ceiling,
measure main, apply the rule, update the baseline and both ceilings together,
and give the reason in the PR. Do not raise a ceiling just to let one change
through. The phase-3 plan (reliant#3462) first proposed 645 / 5,080 KiB from
main 49556b0ba (621.3 / 5,026.7 KiB). On 2026-11-24 the ceilings were
re-derived from 355d77cbc under the same rule. They were re-derived again when the
demo gained the Agents pages (the budget file's `baseline` is the current one).

## Follow-up integration dependencies

This change is independently buildable against main. At implementation time,
Data UI #2339, Storage UI #2364, Identity cross-links #2382 and dashboard saved
views #2378 remain unmerged. It does **not** claim those pages are available on
the demo yet. Shared page registry/navigation and browser route assertions must
land with those reviewed pages; no parallel duplicate UI is added here.

Identity metadata control-plane #2183 is merged, but this checkout's generated
client predates `IdentitySubjectKind`. The synthetic subject column carries the
explicit comment marker already; its typed metadata needs normal vendoring once
the cross-link dependency is integrated. Never fabricate a proto enum or edit
generated code. Storage's five RPC fixtures likewise wait for the merged backend
contract (#2150), normal vendoring, and reviewed UI integration. No live deploy
of unreviewed branches is part of this change.
