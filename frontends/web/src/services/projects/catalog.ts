// Copyright (c) 2025 Reliant Labs

/**
 * THE PROJECT CATALOG: the rows every project surface lists — the project
 * selector, the Projects page, the project page's app chips — built from one
 * `ListProjectsWithApps` answer (control-plane
 * docs/design/unified-shell-and-projects.md §8.3).
 *
 * One product noun, Projects, over two records the server has already linked
 * (the link rule lives once, in reliant's server; nothing here re-derives it):
 *
 *   project row   a reliant project the person can read, with one app chip
 *                 per forge.yaml in its repos (`ProjectApp`), however strongly
 *                 it matched the org's hosted app (SOURCE / NAME / NOT_HOSTED);
 *   app-only row  an org app no listed project contains ("apps without code
 *                 here"), keyed `app:<name>` so it pins and hides beside the
 *                 projects in the `sidebar.projects` setting.
 *
 * Then, in order: hidden rows leave (unless show-hidden), the filter (All /
 * With apps / Sites / Code only), the search, and the sort — pinned first,
 * then most recent activity (a project's own last_active or its newest app
 * deploy; an app-only row's last deploy, else its creation), then name. The
 * selector groups the result as Pinned / Recent / All (`catalogGroups`).
 *
 * Pure: no React, no RPCs. The wire answer is converted once
 * (`catalogFromResponse`); everything after works on the plain types below.
 */

import {
  HostedAppsState as WireHostedAppsState,
  ProjectAppLink,
  ProjectInstallState,
} from "@/gen/reliant/v1/project_pb";
import { projectFromProto, type Project } from "@/hooks/project-queries";
import type { SidebarProjectPrefs } from "@/lib/sidebarProjectPrefs";

// ── The wire shapes (ListProjectsWithApps, proto/reliant/v1/project.proto) ──
//
// Structural: the generated messages satisfy these (useProjectCatalog passes one),
// and a test fixture can be a plain object. Enums are the generated ones.

export interface WireHostedAppSource {
  remote: string;
  subpath: string;
  commit: string;
  lastSeenAt: string;
}

export interface WireHostedApp {
  name: string;
  createdAt: string;
  lastDeployedAt?: string;
  environments: string[];
  hasStaticSite: boolean;
  sources: WireHostedAppSource[];
  warning: string;
}

export interface WireProjectApp {
  name: string;
  repoId: string;
  subpath: string;
  link: ProjectAppLink;
  hosted?: WireHostedApp;
}

export interface WireProjectRepoSummary {
  id: string;
  name: string;
  relativePath: string;
  remote: string;
}

export interface WireProjectCheckout {
  daemonId: string;
  daemonName: string;
  installState: ProjectInstallState;
}

export interface WireProjectWithApps {
  project?: Parameters<typeof projectFromProto>[0] & { viewerAccess: boolean };
  repos: WireProjectRepoSummary[];
  apps: WireProjectApp[];
  checkouts: WireProjectCheckout[];
}

export interface WireListProjectsWithAppsResponse {
  orgId: string;
  projects: WireProjectWithApps[];
  unlinkedApps: WireHostedApp[];
  hostedAppsState: WireHostedAppsState;
}

// ── The catalog's own types ─────────────────────────────────────────────────

/** How strongly a project's forge.yaml matched an org app (the server's `ProjectAppLink`). */
export type AppLink = "source" | "name" | "not_hosted";

/** Whether the org's apps could be read: not `ok`, no chip has a hosted app behind it. */
export type HostedAppsState = "ok" | "no_access" | "unavailable";

export interface HostedAppSource {
  /** Canonical `host/owner/repo`; "" is a local checkout. */
  remote: string;
  subpath: string;
  commit: string;
  lastSeenAt: string;
}

/** One org app (a control-plane hosted project), as the server read it. */
export interface HostedApp {
  name: string;
  createdAt: string;
  /** Unset: never deployed. */
  lastDeployedAt?: string;
  environments: string[];
  hasStaticSite: boolean;
  /** Newest first. */
  sources: HostedAppSource[];
  /** The two-repositories warning (D5); "" otherwise. */
  warning: string;
}

/**
 * What a chip says about an app right now:
 *
 *   live          the org has it and it has been deployed (`lastDeployedAt`);
 *   not_deployed  the org has it but nothing was ever promoted, or the
 *                 forge.yaml names an app the org does not have yet;
 *   no_access     the person may not read the org's apps;
 *   unavailable   the control plane could not be reached.
 */
