---
name: frontend
description: Frontends with or without a backend — Next.js static exports, Vite SPAs, Expo: the component library, Tailwind v4, build shapes, runtime config, visual verification, and generated Connect RPC hooks when there is an API.
---

# Frontend Development in Forge

## Project structure

No backend needed: with no services it is a static site (`deploy/static-site`) and all but the hooks applies. Each frontend lives in `frontends/<name>/` — by default a Next.js App Router app (`forge scaffold frontend <name>`): `src/app/`, `src/components/`, `src/hooks/`, `src/lib/`. Generated TypeScript lives in this frontend's own `src/gen/` — import it as `@/gen/...`, never from the project-root `gen/` (that tree is Go).

`--kind` also takes `vite-spa` and `mobile` (Expo React Native: `app/` screens; events gain `app:background`/`app:foreground`). `--output`/`--base-path` are web-only.

## Build & serving shapes

`next.config.ts` picks the production shape: `static` (the default, a
static export into `out/` that hosted static hosting serves), `standalone` (a
Node server), or `server`. **Every route must be static**: detail and edit are
`/<entity>/view?id=…` and `/<entity>/edit?id=…` (`src/lib/entity-routes.ts`),
never `[id]` (`frontend/pages`). `base_path: /admin` mounts under a URL
prefix; hand-built URLs then go through `joinBasePath`. Both are in
`frontend/serving`. Each env binds the frontend's `runtime` (`deploy` skill).

## Generated TypeScript hooks

`forge generate` writes per-service React Query hooks to `src/hooks/`: reads get `useQuery`, paged reads also `use<Rpc>Infinite`, writes `useMutation`; outside React, `fetchQuery(<svc>Queries.<rpc>(req))`. Import from `@/hooks`. `useApiQuery` / `useApiMutation` cover composite operations. Kinds, `select`, paging, rollback: `proto`.

## The component library

**Search it before you hand-write any UI.** 74 components ship across layouts, charts, diagrams, deck, and ui:

```bash
forge component search card      # by keyword — try the noun you were about to build
forge component list             # the whole catalog, grouped by category
forge component install card_grid
```

**What is already in `src/components/ui/` is not the catalog** — it is the 31 the scaffold auto-installs. Search the catalog; never conclude from the directory.

**Every component already uses this project's theme tokens** (`ink`, `surface`, `border`, `accent`, `danger`, `success`, `warning` and their `-surface`/`-border`/`-ink` variants), so installing one never means re-theming it. A raw palette class (`bg-blue-600`) in one is a forge bug to report, not a reason to hand-write your own.

**✓ already ships in `src/components/ui/`** — import it, do not rebuild it; the rest are one `forge component install` away:

| About to build | Use |
|---|---|
| A stat / KPI block | ✓ `stat_grid`, ✓ `metric_card` |
| "No results yet" copy | ✓ `empty_state` |
| A destructive-action prompt | ✓ `confirmation_dialog` |
| A sortable/dense table | ✓ `data_table` |
| A filter/search bar for a list page | ✓ `filter_bar` |
| A board, timeline, tile grid | `kanban_board`, `timeline`, `card_grid` |
| A conversion / stage funnel | `funnel_chart` |
| A menu, context menu, picker, breadcrumb, toggle, anchored panel, tooltip | `dropdown_menu`, `context_menu`, `combobox`, `breadcrumb`, `toggle_switch`, `popover`, `tooltip` |

A search that returns nothing is a real answer: there is no drawer/sheet or quantity-stepper, so those you write yourself. (The `component_library` MCP tool is the same catalog.)

**Data viz: use Recharts, not the library.** For commodity charts (bar, line, pie, sparkline…) `npm i recharts`. The library ships only **narrative** charts (`quadrant_chart`, `concentric_circles`, `funnel_chart`) and `slide_*` deck charts. Dashboard → Recharts; slide / marketing page → check the library first.

### Base UI primitives (always available)

Every frontend ships low-level primitives under `src/components/ui/`. Compose these instead of inlining `<button>`/`<input>`/`<table>` markup:

