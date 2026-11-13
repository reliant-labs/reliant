// Copyright (c) 2025 Reliant Labs

import type { AttributeFilter, Dashboard, DashboardFilter, DashboardSection, Panel, PanelResult } from "@/services/controlPlane/observability/types";
import type { DashboardSearch } from "./dashboardModel";

export function targetsPanel(filter: DashboardFilter, panel: Panel): boolean {
  return !filter.panelIds?.length || filter.panelIds.includes(panel.id);
}

/** QueryScope uses OTel keys; panel intent uses forge's semantic field vocabulary. */
export function attributeKey(field: string): string {
  return field.replace(/^(span|resource|log|metric)\.attr\./, "");
}

export function viewerFilterValue(filter: DashboardFilter, search: DashboardSearch): string | undefined {
  switch (attributeKey(filter.field)) {
    case "service.name": return search.service;
    case "rpc.service": return search.rpcService;
    case "rpc.method": return search.rpcMethod;
    case "procedure": return search.rpcProcedure;
    default: return search.variables?.[filter.id];
  }
}

export function visiblePanel(panel: Panel, search: DashboardSearch): boolean {
  return !search.hiddenPanels?.includes(panel.id) && !(panel.section && search.hiddenSections?.includes(panel.section));
}

/** One run of the grid: a section's heading and its panels, or the panels that name no section (no heading). */
export interface PanelGroup {
  section?: DashboardSection;
  /** The viewer hid the section: its heading stays, to show it again. */
  hidden: boolean;
  panels: Panel[];
}

/**
 * The grid as a viewer sees it (forge dashspec Section): the panels that name
 * no declared section first, then each section in the order the dashboard
 * declares it, under its heading, its panels in their own order. A section
 * with no panels draws no heading; a hidden one keeps its heading and draws
 * no panels.
 */
export function panelGroups(dashboard: Pick<Dashboard, "sections" | "panels">, search: DashboardSearch): PanelGroup[] {
  const sections = dashboard.sections ?? [];
  const declared = new Set(sections.map((section) => section.id));
  const shown = (panel: Panel) => !search.hiddenPanels?.includes(panel.id);
  const groups: PanelGroup[] = [];
  const loose = dashboard.panels.filter((panel) => !(panel.section && declared.has(panel.section)) && shown(panel));
  if (loose.length) groups.push({ hidden: false, panels: loose });
  for (const section of sections) {
    const members = dashboard.panels.filter((panel) => panel.section === section.id);
    if (!members.length) continue;
    const hidden = !!search.hiddenSections?.includes(section.id);
    groups.push({ section, hidden, panels: hidden ? [] : members.filter(shown) });
  }
  return groups;
}

/** A sql result that is one row of figures: numeric columns only, one row, nothing cut. Never collapse several rows or groups. */
export function sqlAggregate(data: Pick<PanelResult, "truncated" | "rows" | "columns">): boolean {
  return !data.truncated && data.rows.length === 1 && data.columns.length > 0 && Object.keys(data.rows[0].group).length === 0;
}

/** A figure column that holds the period before the figure beside it ("Previous 7 days"). */
const PREVIOUS = /^previous\b/i;

/**
 * A row of figures that carries its own comparison: each figure followed by a
 * column named "Previous …" (forge's built-in Overview counts users over a
 * fixed 7 days, whatever the range, and returns the 7 days before beside
 * them). Split into the figures and their previous values, labelled from
 * that column ("previous 7 days"), so each figure is drawn once, with its
 * change. Null when the row has no such column.
 */
