// yours: scaffolded once, never touched again — forge will not overwrite this file
// Entity URLs for the generated CRUD pages: /<slug>, /<slug>/new, /<slug>/view?id=, /<slug>/edit?id=.
// The id rides in the query string because a static export cannot build a `[id]` segment — never add
// one. No trailing slash; a host that needs one sets `trailingSlash: true`. Depth: `frontend/serving`.

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
