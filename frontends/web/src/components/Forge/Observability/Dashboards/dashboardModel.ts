// Copyright (c) 2025 Reliant Labs

/**
 * The pure parts of native dashboards — no React, no fetching — so every rule
 * the pages follow is testable on its own:
 *
 *   index      filtering (search, source, owner), the starred and recently
 *              viewed shelves, and what a row says about where it came from.
 *   view       the URL state of a dashboard (variables, compare, refresh,
 *              TV), what a comparison shifts the window by, and the alert
 *              bands drawn over its charts.
 *   layout     forge's 24-column widths as the grid's four spans, moving and
 *              resizing panels.
 */

import type { AlertEvent } from "@/services/controlPlane/observability/alertingTypes";
import type { Promotion } from "@/services/controlPlane/observability/deliveryTypes";
import type { DashboardKind, DashboardRef, DashboardSource, DashboardSummary } from "@/services/controlPlane/observability/dashboardTypes";
import type { Dashboard, Panel } from "@/services/controlPlane/observability/types";
import { RANGE_PRESETS, type RangePreset } from "@/services/controlPlane/observability/window";

import type { ChartBand } from "../charts/UPlotChart";
import { GROUP_LABELS, groupOf, type DashboardGroup } from "./dashboardGroups";

// ── Index ────────────────────────────────────────────────────────────────────

export const SOURCE_LABELS: Record<DashboardSource, string> = {
  generated: "generated",
  repo: "repo",
  workspace: "workspace",
  template: "template",
  builtin: "built-in",
};

export type SourceFilter = "release" | "workspace";

/** Who a dashboard belongs to, as the Owner filter names it: forge, the repo, or its author. */
export function ownerOf(summary: DashboardSummary): string {
  if (summary.source === "generated" || summary.source === "builtin") return "forge";
  if (summary.source === "repo") return "repo";
  const user = summary.workspace?.createdBy;
  return user?.name || user?.email || "unknown";
}

/** Every owner in the index, people first (alphabetically), then forge and repo. */
export function ownersOf(dashboards: readonly DashboardSummary[]): string[] {
  const people = new Set<string>();
  let forge = false;
  let repo = false;
  for (const d of dashboards) {
    if (d.source === "generated" || d.source === "builtin") forge = true;
    else if (d.source === "repo") repo = true;
    else people.add(ownerOf(d));
  }
  return [...[...people].sort((a, b) => a.localeCompare(b)), ...(forge ? ["forge"] : []), ...(repo ? ["repo"] : [])];
}

export interface IndexFilter {
  q?: string;
  source?: SourceFilter;
  owner?: string;
  /** One page group (dashboardGroups.ts). */
  group?: DashboardGroup;
}

/**
 * What a search matches: the title, description, key (so a workload name),
 * the tags and the group's name — across every group at once.
 */
function searchText(d: DashboardSummary): string {
  return [d.title, d.description, d.ref.key, ...(d.tags ?? []), GROUP_LABELS[groupOf(d)]].join("\u0000").toLowerCase();
}

export function filterIndex(dashboards: readonly DashboardSummary[], filter: IndexFilter): DashboardSummary[] {
  const q = (filter.q ?? "").trim().toLowerCase();
  return dashboards.filter((d) => {
    if (q && !searchText(d).includes(q)) return false;
    if (filter.group && groupOf(d) !== filter.group) return false;
    if (filter.source === "release" && d.ref.kind !== "release") return false;
    if (filter.source === "workspace" && d.ref.kind !== "workspace") return false;
    if (filter.owner && ownerOf(d) !== filter.owner) return false;
    return true;
  });
}

/** How many starred dashboards the nav nests under Dashboards. */
export const NAV_STARRED = 3;

/** The starred dashboards, in index order (release dashboards first, as the server lists them). */
export function starredOf(dashboards: readonly DashboardSummary[]): DashboardSummary[] {
  return dashboards.filter((d) => d.starred);
}

/** The caller's most recently opened dashboards, newest first. */
export function recentOf(dashboards: readonly DashboardSummary[], limit = 4): DashboardSummary[] {
  return dashboards
    .filter((d) => d.lastViewedAtMs !== null)
    .sort((a, b) => (b.lastViewedAtMs ?? 0) - (a.lastViewedAtMs ?? 0))
    .slice(0, limit);
}

export function sameRef(a: DashboardRef, b: DashboardRef): boolean {
  return a.kind === b.kind && a.projectId === b.projectId && a.key === b.key;
}

/** A dashboard's URL kind segment: only release and workspace dashboards are pages. */
export type DashboardPageKind = Exclude<DashboardKind, "template">;

