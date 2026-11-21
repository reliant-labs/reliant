// Copyright (c) 2025 Reliant Labs

/**
 * What the Projects page (control-plane docs/design/unified-shell-and-projects.md
 * §8.3) does with the catalog's listed rows that the selector does not: its
 * three sections, and where "Get the code" clones from.
 *
 *   Your projects          the person's own projects;
 *   Shared with the org    teammates' projects shared with them (viewer shape);
 *   Apps without code here org apps none of those projects contain.
 *
 * Each section keeps listCatalog's order (pinned first, then recent
 * activity), so a filter, a search or a pin moves a row within its section,
 * never across one. Pure: no React, no RPCs.
 */

import { rowApps, type AppChipModel, type ListedRow } from "./catalog";

export type ProjectsSectionId = "yours" | "shared" | "apps";

export interface ProjectsSection {
  id: ProjectsSectionId;
  label: string;
  rows: ListedRow[];
}

const SECTION_LABELS: Record<ProjectsSectionId, string> = {
  yours: "Your projects",
  shared: "Shared with the org",
  apps: "Apps without code here",
};

function sectionOf({ row }: ListedRow): ProjectsSectionId {
  if (row.kind === "app") return "apps";
  return row.shared ? "shared" : "yours";
}

/** The page's sections of `listed` (already sorted), in page order; empty ones left out. */
export function projectsPageSections(listed: readonly ListedRow[]): ProjectsSection[] {
  const by: Record<ProjectsSectionId, ListedRow[]> = { yours: [], shared: [], apps: [] };
  for (const entry of listed) by[sectionOf(entry)].push(entry);
  return (["yours", "shared", "apps"] as const)
    .filter((id) => by[id].length > 0)
    .map((id) => ({ id, label: SECTION_LABELS[id], rows: by[id] }));
}

/**
 * The repository an app was last cut from: its newest recorded source with a
 * remote (canonical `host/owner/repo`), or null when it has none (a local
 * checkout, CI that sent no source, an older forge).
 */
export function appSourceRemote(app: AppChipModel): string | null {
  return app.hosted?.sources.find((source) => source.remote !== "")?.remote ?? null;
}

/** The clone URL of a canonical `host/owner/repo` remote. */
export function cloneUrlOf(remote: string): string {
  return `https://${remote}`;
}

/** How many apps the rows hold, an app two projects hold counted once. */
export function appCount(listed: readonly ListedRow[]): number {
  return new Set(listed.flatMap(({ row }) => rowApps(row).map((app) => app.name))).size;
}
