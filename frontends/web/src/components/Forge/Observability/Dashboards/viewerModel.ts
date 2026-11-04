// Copyright (c) 2025 Reliant Labs

import type { AttributeFilter, Dashboard, DashboardFilter, Panel } from "@/services/controlPlane/observability/types";
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