export function isDashboardPageKind(value: string | undefined): value is DashboardPageKind {
  return value === "release" || value === "workspace" || value === "builtin";
}

/**
 * A slug for a new workspace dashboard from its title: what the server
 * accepts (`^[a-z0-9][a-z0-9-]{0,62}$`), and what the promoted file is named.
 */
export function slugify(title: string): string {
  const slug = title
    .normalize("NFKD")
    .replace(/[\u0300-\u036f]/g, "")
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 63)
    .replace(/-+$/, "");
  return slug || "dashboard";
}

export const SLUG_PATTERN = /^[a-z0-9][a-z0-9-]{0,62}$/;

// ── View state ───────────────────────────────────────────────────────────────

export const COMPARE_MODES = ["off", "period", "release"] as const;
export type CompareMode = (typeof COMPARE_MODES)[number];

export const COMPARE_LABELS: Record<CompareMode, string> = {
  off: "Off",
  period: "Previous period",
  release: "Previous release",
};

export const REFRESH_INTERVALS = ["off", "10s", "30s", "1m", "5m"] as const;
export type RefreshInterval = (typeof REFRESH_INTERVALS)[number];

const REFRESH_MS: Record<RefreshInterval, number | false> = { off: false, "10s": 10_000, "30s": 30_000, "1m": 60_000, "5m": 300_000 };

export function refreshMs(interval: RefreshInterval): number | false {
  return REFRESH_MS[interval];
}

/**
 * A dashboard's URL state — everything "Share" puts in the link: the window,
 * the service variable, the comparison and the refresh. The environment is
 * the console's own scope (`?env=`), carried by every console link already.
 */
export interface DashboardSearch {
  range?: RangePreset;
  service?: string;
  /** RPC attributes, NOT the deployed process's service.name. */
  rpcService?: string;
  rpcMethod?: string;
  rpcProcedure?: string;
  /** Other explicitly declared filter ids and their viewer values. */
  variables?: Record<string, string>;
  /** Absent: saved grouping. "none": explicitly ungrouped. Otherwise a supported semantic field. */
  breakdown?: string;
  hiddenSections?: string[];
  hiddenPanels?: string[];
  /** Absent: on, when supported by the dashboard. */
  releases?: boolean;
  alerts?: boolean;
  compare?: CompareMode;
  refresh?: RefreshInterval;
  /** Full screen, for a wall display. */
  tv?: boolean;
  /** Open a workspace dashboard's older version (read-only). */
  version?: number;
  /** Open a release dashboard as it shipped in this release. */
  release?: string;
}

export function rangeParam(value: unknown): RangePreset | undefined {
  return (RANGE_PRESETS as readonly string[]).includes(String(value)) ? (value as RangePreset) : undefined;
}

export function compareParam(value: unknown): CompareMode {
  return (COMPARE_MODES as readonly string[]).includes(String(value)) ? (value as CompareMode) : "off";
}

export function refreshParam(value: unknown): RefreshInterval {
  return (REFRESH_INTERVALS as readonly string[]).includes(String(value)) ? (value as RefreshInterval) : "off";
}

const RANGE_MS: Record<RangePreset, number> = {
  "15m": 15 * 60_000,
  "1h": 60 * 60_000,
  "6h": 6 * 60 * 60_000,
  "24h": 24 * 60 * 60_000,
  "7d": 7 * 24 * 60 * 60_000,
};

export function rangeMs(range: RangePreset): number {
  return RANGE_MS[range];
}

/**
 * What the comparison is, and how far back it reads.
 *
 *   period   the window just before this one: shifted by its own length.
 *   release  the window that ended when the environment's current release
 *            was promoted: the same length, before the change. Needs one
 *            environment in scope and a promotion into it; `reason` says why
 *            not otherwise.
 */
export type CompareTarget =
  | { mode: "off" }
  | { mode: "period"; offsetMs: number; label: string }
  | { mode: "release"; offsetMs: number; label: string; promotion: Promotion }
  | { mode: "unavailable"; reason: string };

export function compareTarget(
  mode: CompareMode,
  range: RangePreset,
  promotions: readonly Promotion[],
  envId: string | null,
  nowMs: number
): CompareTarget {
  if (mode === "off") return { mode: "off" };
  if (mode === "period") return { mode: "period", offsetMs: rangeMs(range), label: "previous period" };
  if (!envId) return { mode: "unavailable", reason: "Pick one environment to compare with its previous release." };
  const latest = promotions.filter((p) => p.envId === envId && p.promotedAtMs <= nowMs).sort((a, b) => b.promotedAtMs - a.promotedAtMs)[0];
  if (!latest) return { mode: "unavailable", reason: "No release was promoted into this environment in the last 30 days." };
  return {
    mode: "release",
    offsetMs: Math.max(0, nowMs - latest.promotedAtMs),
    label: `before ${latest.version || latest.releaseId.slice(0, 12)}`,
    promotion: latest,
  };
}

