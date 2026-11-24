# Public demo inventory fixtures

The public demo (`demo.reliantlabs.io`) is the standalone `WEB_BUILD_TARGET=demo`
bundle. It has no signed-in user, daemon, Supabase client or application backend.
Its default Connect transport answers from browser-local mock scenarios.

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
the wire, and in the demo shell (the only place `/forge/demo` runs; the app's
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

## The same pack in the production build

The shop pack also serves the **production** build, through fixture mode
(`FIXTURES_SCENARIO` in config.js, `src/lib/fixturesConfig.ts`). Fixture mode
adds `shop/app.ts`, the reliant services' layer, on top of `shopPack`, so chat,
projects and settings render too. It serves two surfaces:

- the visual harness, `e2e/visual`;
- the hosted preview env, `deploy/kcl/preview`. See `preview-env.md` for what it
  declares and how it is (not yet) deployed.

The demo bundle never loads that layer, and its bundle guard fails if it does.

## Verify the real target

From `frontends/web`:

```sh
pnpm install --frozen-lockfile
pnpm exec vitest run src/components/Forge/Demo src/lib/__tests__/chatMachine.demo.test.ts src/__tests__/demoShellRoutes.test.tsx
WEB_BUILD_TARGET=demo DEMO_BUNDLE_REPORT=/tmp/demo-bundle.json pnpm build
WEB_BUILD_TARGET=demo pnpm exec vite preview --port 4173
# Another terminal; screenshots stay outside the repository:
SCREENSHOT_DIR=/tmp/demo-inventory node scripts/demo/smoke-demo-inventory.mjs http://localhost:4173
```

The build's existing bundle guard is authoritative; never disable it to admit an
authenticated client import. The browser smoke loads an actual release detail,
opens config values and compares history, rejecting external/authenticated RPC
requests and uncaught errors. The demo twin export-parity test covers the Vite
development linker too: Rollup dead-code elimination can hide missing transitive
exports that break development route loading.

Working deep links (once the reviewed demo release is deployed):

- `/forge/p/shop/releases/v1.8.3?env=prod`
- `/forge/p/shop/workloads/dev/api?env=dev&tab=config`

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
guard (`vite-plugin-demo-bundle.ts`) is separate and unchanged: it decides which
modules may be in the bundle, and this script measures only its size.

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
re-derived from 355d77cbc under the same rule.

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
