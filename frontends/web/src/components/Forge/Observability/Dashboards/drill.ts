// Copyright (c) 2025 Reliant Labs

/**
 * Drill-down: a dashboard series or row, as the Logs / Traces / workload
 * links that show what is behind it.
 *
 * A series is identified by its group (`{"span.name": "CreateOrder"}`) on top
 * of the panel's own filters and the dashboard's variables. The explorers
 * speak the console's URL vocabulary (obsSearch.ts), so each group field is
 * translated:
 *
 *   service.name              the explorers' `service`
 *   deployment.environment.*  nothing: the environment is the console scope
 *   span.name (traces)        the Traces root-operation match (`q`)
 *   *.attr.<key>, other keys  an attribute filter token (`f`), when the key
 *                             is one the scope allowlists
 *
 * A panel that narrows to failures (`span.status = error`, `log.severity =
 * error`) carries that across: Traces with errors only, Logs at error and
 * above.
 */

import type { Panel, Predicate } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";

import { ENV_KEY, isAttributeKeyShaped, SERVICE_KEY } from "../attributes";
import { filterParam, type FilterToken } from "../obsFilters";
import type { ObsSearch } from "../obsSearch";

export interface ExploreHrefs {
  logs: (search: ObsSearch) => string;
  traces: (search: ObsSearch) => string;
  /** The workload's page (the Overview's workload detail); "" when no single workload is meant. */
  workload: (name: string) => string;
}

export interface DrillContext {
  range: RangePreset;
  /** The dashboard's service variable. */
  service?: string;
}

const PREFIXES = ["resource.attr.", "span.attr.", "log.attr.", "metric.attr."];

/** A panel field as a scope attribute key, or null when the scope cannot filter on it. */
export function scopeKeyOf(field: string): string | null {
  if (field === SERVICE_KEY || field === ENV_KEY) return null;
  for (const prefix of PREFIXES) if (field.startsWith(prefix)) return isAttributeKeyShaped(field.slice(prefix.length)) ? field.slice(prefix.length) : null;
  if (field.startsWith("span.") || field.startsWith("log.")) return null;
  return isAttributeKeyShaped(field) ? field : null;
}

const isErrorPredicate = (p: Predicate) =>
  (p.field === "log.severity" || p.field === "span.status") && p.op === "eq" && (p.value ?? "").toLowerCase() === "error";

/** Whether the panel (or every one of its series) counts failures only. */
export function countsErrors(panel: Panel): boolean {
  if ((panel.where ?? []).some(isErrorPredicate)) return true;
  return panel.series.length > 0 && panel.series.every((s) => (s.where ?? []).some(isErrorPredicate));
}

/** The service a series is about: its group's, the panel's own `service.name = …`, or the variable. */
export function serviceOf(panel: Panel, group: Record<string, string>, variable?: string): string | undefined {
  return group[SERVICE_KEY] || (panel.where ?? []).find((p) => p.field === SERVICE_KEY && p.op === "eq")?.value || variable || undefined;
}

export function drillSearches(panel: Panel, group: Record<string, string>, context: DrillContext): { logs: ObsSearch; traces: ObsSearch; service?: string } {
  const service = serviceOf(panel, group, context.service);
  const tokens: FilterToken[] = [];
  let operation: string | undefined;
  for (const [field, value] of Object.entries(group)) {
    if (!value) continue;
    if (field === "span.name") {
      operation = value;
      continue;
    }
    const key = scopeKeyOf(field);
    if (key) tokens.push({ key, op: "eq", value });
  }
  // Carry temporary trace/resource predicates into the explorers. Metric-only
  // attributes (for example procedure) must not become nonexistent trace keys.
  for (const predicate of panel.where ?? []) {
    if (predicate.op !== "eq" || !predicate.value) continue;
    if (predicate.field === "span.name") operation ??= predicate.value;
    if (!predicate.field.startsWith("span.attr.") && !predicate.field.startsWith("resource.attr.")) continue;
    const key = scopeKeyOf(predicate.field);
    if (key && !tokens.some((token) => token.key === key)) tokens.push({ key, op: "eq", value: predicate.value });
  }
  const errors = countsErrors(panel);
  const f = filterParam(tokens);
  return {
    service,
    logs: { range: context.range, service, f, severity: errors ? "error,fatal" : undefined },
    traces: { range: context.range, service, f, q: operation, errors: errors || undefined },
  };
}