/**
 * Alert-firing bands from the alert history: one band per firing stretch of
 * each alert (rule + fingerprint) in the environments in scope, from its
 * `firing` transition to the next `resolved` / `inactive` / `removed` (or now,
 * when it is still firing). With `service`, only the alerts whose labels name
 * that service — an alert about another service is not about this chart.
 */
export function alertBands(events: readonly AlertEvent[], envIds: readonly string[], nowMs: number, service?: string): ChartBand[] {
  const inScope = (e: AlertEvent) => envIds.length === 0 || envIds.includes(e.envId);
  const aboutService = (labels: Record<string, string>) => {
    if (!service) return true;
    const named = labels.service ?? labels.service_name ?? labels["service.name"] ?? labels.job ?? labels.workload;
    return !named || named === service;
  };
  const open = new Map<string, AlertEvent>();
  const bands: ChartBand[] = [];
  const sorted = [...events].filter(inScope).sort((a, b) => a.atMs - b.atMs);
  for (const e of sorted) {
    const key = `${e.envId}\u0000${e.rule}\u0000${e.fingerprint}`;
    if (e.transition === "firing") {
      if (!open.has(key)) open.set(key, e);
      continue;
    }
    const start = open.get(key);
    if (!start) continue;
    open.delete(key);
    if (aboutService(start.labels)) bands.push(band(start, e.atMs));
  }
  for (const start of open.values()) if (aboutService(start.labels)) bands.push(band(start, nowMs));
  return bands.sort((a, b) => a.startSec - b.startSec);
}

function band(start: AlertEvent, endMs: number): ChartBand {
  const minutes = Math.max(1, Math.round((endMs - start.atMs) / 60_000));
  return {
    startSec: Math.floor(start.atMs / 1000),
    endSec: Math.ceil(endMs / 1000),
    label: `${start.rule} firing · ${minutes < 60 ? `${minutes}m` : `${Math.round(minutes / 6) / 10}h`}`,
    tone: "destructive",
  };
}

// ── Layout ───────────────────────────────────────────────────────────────────

/** The grid's columns: forge's 24-column widths, drawn as quarters. */
export const GRID_SPANS = 4;

/** A forge width (1–24; 0 = the kind's default) as a span of the four-column grid. */
export function spanOf(panel: Pick<Panel, "width" | "kind">): number {
  const width = panel.width || (panel.kind === "number" ? 6 : 24);
  return Math.min(GRID_SPANS, Math.max(1, Math.round(width / 6)));
}

export function widthOfSpan(span: number): number {
  return Math.min(GRID_SPANS, Math.max(1, span)) * 6;
}

/** The dashboard with one panel moved to `to` (an index in the panel list). */
export function movePanel(dashboard: Dashboard, panelId: string, to: number): Dashboard {
  const from = dashboard.panels.findIndex((p) => p.id === panelId);
  if (from < 0) return dashboard;
  const panels = [...dashboard.panels];
  const [moved] = panels.splice(from, 1);
  panels.splice(Math.max(0, Math.min(to, panels.length)), 0, moved);
  return { ...dashboard, panels };
}

/** The dashboard with one panel's width set to `span` quarters. */
export function resizePanel(dashboard: Dashboard, panelId: string, span: number): Dashboard {
  return { ...dashboard, panels: dashboard.panels.map((p) => (p.id === panelId ? { ...p, width: widthOfSpan(span) } : p)) };
}

export function removePanel(dashboard: Dashboard, panelId: string): Dashboard {
  return { ...dashboard, panels: dashboard.panels.filter((p) => p.id !== panelId) };
}

/** A panel id not used on the dashboard yet. */
export function newPanelId(dashboard: Dashboard, base = "panel"): string {
  const used = new Set(dashboard.panels.map((p) => p.id));
  for (let i = 1; ; i++) if (!used.has(`${base}-${i}`)) return `${base}-${i}`;
}

/** Whether the dashboard has a `service.name` variable (a filter forge declares on it). */
export function hasServiceVariable(dashboard: Dashboard): boolean {
  return dashboard.filters.some((f) => f.field === "service.name");
}

/** Whether release markers belong on its charts: it declares the `releases` annotation. */
export function marksReleases(dashboard: Dashboard): boolean {
  return (dashboard.annotations ?? []).some((a) => a.source === "releases");
}