export type AppChipState = "live" | "not_deployed" | "no_access" | "unavailable";

/** One app chip: a forge.yaml of a project, or an app-only row's app. */
export interface AppChipModel {
  name: string;
  /** The project's link to it; an app-only row's app is `source`-less by definition: `null`. */
  link: AppLink | null;
  state: AppChipState;
  /** The org's app; undefined when not hosted or unreadable. */
  hosted?: HostedApp;
  /** The forge.yaml's repo and directory ("" is the root); absent on an app-only row. */
  repoId?: string;
  subpath?: string;
  /** A released artifact is a static site (the Sites filter). */
  isStaticSite: boolean;
  /** Its environments' names, persistent first. */
  environments: string[];
}

export interface CatalogRepo {
  id: string;
  name: string;
  relativePath: string;
  /** Canonical `host/owner/repo`; "" without a shareable remote, or for a viewer. */
  remote: string;
}

export interface CatalogCheckout {
  daemonId: string;
  daemonName: string;
  installState: ProjectInstallState;
}

interface RowBase {
  /** The row's id in the `sidebar.projects` setting: a project id, or `app:<name>`. */
  key: string;
  /** The row's name, for display and the name tie-break. */
  name: string;
  /** Newest activity, ms since the epoch (0: none known). */
  activityAt: number;
}

export interface ProjectCatalogRow extends RowBase {
  kind: "project";
  project: Project;
  /** Shared with the person, not theirs: no checkouts, no remotes. */
  shared: boolean;
  /**
   * The repository the project's remote names, lowercase `host/path`
   * (Project.remote_key; the SSH and HTTPS forms share it). Absent with no
   * remote, a host-less one, or from an older server. Two of the person's own
   * projects with one key are one repository (services/projects/projectMerge.ts).
   */
  remoteKey?: string;
  repos: CatalogRepo[];
  apps: AppChipModel[];
  checkouts: CatalogCheckout[];
  /** A machine has the code (an INSTALLED checkout). */
  hasCheckout: boolean;
  /** Built from a plain project list (catalogFromProjects): repos, apps and checkouts are unknown, not none. */
  partial?: boolean;
}

export interface AppOnlyCatalogRow extends RowBase {
  kind: "app";
  app: AppChipModel;
}

export type CatalogRow = ProjectCatalogRow | AppOnlyCatalogRow;

/** The catalog of one org: every row, before any choice of the person's is applied. */
export interface ProjectCatalog {
  orgId: string;
  rows: CatalogRow[];
  hostedAppsState: HostedAppsState;
}

/** The pin/hide key of an app-only row (additive in `sidebar.projects`). */
export function appRowKey(name: string): string {
  return `app:${name}`;
}

// ── From the wire ───────────────────────────────────────────────────────────

function time(value: string | undefined): number {
  if (!value) return 0;
  const ms = Date.parse(value);
  return Number.isNaN(ms) ? 0 : ms;
}

function hostedAppsStateOf(wire: WireHostedAppsState): HostedAppsState {
  if (wire === WireHostedAppsState.OK) return "ok";
  if (wire === WireHostedAppsState.NO_ACCESS) return "no_access";
  // UNSPECIFIED (an older server) reads as unreachable: nothing to trust.
  return "unavailable";
}

function linkOf(wire: ProjectAppLink): AppLink {
  if (wire === ProjectAppLink.SOURCE) return "source";
  if (wire === ProjectAppLink.NAME) return "name";
  return "not_hosted";
}

function hostedAppOf(wire: WireHostedApp): HostedApp {
  return {
    name: wire.name,
    createdAt: wire.createdAt,
    lastDeployedAt: wire.lastDeployedAt || undefined,
    environments: [...wire.environments],
    hasStaticSite: wire.hasStaticSite,
    sources: wire.sources.map((s) => ({ remote: s.remote, subpath: s.subpath, commit: s.commit, lastSeenAt: s.lastSeenAt })),
    warning: wire.warning,
  };
}

/** A chip's state from what the server could read and what it found. */
export function chipStateOf(hostedAppsState: HostedAppsState, hosted: HostedApp | undefined): AppChipState {
  if (hostedAppsState === "no_access") return "no_access";
  if (hostedAppsState === "unavailable") return "unavailable";
  return hosted?.lastDeployedAt ? "live" : "not_deployed";
}