| Primitive | What it is |
|-----------|-----------|
| `button` | `primary`/`secondary`/`outline`/`ghost`/`danger`, `xs`–`xl`, `leftIcon`/`rightIcon`, icon-only, `loading`, `href` |
| `input`, `label`, `form` | Text input, field label, and `Form`/`FormField`/`FormError`/`FormActions`. `<FormField>` mints an id via `useId()`, so child `<Label>`/`<Input>`/`<Select>` auto-bind without `htmlFor`/`id` |
| `textarea`, `toggle_switch` | On demand: `forge component install textarea toggle_switch`. ToggleSwitch is `role="switch"` with `label` or `ariaLabel`; both auto-bind inside `<FormField>` |
| `card` | Generic surface primitive |
| `status`, `tag`, `mono` | State (dot + word), kind label, copyable sha |
| `avatar`, `tabs`, `chip` | Avatar, tab nav (underline/pills/boxed), filter chip. `Tabs`/`FilterBar`: pass `activeTab`/`values` to control them from the URL |
| `table` | Bare structural table. For a wired list view use `<Resource>` (see `frontend-runtime`) |
| `select` | Options array, sizes, invalid state |
| `toast_notification` | The toast STACK, rendered once by the owned `providers.tsx`. Never call it directly — fire `emitToast({ message, variant })` from `@/lib/events` |

Plus auto-installed: `sidebar_layout`, `page_header`, `badge`, `modal`, `skeleton_loader`, `pagination`, `search_input`, `alert_banner`, `key_value_list`, `login_form`. All `overwrite: once` — yours after install.

