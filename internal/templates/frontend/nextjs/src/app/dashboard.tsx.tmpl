"use client";

// yours: scaffolded once, never touched again — forge will not overwrite this file
//
// STARTER CODE, NOT A DESIGN. A tile grid is the most generic dashboard there
// is; it exists to prove the data layer works end to end, not because a count
// per entity is what your users need. Replace it with the two or three numbers
// that actually matter for your product, and delete the rest.
//
// Tiles read LIVE counts through the generated list hooks, so what they show
// is real — copy the `useList*({ pageSize: 1 }).data?.totalCount` shape for
// any metric you add. Presentation comes from the semantic theme tokens in
// globals.css (bg-surface, text-ink, border-accent), so restyle there first
// and every page follows.
//
// The tiles come from DASHBOARD_TILES (src/components/dashboard-tiles_gen.ts,
// one generated module per entity), so an entity added later gets its tile
// without forge touching this file. Filter or replace the grid freely; a metric
// of your own is a component of your own beside it.

import { Inbox, Plus } from "lucide-react";
import Link from "next/link";

import { DASHBOARD_TILES } from "@/components/dashboard-tiles_gen";
import { ALL_ROUTES } from "@/components/nav";

/**
 * EntityTiles renders the per-entity stat-card grid. One card per CRUD
 * entity, linking into its list page, with a live count from the entity's
 * list hook. Empty state ("No resources yet") surfaces when the project
 * has no CRUD services yet.
 */
export function EntityTiles() {
  if (DASHBOARD_TILES.length === 0) {
    return (
      <div className="rounded-lg border border-dashed border-border-strong bg-surface p-12 text-center">
        <span className="mx-auto flex h-12 w-12 items-center justify-center rounded-full bg-surface-muted">
          <Inbox aria-hidden className="h-6 w-6 text-ink-subtle" strokeWidth={1.5} />
        </span>
        <p className="mt-4 text-sm font-medium text-ink">No resources yet</p>
        <p className="mx-auto mt-1 max-w-sm text-sm text-ink-muted">
          Add services with CRUD RPCs and run{" "}
          <code className="rounded-md bg-surface-muted px-1.5 py-0.5 font-mono text-xs text-ink">
            forge generate
          </code>{" "}
          to populate this dashboard.
        </p>
      </div>
    );
  }

  // auto-fit rather than fixed breakpoints: tiles keep a sane minimum width
  // and the grid decides the column count, so one entity does not stretch a
  // card across the viewport and seven do not cram.
  return (
    <div className="mb-8 grid gap-4 [grid-template-columns:repeat(auto-fit,minmax(15rem,1fr))]">
      {DASHBOARD_TILES.map(({ slug, Tile }) => (
        <Tile key={slug} />
      ))}
    </div>
  );
}

/**
 * QuickActions renders a "Create <Entity>" button for every CRUD entity
 * whose service has a Create RPC — and ONLY those, so each button targets
 * a create page the generator actually emitted. Returns null when no
 * entity is creatable.
 */
export function QuickActions() {
  const createRoutes = ALL_ROUTES.filter((route) => route.hasCreate);
  if (createRoutes.length === 0) {
    return null;
  }
  return (
    <div>
      <h2 className="mb-3 text-xs font-semibold uppercase tracking-wider text-ink-subtle">
        Quick actions
      </h2>
      <div className="flex flex-wrap gap-2">
        {createRoutes.map((route) => (
          <Link
            key={route.slug}
            href={`${route.path}/new`}
            className="inline-flex items-center gap-1.5 rounded-md border border-border bg-surface px-3 py-2 text-sm font-medium text-ink transition-colors hover:border-border-strong hover:bg-surface-muted focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent focus-visible:ring-offset-2"
          >
            <Plus aria-hidden className="h-4 w-4 text-ink-subtle" strokeWidth={2} />
            New {route.labelSingular}
          </Link>
        ))}
      </div>
    </div>
  );
}