function chipOf(name: string, link: AppLink | null, hosted: HostedApp | undefined, state: HostedAppsState, where?: { repoId: string; subpath: string }): AppChipModel {
  return {
    name,
    link,
    state: chipStateOf(state, hosted),
    hosted,
    ...(where ? { repoId: where.repoId, subpath: where.subpath } : {}),
    isStaticSite: hosted?.hasStaticSite === true,
    environments: hosted?.environments ?? [],
  };
}

function projectRowOf(wire: WireProjectWithApps, state: HostedAppsState): ProjectCatalogRow | null {
  if (!wire.project) return null;
  const project = projectFromProto(wire.project);
  const apps = wire.apps.map((a) => {
    // Not OK: the server sends no `hosted`, and a NAME/SOURCE link it could not check is not one.
    const hosted = state === "ok" && a.hosted ? hostedAppOf(a.hosted) : undefined;
    return chipOf(a.name, linkOf(a.link), hosted, state, { repoId: a.repoId, subpath: a.subpath });
  });
  const checkouts = wire.checkouts.map((c) => ({ daemonId: c.daemonId, daemonName: c.daemonName, installState: c.installState }));
  const deployed = apps.map((a) => time(a.hosted?.lastDeployedAt));
  return {
    kind: "project",
    key: project.id,
    name: project.name,
    activityAt: Math.max(time(project.last_active), ...deployed),
    project,
    shared: wire.project.viewerAccess,
    ...(wire.project.remoteKey ? { remoteKey: wire.project.remoteKey } : {}),
    repos: wire.repos.map((r) => ({ id: r.id, name: r.name, relativePath: r.relativePath, remote: r.remote })),
    apps,
    checkouts,
    hasCheckout: checkouts.some((c) => c.installState === ProjectInstallState.INSTALLED),
  };
}

function appOnlyRowOf(wire: WireHostedApp, state: HostedAppsState): AppOnlyCatalogRow {
  const hosted = hostedAppOf(wire);
  return {
    kind: "app",
    key: appRowKey(hosted.name),
    name: hosted.name,
    activityAt: time(hosted.lastDeployedAt) || time(hosted.createdAt),
    app: chipOf(hosted.name, null, hosted, state),
  };
}

/** The catalog of one `ListProjectsWithApps` answer: the server's order, converted once. */
export function catalogFromResponse(response: WireListProjectsWithAppsResponse): ProjectCatalog {
  const state = hostedAppsStateOf(response.hostedAppsState);
  const rows: CatalogRow[] = [];
  for (const p of response.projects) {
    const row = projectRowOf(p, state);
    if (row) rows.push(row);
  }
  // The server sends none unless it read the org's apps; trust that, not a stale field.
  if (state === "ok") for (const a of response.unlinkedApps) rows.push(appOnlyRowOf(a, state));
  return { orgId: response.orgId, rows, hostedAppsState: state };
}

/** Whether a project of the person's own (not one shared with them) has a git remote: a repository they brought. */
export function catalogHasOwnRepo(catalog: ProjectCatalog): boolean {
  return catalog.rows.some(
    (row) => row.kind === "project" && !row.shared && (!!row.project.remote_url || row.repos.some((r) => r.remote !== "")),
  );
}

/** Whether any app in the catalog has been deployed (`lastDeployedAt`). */
export function catalogHasDeployedApp(catalog: ProjectCatalog): boolean {
  return catalog.rows.some((row) =>
    row.kind === "app" ? !!row.app.hosted?.lastDeployedAt : row.apps.some((app) => !!app.hosted?.lastDeployedAt),
  );
}

/**
 * A catalog of plain projects (ListProjects' rows), for when the server
 * cannot answer ListProjectsWithApps (an older server): every project, no
 * app chips, no checkouts known, and the org's apps unread. A picker keeps
 * working; it only knows less.
 */
export function catalogFromProjects(orgId: string, projects: readonly Project[]): ProjectCatalog {
  return {
    orgId,
    hostedAppsState: "unavailable",
    rows: projects.map((project) => ({
      kind: "project",
      key: project.id,
      name: project.name,
      activityAt: time(project.last_active),
      project,
      shared: false,
      repos: [],
      apps: [],
      checkouts: [],
      hasCheckout: false,
      partial: true,
    })),
  };
}

// ── Choosing rows ───────────────────────────────────────────────────────────

/**
 * The Projects filters. They partition the rows by what they hold:
 *
 *   all       every row;
 *   with_apps a row with an app: a project whose code holds a forge.yaml, and
 *             every app-only row;
 *   sites     a row with an app that ships a static site;
 *   code_only a project whose code holds no forge.yaml (with_apps' complement).
 */
