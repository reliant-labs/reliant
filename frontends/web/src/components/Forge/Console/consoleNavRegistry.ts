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
 *     after: "product-analytics",       // where it sits when the nav draws no group for it
 *     group: "product-analytics",       // optional: a child of Product analytics, after Insights and Events & metrics
 *     available: (input) => true,       // optional: hide it for an app it does not apply to
 *   });
 *
 * With `group`, the row renders INSIDE that group, so the group opens by
 * itself while the page is current. Two groups take rows: Product analytics
 * (Analytics ▾ in the shell's nav; every app's nav draws it), and Security,
 * which the shell's nav declares but draws only once a row has joined it, so
 * it never shows an empty group or links to a page that is not mounted.
 * Without `group`, the row is a top-level row after `after`.
 *
 * The page's path is `consolePath(app, { kind: "extension", id, segment })`
 * (paths.ts), and `consolePageOf` reads it back as that page while the entry
 * is registered, so its row is the current one there. A page with routes and
 * a ConsolePage kind of its own (Security's) names it instead, as `target`,
 * and the `kinds` that light its row:
 *
 *   registerConsoleNavEntry({
 *     id: "vulnerabilities", label: "Vulnerabilities", segment: "security",
 *     after: "more-tools", group: "security",
 *     target: { kind: "vulnerabilities" }, kinds: ["vulnerabilities", "vulnerability"],
 *   });
 */

import type { ReactNode } from "react";

import type { GroupedNavInput } from "./groupedNav";
import type { ConsolePage } from "./paths";

/** The built-in rows an entry can follow, in the nav's order. */
export const CONSOLE_NAV_ANCHORS = [
  "overview",
  "services",
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
/** The built-in groups a registered row can join. */
export type ConsoleNavGroup = Extract<ConsoleNavAnchor, "product-analytics"> | "security";

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
  /**
   * The group the row joins as its last child, when the nav draws that group:
   * Product analytics (Insights · Events & metrics · Experiments; the
   * Experiments page registers with `group: "product-analytics"`), or
   * Security, which shows once a row joins it.
   */
  group?: ConsoleNavGroup;
  /**
   * The page the row opens, when it is a built-in console page with routes of
   * its own; omitted, the registered page at `segment`.
   */
  target?: ConsolePage;
  /** The page kinds that light the row, with `target`; omitted, `target`'s own kind. */
  kinds?: readonly ConsolePage["kind"][];
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

/** The entry whose page `segment` is, if one is registered (an entry with a `target` has its own routes). */
export function consoleNavEntryForSegment(segment: string): ConsoleNavEntry | undefined {
  return [...entries.values()].find((entry) => !entry.target && entry.segment === segment);
}

/** The page an entry's row opens: its `target`, else the registered page at its `segment`. */
export function consoleNavEntryTarget(entry: ConsoleNavEntry): ConsolePage {
  return entry.target ?? { kind: "extension", id: entry.id, segment: entry.segment };
}

/** Whether `page` is the entry's, so its row is the current one. */
export function consoleNavEntryLights(entry: ConsoleNavEntry, page: ConsolePage | null | undefined): boolean {
  if (!page) return false;
  if (!entry.target) return page.kind === "extension" && page.id === entry.id;
  return (entry.kinds ?? [entry.target.kind]).includes(page.kind);
}
