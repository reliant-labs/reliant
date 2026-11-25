# Proposal: URL view state in @reliantlabs/forge-web-runtime

**Status:** proposal, not implemented. Written for owner review. Nothing here has
landed in forge.
**Date:** 2026-11-24
**Issue:** forge#2723
**Scope:** forge `web-runtime/` (new router-neutral module), the `frontend/state`
skill, and the Next.js and Vite frontend scaffolds. First consumer: reliant
`frontends/web/src/lib/urlState/`.

## Summary

The owner decided on 2026-11-24 that in reliant's web app **the URL is the only
source of view state**. Storage holds only defaults for an empty URL: this tab's
sessionStorage first, then a localStorage seed for new tabs. The decision came
out of a prod incident (reliant#3419): the selected projects lived in shared
localStorage and in a per-org server setting, so two tabs overwrote each other's
choice, and a "restore the last chat" path won over the chat the URL named.

Reliant is building the helpers first, as the owner asked ("reliant first, then
the router-neutral parts move to forge-web-runtime"). This proposal says which
parts move, what forge's scaffold does with them, and what the `frontend/state`
skill should teach instead of what it teaches today.

## Why forge should care

- forge's `frontend/state` skill tells every agent to keep "cross-page
  selection (survives reload)" such as `currentOrgId`, `currentTeamId` and
  `currentWorkspaceId` in a **Zustand `persist` store**. That is the pattern
  that caused the reliant incident. A persisted store is shared by every tab of
  the browser, and zustand's `persist` rewrites the whole blob on every set, so
  the last tab to write wins. A reload, or a new tab, then restores another
  tab's selection over the URL.
- The scaffold's `src/lib/search-schemas.ts` gives typed _reads_
  (`useTypedSearchParams`). Writing the URL is still up to each page: whether a
  change is a push or a replace, how a default stays out of the URL, how a param
  survives a navigation that does not name it, and what an empty URL starts
  from. Each app re-invents these, and gets them subtly wrong.

## What moves (router-neutral, no app imports)

These are reliant's `lib/urlState/searchParams.ts` and `lib/urlState/tabDefault.ts`
as written. They import nothing from the app or the router.

1. **`SearchParam<T>`, a one-param grammar**: `parse(raw) → T | undefined` (an
   unreadable value is absent, never an error, because a URL is user-edited and
   shared), `serialize`, `defaultValue`, `equals`. Helpers: `readParam`,
   `hasParam`, `writeParam` (a value equal to the default is not written),
   `stringParam(key, allowed?, default?)`, `idListParam(key)`.
2. **`retainSearchParams(params)`**, a search middleware with the
   `{ search, next } → search` shape. A navigation that does not name a retained
   param keeps it, and one that names it (even as `undefined`) decides.
   TanStack Router takes this shape directly. Next.js has no search middleware,
   so for Next the same rule is a link helper: `withRetained(href, current)`.
3. **`createTabDefault({ key, parse, serialize })`**, a default for an empty URL.
   `get()` reads this tab's sessionStorage, then the localStorage seed, which it
   copies into the tab so the tab keeps it. `set()` writes both. It listens to
   no `storage` event, so a tab that has its own value never moves because of
   another tab. Keys are a function, so they can be scoped per user and org.
4. **The rule, in one function** (reliant's `fillScope`, generalised):
   If the URL names the state, use it and record it as the tab default. If it
   names nothing, take the default and write it into the URL with a replace, so
   what is on screen is always in the URL and can be shared.

What stays in reliant: the shell-scope schema (`projects`, `focus`), the
org-qualified paths, and the TanStack wiring. These are product concepts. Org
slugs stay out of forge, because orgs are a control-plane concept.

## How the scaffold uses it

- **Skill:** the `frontend/state` decision table drops "Zustand `persist` store"
  for cross-page selection and says: selection is **URL** state. The only
  storage is `createTabDefault`, read only when the URL is empty. A new row,
  "Defaults for an empty URL", points at it. `frontend/state` also gains a short
  "push or replace" rule: a person's choice is a push, while filling a default or
  mirroring how the page already looks is a replace.
- **Next.js scaffold** (`src/lib/search-schemas.ts`): beside
  `useTypedSearchParams`, add `useSearchParamState(param)`. It returns
  `[value, set(value, { replace })]` and is built on `readParam`/`writeParam`
  and `router.push`/`router.replace`. Static export is unaffected, because
  search params are client-side.
- **Vite scaffold** (when it has a router): the same hook over the router it
  ships.
- **Lint (later, warn-first per the rollout table):** a `frontend` lint rule
  that flags a `persist(...)` store whose `partialize` includes a field named
  like a selection (`current*Id`, `selected*`, `*FilterId`). This is a
  heuristic, so it starts as a warning only.

## Alternatives considered

- **Forge first (F3).** This would design the API without a consumer and block
  reliant's fix on a forge release. Rejected by the owner's sequencing.
- **Ship a TanStack-only helper.** forge's default frontend is Next.js, which
  would get nothing. The grammar and the defaults are router-neutral, and only
  the middleware's wiring is not.
- **BroadcastChannel sync between tabs.** This is the opposite of the goal. The
  owner wants tabs independent, and syncing them would reintroduce the bleed
  without the clobbering.

## Semver

All additive: new exports in the web-runtime barrel, a new scaffold hook in a
file the project owns, and a skill edit. The lint rule follows the warn-first
rollout table (`internal/cli/lint/lint_rollout_table.go`).

## Plan

1. Reliant ships its URL-state PRs and runs on them for at least one release.
2. Move `searchParams.ts` and `tabDefault.ts` into `web-runtime/src/url-state.ts`
   with their tests, export them from the barrel, and add them to
   `published-surface.test.ts`. Reliant then imports them from the package and
   deletes its copies.
3. Update the skill and the Next.js scaffold hook.
4. Add the lint rule as a warning.