export type CatalogFilter = "all" | "with_apps" | "sites" | "code_only";

export const CATALOG_FILTERS: readonly { id: CatalogFilter; label: string }[] = [
  { id: "all", label: "All" },
  { id: "with_apps", label: "With apps" },
  { id: "sites", label: "Sites" },
  { id: "code_only", label: "Code only" },
];

export function rowApps(row: CatalogRow): AppChipModel[] {
  return row.kind === "project" ? row.apps : [row.app];
}

function isRootPath(relativePath: string): boolean {
  const path = relativePath.trim();
  return path === "" || path === "." || path === "./";
}

/**
 * The project's own repository: the one at its root, else its only one. A
 * multi-repo project whose root holds no repository has none of its own.
 */
export function ownRepoOf(row: ProjectCatalogRow): CatalogRepo | undefined {
  return row.repos.find((repo) => isRootPath(repo.relativePath)) ?? (row.repos.length === 1 ? row.repos[0] : undefined);
}

/**
 * The project's OWN apps, in the server's order: every forge.yaml of its own
 * repository (any directory of it), the app its root forge.yaml declares
 * (`forge_project_name`), and a chip with no repository (the server's ref for
 * that declared name before the repos were read).
 *
 * A nested repository's app is still one of the row's chips (the Projects
 * page lists it there), but it is that repository's, not the project's: a
 * parent checkout holding `reliant/` and `control-plane/` is not the
 * control-plane app, and must not open its sections or claim its console. A
 * partial row (repos unknown) has only the declared name to go on.
 */
export function ownAppsOf(row: ProjectCatalogRow): AppChipModel[] {
  const declared = row.project.forge_project_name?.trim() || null;
  const own = ownRepoOf(row);
  return row.apps.filter((app) => !app.repoId || app.name === declared || (own !== undefined && app.repoId === own.id));
}

export function matchesFilter(row: CatalogRow, filter: CatalogFilter): boolean {
  switch (filter) {
    case "all":
      return true;
    case "with_apps":
      return rowApps(row).length > 0;
    case "sites":
      return rowApps(row).some((a) => a.isStaticSite);
    case "code_only":
      return row.kind === "project" && row.apps.length === 0;
  }
}

/** Everything a search term can match on a row, lower-cased. */
function haystack(row: CatalogRow): string {
  const parts: string[] = [row.name];
  if (row.kind === "project") {
    const p = row.project;
    parts.push(p.path, p.remote_url ?? "", p.description ?? "");
    for (const r of row.repos) parts.push(r.name, r.relativePath, r.remote);
    for (const a of row.apps) parts.push(a.name);
  } else {
    for (const s of row.app.hosted?.sources ?? []) parts.push(s.remote);
  }
  return parts.join("\n").toLowerCase();
}

/**
 * Whether `row` matches `query`: every whitespace-separated term appears,
 * case-insensitively, in its name, a repo's name, path or remote, the
 * project's folder or remote, or an app's name. An empty query matches all.
 */
export function matchesQuery(row: CatalogRow, query: string): boolean {
  const terms = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (terms.length === 0) return true;
  const hay = haystack(row);
  return terms.every((t) => hay.includes(t));
}

/** Most recent activity first, then name (case-insensitive), then key: a total order. */
export function compareByActivity(a: CatalogRow, b: CatalogRow): number {
  if (a.activityAt !== b.activityAt) return b.activityAt - a.activityAt;
  const byName = a.name.localeCompare(b.name, undefined, { sensitivity: "base" });
  if (byName !== 0) return byName;
  return a.key < b.key ? -1 : a.key > b.key ? 1 : 0;
}

/** The person's choices the catalog applies: pins and hides (`sidebar.projects`). */
export type CatalogPrefs = Pick<SidebarProjectPrefs, "pinned" | "hidden">;

export interface CatalogView {
  filter?: CatalogFilter;
  query?: string;
  /** List hidden rows too (the setting's show-all, or a page's own toggle). */
  showHidden?: boolean;
  /** Which rows the caller can use: a chat's project picker takes projects only. */
  kinds?: "any" | "projects" | "apps";
}

/** A row as listed: the row and the person's choice about it. */
export interface ListedRow {
  row: CatalogRow;
  pinned: boolean;
  hidden: boolean;
}

