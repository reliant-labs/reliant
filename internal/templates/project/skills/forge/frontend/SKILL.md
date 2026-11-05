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
| A board, calendar, timeline, tile grid | `kanban_board`, `calendar`, `timeline`, `card_grid` |
| A funnel, markdown composer, upload | `funnel_chart`, `markdown_editor`, `upload_dropzone` |
| A menu, context menu, picker, breadcrumb, toggle, anchored panel, tooltip | `dropdown_menu`, `context_menu`, `combobox`, `breadcrumb`, `toggle_switch`, `popover`, `tooltip` |

A search that returns nothing is a real answer: there is no drawer/sheet or quantity-stepper, so those you write yourself. (The `component_library` MCP tool is the same catalog.)

**Data viz:** commodity charts (bar, line, pie, sparkline…) → `npm i recharts`. The library ships only **narrative** charts (`funnel_chart`, `quadrant_chart`, `time_series_chart` (annotated), `cohort_grid`) and `slide_*` charts: check it first.

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

## Where the rest lives

| You need… | Load |
|---|---|
| the base UI primitives (`button`, `form`/`FormField`, `table`, `select`, the toast stack), the `Badge`/`Modal` aliases, `status-badge`, `enum-select`, `entity-picker`, `session_nav` | `frontend/components` |
| the generated transport, `mock_api` (`""`, `"true"`, `"hybrid"`), fixtures, scenarios | `frontend/mock-mode` |
| per-env values, `config.js`, `runtime_config`, `forge.WorkloadURL`, `CORS_ORIGINS` | `frontend/runtime-config` |
| the scaffolded auth, event bus, UI store, `admin-url`; `forge env up` and the dev `PORT` | `frontend/scaffolded` |
| the runtime package: transport, app shell, `<Resource>`, telemetry | `frontend-runtime` |

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
