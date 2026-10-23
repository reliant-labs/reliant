// Copyright (c) 2025 Reliant Labs

/**
 * The explorer's query in its one stable spelling: the same panel always
 * serializes to the same bytes, so a link to it is stable and two links can be
 * compared. Keys are sorted and every field at its default is dropped.
 */

import type { Panel, Predicate, SeriesIntent } from "@/services/controlPlane/observability/types";

function sortKeys<T>(value: T): T {
  if (Array.isArray(value)) return value.map(sortKeys) as unknown as T;
  if (value && typeof value === "object") {
    const out: Record<string, unknown> = {};
    for (const key of Object.keys(value).sort()) out[key] = sortKeys((value as Record<string, unknown>)[key]);
    return out as T;
  }
  return value;
}

function canonicalPredicate(p: Predicate): Predicate {
  const out: Predicate = { field: p.field, op: p.op };
  if (p.value) out.value = p.value;
  if (p.values?.length) out.values = [...p.values];
  return out;
}

function canonicalSeries(s: SeriesIntent): SeriesIntent {
  const out: SeriesIntent = { label: s.label, agg: s.agg };
  if (s.field) out.field = s.field;
  if (s.metricType) out.metricType = s.metricType;
  if (s.agg === "quantile" && s.quantile) out.quantile = s.quantile;
  if (s.where?.length) out.where = s.where.map(canonicalPredicate);
  if (s.thresholds) out.thresholds = { good: s.thresholds.good, poor: s.thresholds.poor };
  return out;
}

/**
 * `p` with sorted keys and every default dropped (an empty list, an empty
 * string, a false flag, a zero). The required fields — id, title, kind, signal,
 * series — always stay. List order is kept: it means something (A is the first
 * series, group-by order is the breakdown order).
 */
export function canonicalPanel(p: Panel): Panel {
  const out: Panel = { id: p.id, title: p.title, kind: p.kind, signal: p.signal, series: (p.series ?? []).map(canonicalSeries) };
  if (p.description) out.description = p.description;
  if (p.ratio) out.ratio = true;
  if (p.where?.length) out.where = p.where.map(canonicalPredicate);
  if (p.groupBy?.length) out.groupBy = [...p.groupBy];
  if (p.columns?.length) out.columns = [...p.columns];
  if (p.funnel) out.funnel = { ...p.funnel };
  if (p.hasError) out.hasError = true;
  if (p.formula?.trim()) out.formula = p.formula.trim();
  if (p.limit) out.limit = p.limit;
  if (p.sql) out.sql = p.sql;
  if (p.unit) out.unit = p.unit;
  if (p.width) out.width = p.width;
  if (p.height) out.height = p.height;
  return sortKeys(out);
}

/** The panel's canonical JSON text. */
export function canonicalJSON(p: Panel): string {
  return JSON.stringify(canonicalPanel(p));
}

function toBase64Url(text: string): string {
  const bytes = new TextEncoder().encode(text);
  let binary = "";
  for (const b of bytes) binary += String.fromCharCode(b);
  return btoa(binary).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function fromBase64Url(text: string): string {
  const padded = text.replace(/-/g, "+").replace(/_/g, "/");
  const binary = atob(padded + "=".repeat((4 - (padded.length % 4)) % 4));
  return new TextDecoder().decode(Uint8Array.from(binary, (c) => c.charCodeAt(0)));
}

/** A panel as the URL carries it: base64url of its canonical JSON. */
export function encodePanelParam(p: Panel): string {
  return toBase64Url(canonicalJSON(p));
}

/** The panel a `q=` value names, canonicalized, or null when it is not a panel (a damaged or hand-edited link). */
export function decodePanelParam(value: string | undefined): Panel | null {
  if (!value || value.length > MAX_PANEL_PARAM_CHARS) return null;
  try {
    const raw = JSON.parse(fromBase64Url(value)) as Partial<Panel> | null;
    if (!raw || typeof raw !== "object" || Array.isArray(raw)) return null;
    return canonicalPanel({
      ...raw,
      id: typeof raw.id === "string" && raw.id ? raw.id : "explore",
      title: typeof raw.title === "string" ? raw.title : "",
      kind: typeof raw.kind === "string" && raw.kind ? raw.kind : "line",
      signal: typeof raw.signal === "string" && raw.signal ? raw.signal : "metrics",
      series: Array.isArray(raw.series) ? raw.series : [],
    });
  } catch {
    return null;
  }
}

/** The most characters a `q=` value may have: 8 KB, so a link survives a proxy, a chat message and an access log. */
export const MAX_PANEL_PARAM_CHARS = 8 * 1024;