/**
 * The rows `view` lists, pinned first and then by activity. `kinds: "apps"`
 * keeps the rows that hold an app (a project with chips, an app-only row).
 */
export function listCatalog(catalog: ProjectCatalog, prefs: CatalogPrefs, view: CatalogView = {}): ListedRow[] {
  const { filter = "all", query = "", showHidden = false, kinds = "any" } = view;
  const out: ListedRow[] = [];
  for (const row of catalog.rows) {
    if (kinds === "projects" && row.kind !== "project") continue;
    if (kinds === "apps" && rowApps(row).length === 0) continue;
    const hidden = prefs.hidden.has(row.key);
    if (hidden && !showHidden) continue;
    if (!matchesFilter(row, filter) || !matchesQuery(row, query)) continue;
    out.push({ row, pinned: !hidden && prefs.pinned.has(row.key), hidden });
  }
  return out.sort((a, b) => (a.pinned !== b.pinned ? (a.pinned ? -1 : 1) : compareByActivity(a.row, b.row)));
}

/** How many rows to count in each filter, for the filter bar's counts. */
export function filterCounts(catalog: ProjectCatalog, prefs: CatalogPrefs, view: Omit<CatalogView, "filter"> = {}): Record<CatalogFilter, number> {
  const counts: Record<CatalogFilter, number> = { all: 0, with_apps: 0, sites: 0, code_only: 0 };
  for (const { row } of listCatalog(catalog, prefs, { ...view, filter: "all" })) {
    for (const f of CATALOG_FILTERS) if (matchesFilter(row, f.id)) counts[f.id] += 1;
  }
  return counts;
}

// ── The selector's groups ───────────────────────────────────────────────────

export type CatalogGroupId = "pinned" | "recent" | "all" | "results";

export interface CatalogGroup {
  id: CatalogGroupId;
  label: string;
  rows: ListedRow[];
}

/** How many unpinned rows the selector shows under Recent. */
export const RECENT_COUNT = 5;

/**
 * The selector's groups of `listed` (already sorted by listCatalog): Pinned,
 * then Recent (the `recentCount` most active unpinned rows), then All (the
 * rest), each row once and empty groups left out. While searching it is one
 * group, Results, in the same order: a match is not "recent".
 */
export function catalogGroups(listed: readonly ListedRow[], options: { searching?: boolean; recentCount?: number } = {}): CatalogGroup[] {
  const { searching = false, recentCount = RECENT_COUNT } = options;
  if (searching) return listed.length > 0 ? [{ id: "results", label: "Results", rows: [...listed] }] : [];
  const pinned = listed.filter((l) => l.pinned);
  const rest = listed.filter((l) => !l.pinned);
  const groups: CatalogGroup[] = [
    { id: "pinned", label: "Pinned", rows: pinned },
    { id: "recent", label: "Recent", rows: rest.slice(0, recentCount) },
    { id: "all", label: "All", rows: rest.slice(recentCount) },
  ];
  return groups.filter((g) => g.rows.length > 0);
}

/** One app the person can pick, with the project whose code holds it (none for an app-only row). */
export interface AppEntry {
  app: AppChipModel;
  project?: ProjectCatalogRow;
  pinned: boolean;
}

/**
 * Every app in `listed`, one entry per name, in row order: an app two
 * projects hold is listed once, under the first (the more recently active).
 */
export function appEntries(listed: readonly ListedRow[]): AppEntry[] {
  const seen = new Set<string>();
  const out: AppEntry[] = [];
  for (const { row, pinned } of listed) {
    for (const app of rowApps(row)) {
      if (seen.has(app.name)) continue;
      seen.add(app.name);
      out.push({ app, project: row.kind === "project" ? row : undefined, pinned });
    }
  }
  return out;
}

// ── Words ───────────────────────────────────────────────────────────────────

/** A chip's state, in words: its tooltip's first line and its screen-reader label. */
export function chipStateLabel(state: AppChipState): string {
  switch (state) {
    case "live":
      return "Deployed";
    case "not_deployed":
      return "Not deployed";
    case "no_access":
      return "No access to this org's apps";
    case "unavailable":
      return "App status unavailable";
  }
}

/** How a project's code is linked to the app, in words (the chip's tooltip). */
export function linkLabel(link: AppLink | null): string | null {
  switch (link) {
    case "source":
      return "Deployed from this repository";
    case "name":
      return "Same name in this org; not deployed from this repository";
    case "not_hosted":
      return "This org has no app of this name yet";
    case null:
      return null;
  }
}
