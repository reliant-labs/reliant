// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// Entity URLs — the one place the generated CRUD pages build and read them,
// so no page hand-rolls a query string.
//
// Every generated route is a STATIC route:
//
//   /<slug>                 list
//   /<slug>/new             create
//   /<slug>/view?id=<id>    detail
//   /<slug>/edit?id=<id>    edit
//
// The id rides in the query string rather than in a `[id]` path segment, and
// that is what lets `output: "export"` build them. A static export can only
// emit the pages it can enumerate at build time, and an entity id exists only
// at runtime — so `/<slug>/[id]` fails `next build` ("missing
// generateStaticParams()") while `/<slug>/view` exports once, as
// `out/<slug>/view.html`, and reads its id in the browser. The same pages
// work unchanged under `output: "standalone"`.
//
// No trailing slash (Next's default, `trailingSlash: false`): the export
// writes `<slug>/view.html`, and both the hosted static origin and the
// scaffold's nginx image resolve `/<slug>/view` to it by trying `.html`.
// A plain file server that only maps directories to index.html needs
// `trailingSlash: true` in next.config.ts instead; these helpers do not
// change, because Next adds the slash to every <Link> and router call.

import { useSearchParams } from "next/navigation";

/** A primary key as it appears on the wire. */
export type EntityId = string | number | bigint;

/** The detail page of one row: `/<slug>/view?id=<id>`. */
export function entityViewHref(slug: string, id: EntityId): string {
  return `/${slug}/view?id=${encodeURIComponent(String(id))}`;
}

/** The edit page of one row: `/<slug>/edit?id=<id>`. */
export function entityEditHref(slug: string, id: EntityId): string {
  return `/${slug}/edit?id=${encodeURIComponent(String(id))}`;
}

/**
 * The id a view/edit page is showing, read from `?id=`. Undefined when the
 * parameter is absent or empty — a trimmed link or a bookmark of the bare
 * route — so the page can render a designed state instead of throwing.
 *
 * It calls useSearchParams, so the component that calls it must sit under a
 * <Suspense> boundary: a static export prerenders the route with no query
 * string, and Next refuses to build a page that reads search params outside
 * one. The generated pages split into a Suspense shell and the component
 * that reads the id for exactly this reason.
 */
export function useEntityIdParam(): string | undefined {
  return useSearchParams().get("id") || undefined;
}
