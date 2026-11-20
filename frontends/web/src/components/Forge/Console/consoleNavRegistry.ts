// Copyright (c) 2026 Reliant Labs

/**
 * A DECLARATIVE SEAM in an app's nav (U6b): a page that lives outside this
 * module (Experiments, first) adds its row by registering it, without
 * editing the nav. Nothing is pre-declared: an unregistered page has no row,
 * so the nav never links to a route that does not exist.
 *
 * Register at module load, from the module that mounts the page's route —
 * beside its createRoute under createForgeConsoleRoutes — so the entry is in
 * place before the nav first renders:
 *
 *   registerConsoleNavEntry({
 *     id: "experiments",
 *     label: "Experiments",
 *     icon: <FlaskConical className="h-4 w-4" aria-hidden="true" />,
 *     segment: "experiments",          // /forge/p/<app>/experiments, /o/<org>/apps/<app>/experiments
 *     after: "product-analytics",
 *     available: (input) => true,       // optional: hide it for an app it does not apply to
 *   });
 *
 * The page's path is `consolePath(app, { kind: "extension", id, segment })`
 * (paths.ts), and `consolePageOf` reads it back as that page while the entry
 * is registered, so its row is the current one there.
 */

import type { ReactNode } from "react";

import type { GroupedNavInput } from "./groupedNav";

/** The built-in rows an entry can follow, in the nav's order. */
export const CONSOLE_NAV_ANCHORS = [
  "overview",
  "dashboards",
  "issues",
  "performance",
  "sessions",
  "product-analytics",
  "releases",
  "data",
  "users",
  "more-tools",
] as const;
export type ConsoleNavAnchor = (typeof CONSOLE_NAV_ANCHORS)[number];

export interface ConsoleNavEntry {
  /** Unique; the page's id in its ConsolePage (`{ kind: "extension", id }`). */
  id: string;
  label: string;
  icon?: ReactNode;
  /** The path segment under the app's console: `/forge/p/<app>/<segment>`. One segment, no slash. */
  segment: string;
  /**
   * The row it follows. An anchor the app's nav does not draw (Product
   * analytics, for an app without the hub) falls back to the nearest one
   * before it that it does.
   */
  after: ConsoleNavAnchor;
  /** Whether the app has the page; omitted, every app does. */
  available?: (input: GroupedNavInput) => boolean;
}

const entries = new Map<string, ConsoleNavEntry>();

/** Add (or replace, by id) an entry; returns its removal. */
export function registerConsoleNavEntry(entry: ConsoleNavEntry): () => void {
  if (!/^[a-z0-9][a-z0-9-]*$/.test(entry.segment)) throw new Error(`console nav entry ${entry.id}: segment must be one lowercase path segment`);
  entries.set(entry.id, entry);
  return () => {
    if (entries.get(entry.id) === entry) entries.delete(entry.id);
  };
}

/** Every registered entry, in registration order. */
export function consoleNavEntries(): readonly ConsoleNavEntry[] {
  return [...entries.values()];
}

/** The entry whose page `segment` is, if one is registered. */
export function consoleNavEntryForSegment(segment: string): ConsoleNavEntry | undefined {
  return [...entries.values()].find((entry) => entry.segment === segment);
}