export function sqlPreviousFigures(data: PanelResult): { current: PanelResult; previous: PanelResult; label: string } | null {
  if (!sqlAggregate(data)) return null;
  const cols = data.columns;
  const pairs: { at: number; prev: number }[] = [];
  for (let i = 0; i < cols.length - 1; i++) {
    if (!PREVIOUS.test(cols[i]) && PREVIOUS.test(cols[i + 1])) pairs.push({ at: i, prev: i + 1 });
  }
  if (!pairs.length) return null;
  const kept = cols.map((_, i) => i).filter((i) => !pairs.some((p) => p.prev === i));
  const values = data.rows[0].values;
  const row = (pick: (i: number) => number | null) => [{ group: {}, values: kept.map(pick) }];
  return {
    current: { ...data, columns: kept.map((i) => cols[i]), rows: row((i) => values[i] ?? null) },
    previous: { ...data, columns: kept.map((i) => cols[i]), rows: row((i) => values[pairs.find((p) => p.at === i)?.prev ?? -1] ?? null) },
    label: cols[pairs[0].prev].toLowerCase(),
  };
}

/** sql styles that draw no figure, so take no sparkline and no comparison. */
const FIGURELESS_STYLES = new Set(["funnel", "heatmap"]);

/** Whether a sql panel draws figures from this answer: the ones a comparison and a sparkline go with. */
export function drawsSqlFigures(panel: Pick<Panel, "kind" | "display">, data: PanelResult | undefined): boolean {
  return panel.kind === "sql" && !FIGURELESS_STYLES.has(panel.display?.style ?? "") && !!data && data.series.length === 0 && sqlAggregate(data);
}

/** The query a sql panel's sparkline runs (its trendSql, as a sql panel of its own), or undefined when it has none. */
export function sqlTrendPanel(panel: Panel): Panel | undefined {
  if (panel.kind !== "sql" || !panel.trendSql?.trim() || FIGURELESS_STYLES.has(panel.display?.style ?? "")) return undefined;
  return { ...panel, id: `${panel.id}.trend`, sql: panel.trendSql, trendSql: undefined, display: undefined };
}

const GROUPABLE = new Set(["line", "number", "table", "bar"]);

/** Offer only fields already declared by the dashboard, never invented dimensions. */
export function breakdownFields(dashboard: Dashboard): string[] {
  return [...new Set([
    ...dashboard.panels.filter((p) => GROUPABLE.has(p.kind)).flatMap((p) => p.groupBy ?? []),
    ...dashboard.filters.map((f) => f.field),
  ])];
}

function breakdownTargets(dashboard: Dashboard, panel: Panel, field: string): boolean {
  const declarations = dashboard.filters.filter((f) => f.field === field);
  return declarations.length ? declarations.some((f) => targetsPanel(f, panel)) : !!panel.groupBy?.includes(field);
}

/** Transient query projection. The original panel and saved spec are never mutated. */
export function viewerQuery(dashboard: Dashboard, panel: Panel, search: DashboardSearch): { panel: Panel; filters: AttributeFilter[]; services: string[] } {
  const filters: AttributeFilter[] = [];
  const where = [...(panel.where ?? [])];
  let services: string[] = [];
  for (const filter of dashboard.filters) {
    const value = viewerFilterValue(filter, search);
    if (!value || !targetsPanel(filter, panel)) continue;
    const key = attributeKey(filter.field);
    if (key === "service.name") services = [value];
    // Intent predicates keep metric.attr.procedure distinct from span attributes.
    else where.push({ field: filter.field, op: "eq", value });
  }
  const breakdown = search.breakdown;
  let queryPanel = where.length !== (panel.where?.length ?? 0) ? { ...panel, where } : panel;
  if (GROUPABLE.has(panel.kind) && panel.signal && breakdown) {
    if (breakdown === "none") queryPanel = { ...queryPanel, groupBy: [] };
    else if (breakdownFields(dashboard).includes(breakdown) && breakdownTargets(dashboard, panel, breakdown)) {
      queryPanel = { ...queryPanel, groupBy: [breakdown] };
    }
  }
  return { panel: queryPanel, services, filters };
}

export function toggledHidden(hidden: readonly string[] | undefined, id: string): string[] | undefined {
  const next = new Set(hidden);
  if (next.has(id)) next.delete(id); else next.add(id);
  return next.size ? [...next] : undefined;
}
