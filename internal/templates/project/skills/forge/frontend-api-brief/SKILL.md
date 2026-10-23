---
name: frontend-api-brief
description: Frontend API brief — the forge-specific facts to know at step 0 before writing pages against the generated Connect RPC clients, so you don't re-derive them from generated source. Enum member names, what List requests actually filter, FK ids vs display names, no aggregate RPCs, App Router Suspense, and component export shapes.
---

# Frontend API Brief

Read this BEFORE writing frontend pages against the generated Connect RPC
clients in `src/gen/`. These are the forge-specific facts that cost a prior
agent ~8.5 minutes and wrong code across three files when re-derived from
generated source. Each item has the fix and, where the fix needs a proto or
schema change, what to hand the backend phase — a frontend phase can read
protos but cannot edit them.

## 1. protobuf-es v2 strips the enum TYPE PREFIX

A proto `enum Priority { PRIORITY_UNSPECIFIED = 0; PRIORITY_HIGH = 1; }`
compiles to a TS member `Priority.HIGH` — the generator drops the
`PRIORITY_` type prefix from the member name. But the JSDoc
(`@generated from enum value: PRIORITY_HIGH`) and the value stored in the DB /
used in Go stay the FULL `PRIORITY_HIGH`.

- Reference the member as the bare `Priority.HIGH`. Never
  `Priority.PRIORITY_HIGH` — that member does not exist and is a TS error.
- When you compare against or display a raw string that came from the wire /
  DB / a URL param, that string is the prefixed `PRIORITY_HIGH`, not the bare
  member name.

## 2. `List<Entity>Request` filters ONLY what is declared on it

The generated request exposes only the filter fields declared on that message —
typically a text `search` plus any bool fields. It does NOT auto-expose:

- enum-category facets (filter a list by a status/type/category enum), or
- per-owner / "mine" scoping keyed on an `<owner>_id`.

A frontend phase cannot add proto fields. When the filter you need is absent:

- Record the gap verbatim for the backend phase:
  "List<Entity>Request needs a `<field>` filter".
- Do NOT silently fetch one page and filter in the browser — that truncates
  everything past the page cap, a correctness bug. Client-side filtering is
  acceptable ONLY when you can fetch the whole set in a single page.
- NEVER present an unscoped list as though it were owner-scoped.

## 3. List rows carry FK ids, not display names

A list row gives you the foreign-key `<x>_id`, not the referenced entity's
name — and `search` matches only the entity's OWN columns, so you cannot
search by a related entity's name. Either:

- resolve id→name client-side (a lookup fetch per referenced entity) — works
  but does not scale, or
- note that the backend phase needs a denormalized name column (or a read
  model / join) on the entity.

## 4. There are NO aggregate / summary RPCs

Generated RPCs are CRUD plus whatever custom RPCs were authored — there is no
count / sum / stats endpoint. For KPIs:

- compute client-side from a full fetch. Watch mixed units — do NOT sum a
  money field ACROSS different currencies into one number, or
- note the backend phase needs a stats endpoint.
- Scaffolded KPI tiles show the current page's `.length`, not the real total.
  Prefer `totalCount` from the list response for any "how many" tile.

## 5. App Router: `useSearchParams()` needs a Suspense boundary

A bare `useSearchParams()` builds fine in dev but FAILS the production build.
Wrap the component that calls it in a `<Suspense>` boundary, or read
`window.location` inside an effect instead. This is a build-breaker, not a
runtime warning.

## 6. Component export shapes and response field names vary — grep, don't guess

`ui/` components are not uniform: some are default exports, some named. And
generated response messages do not all name their list field the same way.
Before importing a component or reading a response field, grep the export /
type in the component file or in `src/gen/` — guessing costs a wrong-import
build round-trip.

## 7. Scaffolded pages are OWNED and DISPOSABLE

Everything that is not a `_gen` file (and not under `src/gen/`) is yours to
keep, rewrite, or delete. Delete the scaffolded routes that are not part of
the product surface. Before you OVERWRITE an existing file, open it with the
Read TOOL first — `cat` / `grep` do not satisfy the write gate. For a
WHOLESALE replacement, prefer delete-then-write to a fresh path, which skips
the Read-before-Write gate entirely; reserve the Read-first path for in-place
or partial overwrites.
