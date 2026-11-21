// Copyright (c) 2025 Reliant Labs

/**
 * WHAT THE APP CONSOLE'S CODE CARD SAYS (control-plane
 * docs/design/unified-shell-and-projects.md §8.4, cases 4a–6, 10–12): where an
 * app is deployed from, and which of the person's projects hold its code.
 *
 * Everything comes from the project catalog (ListProjectsWithApps), which
 * already linked the two records under reliant's and control-plane's
 * authorization; nothing here links anything. The app's hosted record is on
 * any project chip of its name, or on its app-only row ("apps without code
 * here"). The projects that contain it are the project rows with a chip of
 * its name:
 *
 *   yours    the person's own projects, SOURCE (deployed from this checkout)
 *            before NAME (same name: a fork, a local folder, an older forge),
 *            then by activity (the catalog's order);
 *   shared   teammates' projects shared with the org, in the same order.
 *
 * Pure: the card (AppCodeCard.tsx) renders what this returns.
 */

import { rowApps, type AppChipModel, type AppLink, type HostedApp, type HostedAppSource, type ProjectCatalog, type ProjectCatalogRow } from "@/services/projects/catalog";

/** A project that contains the app, and how strongly it is linked to it. */
export interface AppCodeProject {
  row: ProjectCatalogRow;
  /** `null` when the org's apps could not be read, so the link was not checked. */
  link: Exclude<AppLink, "not_hosted"> | null;
  /** The forge.yaml's directory in the project's repo ("" is the root). */
  subpath: string;
}

export interface AppCode {
  /** Whether the org's apps could be read (the catalog's hostedAppsState). */
  state: ProjectCatalog["hostedAppsState"];
  /** The org's record of the app; undefined when it could not be read or is not listed. */
  hosted?: HostedApp;
  /** Where it was cut and deployed from, newest first (empty: none recorded). */
  sources: HostedAppSource[];
  /** The two-repositories warning (D5), "" otherwise. */
  warning: string;
  yours: AppCodeProject[];
  shared: AppCodeProject[];
}

const STRENGTH: Record<"source" | "name" | "unknown", number> = { source: 0, name: 1, unknown: 2 };
const strength = (link: AppCodeProject["link"]) => STRENGTH[link ?? "unknown"];

/** The strongest of a project's forge.yamls that name the app (two subpaths may). */
function chipFor(row: ProjectCatalogRow, appName: string): { chip: AppChipModel; link: AppCodeProject["link"] } | undefined {
  let best: { chip: AppChipModel; link: AppCodeProject["link"] } | undefined;
  for (const chip of row.apps) {
    if (chip.name !== appName) continue;
    const link = linkOf(chip);
    if (link === undefined) continue;
    if (!best || strength(link) < strength(best.link)) best = { chip, link };
  }
  return best;
}

/**
 * A chip's link to the app, for this card: SOURCE or NAME as the server said;
 * `null` when the org's apps were not read (the server could not check, and
 * sent NOT_HOSTED); `undefined` when the org was read and has no such app
 * linked here, which cannot be this console's app.
 */
function linkOf(app: AppChipModel): AppCodeProject["link"] | undefined {
  if (app.link === "source" || app.link === "name") return app.link;
  if (app.state === "no_access" || app.state === "unavailable") return null;
  return undefined;
}

export function appCode(catalog: ProjectCatalog, appName: string): AppCode {
  // Any chip, or the app-only row, that carries the org's record of the app.
  const hosted = catalog.rows.flatMap(rowApps).find((app) => app.name === appName && app.hosted)?.hosted;

  const yours: AppCodeProject[] = [];
  const shared: AppCodeProject[] = [];
  for (const row of catalog.rows) {
    if (row.kind !== "project") continue;
    const found = chipFor(row, appName);
    if (!found) continue;
    const project: AppCodeProject = { row, link: found.link, subpath: found.chip.subpath ?? "" };
    (row.shared ? shared : yours).push(project);
  }
  // Stable: equal strengths keep the catalog's (activity) order.
  const byStrength = (a: AppCodeProject, b: AppCodeProject) => strength(a.link) - strength(b.link);
  yours.sort(byStrength);
  shared.sort(byStrength);

  return {
    state: catalog.hostedAppsState,
    hosted,
    sources: hosted?.sources ?? [],
    warning: hosted?.warning ?? "",
    yours,
    shared,
  };
}

/** "github.com/acme/shop" at "services/api", or the root; "" is a local checkout. */
export function sourceLabel(source: Pick<HostedAppSource, "remote" | "subpath">): string {
  if (!source.remote) return source.subpath ? `a local checkout, ${source.subpath}/` : "a local checkout";
  return source.subpath ? `${source.remote}@${source.subpath}` : source.remote;
}

/** A commit as the card shows it: its first 7 characters. */
export function shortCommit(commit: string): string {
  return commit.slice(0, 7);
}

/** How a project is linked, in the card's words. */
export function projectLinkLabel(link: AppCodeProject["link"]): string {
  if (link === "source") return "deployed from here";
  if (link === "name") return "same name";
  return "names this app";
}

/** The clone URL "Get the code" fills in: the newest recorded remote, over https. */
export function cloneUrlOf(sources: readonly HostedAppSource[]): string | undefined {
  const remote = sources.find((source) => source.remote)?.remote;
  return remote ? `https://${remote}` : undefined;
}