Two carry aliases; write against canonical. **`Badge`** — `error`/`success`/`warning`/`info`/`neutral`, aliases `danger→error`, `default→neutral`. **`Modal`** — canonical footer is the **`footer` slot prop**; rewrite footer-in-`children` when you next touch it. (`Button`'s destructive variant is `danger`, not `error`.)

### Owned higher-level components

Owned scaffold (not `ui/`), composing the base primitives — yours to edit or delete:

- **`status-badge.tsx`** / **`enum-select.tsx`** — proto-enum badge + select. See **Formatting** below for the enum-object rule and status colors.
- **`entity-picker.tsx`** / **`entity-name.tsx`** — the foreign-key pair: searchable select over a generated LIST hook, id→display-name over a GET hook. Never hand-roll a per-entity picker. See `frontend/pages`.
- **`src/components/session_nav.tsx`** (Next.js) — signed-in user + sign-out, on `useAuth()`. No login form or `/login` route ships; sign-in is your IdP's. See `auth`.

Typed list views are NOT owned scaffold: the generated CRUD pages import `<Resource>` from `@reliantlabs/forge-web-runtime`, which owns the loading/error/empty/data ladder and cursor pagination (`frontend-runtime`).

## Formatting: `@/lib/format-utils`

Scaffolded into every frontend. **Import these — never re-derive them per feature.**

| Need | Export |
|---|---|
| Money (`int64` cents) | `formatMoneyCents(cents)` → `$272,000.00` |
| Money — headline, no cents | `formatMoneyWhole(cents)` → `$272,000` |
| Recurring price | `formatMoneyInterval(cents, "month")` → `$29.00/mo` |
| Proto `Timestamp` → `Date` | `timestampToDate(ts)` — null when unset/out of range |
| Date, date+time | `formatDate(ts)`, `formatDateTime(ts)` |
| Elapsed time | `formatAge(ts)` → `"3 days ago"` |
| Enum → label | `enumLabel(value, EnumObject)` → `"Inspection Scheduled"` |
| Enum → `<select>` options | `enumOptions(EnumObject)` |
| Any column value | `formatValue(v)` |

Unset renders `—` throughout. Pass the enum **object**, never its type name — protobuf-es enums are runtime numbers and only the object reverse-maps.

**Money form fields, exact amounts in any currency:** `formatMinorUnits` / `parseMinorUnits` / `minorUnitsToInput` from `@reliantlabs/forge-web-runtime` (`bigint` minor units; `null`, never rounding), and `formatBasisPoints` / `parseBasisPoints` / `basisPointsToInput` for rates.

**Status colors:** `<StatusBadge value={x.status} enumType={X}>` resolves a variant from a built-in generic-lifecycle map (`active`, `paid`, `failed`, …). Declare your product's own words once at module scope — never edit the built-in map or write a per-feature color record:

```ts
registerStatusVariants({ weather_hold: "warning", emergency: "error" });
```

An unregistered status renders neutral, never a guessed color.

## Connect RPC clients + mock mode

Import the generated transport from `src/lib/connect.ts`; for direct calls `createClient(MyService, transport)`. It talks to the REAL backend through `src/lib/api-base.ts` (`frontend/serving`). Mock mode is **opt-in**: set `mock_api` in `deploy/kcl/<env>/config.k`, then `forge generate`.

| `mock_api` | RPCs | Signed-in user |
|---|---|---|
| `""` (default) | Real backend. | `GET /auth/session` |
| `"true"` | Fixtures only; "MOCK DATA" banner. | Fixture `dev@localhost`, no `/auth/session`, so guards pass. |
| `"hybrid"` | `?scenario=` handlers over the real backend. | Same fixture, UI only: forwarded RPCs carry just the real cookie. |

**Never remove the mock banner from the layout** — it stops a working-looking UI masquerading as a working stack.

### Where the mock pipeline lives

The engine is library code (`@reliantlabs/forge-web-runtime/mock-transport`, a subpath so production bundles shake it out). Your project keeps:

- **`src/lib/mock-transport_gen.ts`** (Tier-1) — gathers the per-entity `src/mocks/entities/*_gen.ts`.
- **`src/mocks/<entity>_gen.ts`** — the fixtures, the same rows `forge db seed apply` writes.
- **`src/mocks/scenarios/*.ts`** — your scenarios; forge only regenerates the `index_gen.ts` barrel.

Dispatch: scenario handler → hybrid passthrough → fixtures → `Unimplemented`. A Get miss is `NotFound`. Writes persist until reload. A fixture List mirrors the backend's: each SET `optional` request field filters by equality (enums by value), `search` matches strings and enum names, `order_by`/`descending` sort, `page_size` pages, and `total_count` is the filtered count. Custom RPCs need a scenario.

To stub an RPC, run `forge scaffold scenario <name>` and add a typed handler — do NOT edit `mock-transport_gen.ts`. Activate with `?scenario=<name>`; it is read once at module init, so client-side navigation keeps it active until a full reload.

## Runtime config and backend URLs

A frontend reads its per-env values from `src/lib/config_gen.ts` (`loadConfig()`), which reads `window.__FORGE_CONFIG__`. That global is set by **`<basePath>/config.js`**, a classic blocking `<script>` in the shell's `<head>`: `layout.tsx` for Next.js, `index.html` (`%BASE_URL%config.js`) for Vite. Values arrive at runtime, so one bundle promotes across envs: never read `NEXT_PUBLIC_*` / `VITE_*` for a per-env value (the bundler freezes it).

Two sources are layered into the document, key by key:

1. **Typed config**: the `(forge.v1.frontend_config)` proto message, with per-env values in `deploy/kcl/<env>/config.k`.
2. **`runtime_config`** on the `forge.Frontend`, which wins on the same key; an unread key fails the render (`frontend-runtime`). Use it when a value is **another workload's URL**:

```kcl
forge.Frontend {
    name = "web"
    runtime = forge.OnHost {}
    runtime_config = {
        API_URL = forge.WorkloadURL { workload = "api" }   # a sibling workload's public URL
        APP_NAME = "acme"                                   # or a literal
    }
}
```

Name the workload, never the hostname:

| Env | Who resolves `forge.WorkloadURL` | Who writes `config.js` |
|---|---|---|
| **Hosted** (the referrer is a frontend or a workload bound to `forge.OnHosted`) | The control plane: it allocates hostnames and rewrites the document when one moves. | The control plane, after every StaticSite sync (the release carries none). |
| Everything else (host, compose, cluster, `OnFirebase` / `OnBucket`) | forge, **at render time**: a route to the workload → a custom domain on its exposed port → its host `listen_ports` → a frontend's `port`. | forge: `public/config.js` in dev, per env on a Firebase / bucket deploy. |

Unknown names are refused at load; off hosting, so is a URL forge cannot know at render time (a host service without `listen_ports`).

A hosted site's own hostname is not a field: `forge domain add` (see `deploy`).

The backend half uses the same reference. A workload's `CORS_ORIGINS` names the site, not its URL:

```kcl
wl.api | {env = {CORS_ORIGINS = forge.WorkloadURL {workload = "web"}}}
```

## Protobuf-ES v2

Forge uses protobuf-es v2 — create message instances with `create()`, never constructors:

```typescript
import { create } from "@bufbuild/protobuf";
import { CreateTaskRequestSchema } from "@/gen/services/tasks/v1/tasks_pb";
const req = create(CreateTaskRequestSchema, { name: "My Task" }); // NOT new CreateTaskRequest({...}) — that's v1
```

## Styling: Tailwind v4

**Tailwind CSS v4** — no `tailwind.config.js`; configure in CSS with `@theme`; import with `@import "tailwindcss"` (not `@tailwind base/...`); the PostCSS plugin is `@tailwindcss/postcss`.

```css
/* src/app/globals.css */
@import "tailwindcss";
@theme { --color-brand: #3b82f6; --font-sans: "Inter", sans-serif; }
```

Prefer `@theme` tokens over one-off colors; keep global CSS small. `npm run lint:styles` catches `!important` and invalid v4 at-rules. Full rules in `frontend/patterns`.

## Visual verification

**Use BOTH `take_snapshot` AND `take_screenshot` (Chrome DevTools) before declaring frontend work complete.** The a11y snapshot misses layout shifts, wrong colors, z-index and overflow; screenshots catch them. Screenshot at multiple breakpoints. `frontend/design` carries the overflow probe.

## Component patterns

Functional components with hooks only. Prefer **server components**; add `"use client"` only for interactivity, browser APIs, or hooks. Handle Connect errors by code (`err instanceof ConnectError`, switch on `err.code`). Every data-fetching component must handle **loading**, **success**, and **error**.

## Files NOT to edit

Regenerated by `forge generate` — edits overwritten: `src/gen/`, `src/lib/connect.ts`, `src/lib/basepath_gen.ts`, `src/hooks/*-hooks.ts`. Put custom code in separate files (`src/hooks/custom-hooks.ts`).

## Files that are starter code (rewrite expected)

Everything carrying the `// yours: scaffolded once` banner is written once and left alone — `src/app/page.tsx`, `src/app/auth/sign-in/page.tsx`, and every generated list/detail/create/edit page under `src/app/<entity>/`. `src/components/nav.tsx` and `src/app/dashboard.tsx` import generated per-entity registries, so forge never edits them.

None of it is a design — it is Rails-style scaffolding built from the entity list, enough to prove hooks, transport and auth are wired. Replacing it is expected: real copy, a hierarchy built around what the product does, and chosen empty/loading/error states.

Load `frontend/design` before building the real thing; it asks for a brief first and will not proceed on taste alone.

## Scaffolded systems (yours to extend)

Created by `forge scaffold frontend`, yours to modify:

- **Auth** (`src/lib/auth/`) — native sign-in: the browser POSTs credentials to this app's own API and gets an HttpOnly session cookie; the server runs the OIDC flow. `useAuth()` gives `{ identity, isAuthenticated, isLoading, refresh, logout }` — an identity for rendering, and deliberately no token. See `auth/frontend`.
- **Event bus** (`src/lib/events.ts`; hooks in `src/lib/use-event.ts`) — typed pub/sub for imperative cross-cutting actions (`toast:show`, `auth:expired`, `navigate`). Extend `EventMap`; use `useEvent(name, handler)`. Not a source of truth.
- **UI store** (`src/stores/ui-store.ts`) — Zustand baseline for client state (`sidebarCollapsed`, `commandPaletteOpen`). Add domain stores in `src/stores/` (rules below).
- **`src/lib/format-utils.ts`** — see **Formatting**.
- Also scaffolded: `src/lib/admin-url.ts` (`adminUrl`/`absoluteAdminUrl` over `basepath_gen.ts` — use these or `joinBasePath` for any string handed to an external system that round-trips back).

## Dev workflow

```bash
forge env up dev   # infra + services (hot reload) + frontends; reads deploy/kcl/<env>/
forge generate     # After any .proto change
```

`forge env up` dev-serves each declared frontend via `npm run dev` (`npm run build` is for `forge build` / `forge env deploy`). Each serves on its own KCL-declared port (`forge.Frontend.port`), **force-injected as `PORT`** into the child (a stale shell `PORT` never wins). There is no dev reverse proxy. To route a service under the prod Gateway, declare an `HTTPRoute` with a `host:` in `deploy/kcl/<env>/main.k`.

## File naming inside `frontends/<name>/src/`

- **Components under `src/components/ui/`** are `snake_case` (`data_table.tsx`, `toast_notification.tsx`), each default-exporting one PascalCase component.
- **Hooks, lib utilities, stores** are `kebab-case` (`use-api-query.ts`, `ui-store.ts`).

For the full Go / proto / TS / `forge.yaml` casing table, see `architecture` → **Naming conventions**.

## Sub-skills & rules

Load **adopt** (generating into an app forge did not scaffold; the package.json `"forge"` block), **state** (decision table, state vs events, ownership), **patterns** (composition, container/presentational, effects, typed boundaries), and `frontend-testing` (what to test, the `mockTransport()` seam, per-layer recipes).

- Never hand-edit generated files; run `forge generate` after every `.proto` change.
- Always `create(Schema, {...})` for protobuf messages, never `new Message()`.
- `forge component search <noun>` before hand-writing any UI. Recharts for dashboard charts.
- Money, dates, enum labels, status colors: `@/lib/format-utils`; money/rate inputs: `parseMinorUnits` / `parseBasisPoints`.
- React Query hooks for server data, Zustand for client state only (subscribe to slices), event bus for imperative actions — never as a source of truth.
- Keep forms in react-hook-form + Zod.
