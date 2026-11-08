# Public demo inventory fixtures

The public demo (`demo.reliantlabs.io`) is the standalone `WEB_BUILD_TARGET=demo`
bundle. It has no signed-in user, daemon, Supabase client or application backend.
Its default Connect transport answers from browser-local mock scenarios.

## One synthetic shop

`components/Forge/Demo/demoData/inventoryScenario.ts` answers the generated
`AppDatabaseService` browse RPCs and `IdentityService` reads. Every normal
environment declares one database, `shop`: the selector must not invent extra
connections. The tables are `public.users`, `public.chats`, `public.machines`
and `public.inventory`. Primary and foreign keys are explicit in both directions.
A user's `customer-*` primary key is deliberately different from their
`subject-*` identity-provider subject. Emails are reserved example.com addresses.

Browse requests use the real generated contract: column projection, ANDed typed
filters, SQL null semantics, LIKE/ILIKE wildcards, prioritized sorts, offset
paging and optional counts. They never evaluate SQL or call a backend. Prod
requires a matching confirmation and nonempty reason, to demonstrate the audited
workflow; `demo-local-read` identifies a synthetic read, not an audit server row.

`configScenario.ts` answers `GetWorkloadConfig` with immutable **fake** bundle
identities/digests. YAML and objects are projections of the same redacted tree.
The fixed `[withheld]` markers reveal neither values nor secret-derived hashes.
Bundle selectors are scoped to their environment, and historical release reads
produce a meaningful timeout change. `workloadScenario.ts` aligns the demo's
observability workload rows and declared bundle with `consoleScenario.RUNNING`,
the same current-promotion ledger used by config and release history.

## Verify the real target

From `frontends/web`:

```sh
pnpm install --frozen-lockfile
pnpm exec vitest run src/components/Forge/Demo/demoData/__tests__ src/lib/__tests__/chatMachine.demo.test.ts src/__tests__/demoShellRoutes.test.tsx
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
