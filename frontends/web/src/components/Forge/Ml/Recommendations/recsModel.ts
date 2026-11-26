// Copyright (c) 2026 Reliant Labs

/**
 * The Recommendations inbox's view-model (02-views §9): filters read from
 * and written to the URL, impact as the sort key (money first, then GPU-hours,
 * then the other units by severity), grouping by category or severity, the
 * Coverage category folded into one "Telemetry coverage" row, the header
 * numbers, and a unified diff split into lines to colour. Pure; tested in
 * __tests__/recsModel.test.ts.
 */

import {
  MlFindingCategory,
  MlFindingState,
  MlFixKind,
  MlSeverity,
  MlSubjectKind,
  type MlFinding,
} from "@/gen/controlplane/services/ml_early_access/v1/ml_early_access_pb";

import { findingCategoryOf, severityOf, severityRank, tsMs } from "../kit";

// ── URL state ─────────────────────────────────────────────────────────────────

export type GroupBy = "category" | "severity" | "none";
export type SortBy = "impact" | "severity" | "age";
/** active: open, assigned, fix proposed. */
export type StateFilter = "active" | "snoozed" | "fixed" | "dismissed" | "all";

export interface RecFilters {
  severity?: string;
  category?: string;
  cluster?: string;
  state: StateFilter;
  sort: SortBy;
}

const CATEGORY_SLUGS: Record<string, MlFindingCategory> = {
  health: MlFindingCategory.HEALTH,
  waste: MlFindingCategory.WASTE,
  scheduling: MlFindingCategory.SCHEDULING,
  startup: MlFindingCategory.STARTUP,
  training: MlFindingCategory.TRAINING_EFFICIENCY,
  inference: MlFindingCategory.INFERENCE,
  coverage: MlFindingCategory.COVERAGE,
  reliability: MlFindingCategory.RELIABILITY,
};
export const CATEGORY_SLUG_OF = Object.fromEntries(Object.entries(CATEGORY_SLUGS).map(([k, v]) => [v, k])) as Record<MlFindingCategory, string>;
const SEVERITY_SLUGS: Record<string, MlSeverity> = { critical: MlSeverity.CRITICAL, warning: MlSeverity.WARNING, info: MlSeverity.INFO };

/** `?filter=sev=critical;cat=waste;cluster=…;state=all;sort=age` → filters (unknown parts ignored). */
export function parseFilter(raw: string | undefined): RecFilters {
  const out: RecFilters = { state: "active", sort: "impact" };
  for (const part of (raw ?? "").split(";")) {
    const [k, v] = part.split("=");
    if (!v) continue;
    if (k === "sev" && SEVERITY_SLUGS[v] !== undefined) out.severity = v;
    if (k === "cat" && CATEGORY_SLUGS[v] !== undefined) out.category = v;
    if (k === "cluster") out.cluster = v;
    if (k === "state" && ["active", "snoozed", "fixed", "dismissed", "all"].includes(v)) out.state = v as StateFilter;
    if (k === "sort" && ["impact", "severity", "age"].includes(v)) out.sort = v as SortBy;
  }
  return out;
}

export function serializeFilter(f: RecFilters): string | undefined {
  const parts = [f.severity && `sev=${f.severity}`, f.category && `cat=${f.category}`, f.cluster && `cluster=${f.cluster}`, f.state !== "active" && `state=${f.state}`, f.sort !== "impact" && `sort=${f.sort}`].filter(Boolean);
  return parts.length ? parts.join(";") : undefined;
}

export const parseGroup = (raw: string | undefined): GroupBy => (raw === "severity" || raw === "none" ? raw : "category");

/** Whether the read must ask for closed findings. */
export const needsClosed = (f: RecFilters) => f.state === "fixed" || f.state === "dismissed" || f.state === "all";

/** How many filters are set (for "Clear filters · n"). */
export const activeFilterCount = (f: RecFilters, q: string | undefined) => [f.severity, f.category, f.cluster, f.state !== "active" ? f.state : undefined, q].filter(Boolean).length;

// ── Filtering and sorting ─────────────────────────────────────────────────────

const ACTIVE = new Set([MlFindingState.OPEN, MlFindingState.ASSIGNED, MlFindingState.FIX_PROPOSED]);

export function stateMatches(state: MlFindingState, filter: StateFilter): boolean {
  switch (filter) {
    case "active":
      return ACTIVE.has(state);
    case "snoozed":
      return state === MlFindingState.SNOOZED;
    case "fixed":
      return state === MlFindingState.FIXED;
    case "dismissed":
      return state === MlFindingState.DISMISSED;
    default:
      return true;
  }
}

export function filterFindings(findings: readonly MlFinding[], f: RecFilters, q?: string): MlFinding[] {
  const needle = q?.trim().toLowerCase();
  return findings.filter(
    (x) =>
      stateMatches(x.state, f.state) &&
      (!f.severity || x.severity === SEVERITY_SLUGS[f.severity]) &&
      (!f.category || x.category === CATEGORY_SLUGS[f.category]) &&
      (!f.cluster || x.clusterIds.includes(f.cluster)) &&
      (!needle || [x.title, x.summary, x.ruleId, ...x.subjects.map((s) => s.label || s.id)].some((t) => t.toLowerCase().includes(needle))),
  );
}

/** Money per day, the comparable impact; GPU-hours priced at nothing break its ties. */
export const impactUsd = (f: MlFinding) => f.impact?.usdPerDay ?? 0;

/** Impact first (money, then GPU-hours), severity breaks ties, then the oldest. */
export function compareImpact(a: MlFinding, b: MlFinding): number {
  return impactUsd(b) - impactUsd(a) || (b.impact?.gpuHoursPerDay ?? 0) - (a.impact?.gpuHoursPerDay ?? 0) || severityRank(a.severity) - severityRank(b.severity) || (tsMs(a.firstSeenAt) ?? 0) - (tsMs(b.firstSeenAt) ?? 0);
}

export function sortFindings(findings: readonly MlFinding[], sort: SortBy): MlFinding[] {
  const out = [...findings];
  if (sort === "severity") return out.sort((a, b) => severityRank(a.severity) - severityRank(b.severity) || compareImpact(a, b));
  if (sort === "age") return out.sort((a, b) => (tsMs(b.firstSeenAt) ?? 0) - (tsMs(a.firstSeenAt) ?? 0));
  return out.sort(compareImpact);
}

// ── Grouping ──────────────────────────────────────────────────────────────────

export interface FindingGroup {
  key: string;
  label: string;
  findings: MlFinding[];
  usdPerDay: number;
  gpuHoursPerDay: number;
  /** The Coverage category: drawn as one collapsed row until opened. */
  coverage?: boolean;
}

function summarize(key: string, label: string, findings: MlFinding[], coverage?: boolean): FindingGroup {
  return {
    key,
    label,
    findings,
    usdPerDay: findings.reduce((a, f) => a + impactUsd(f), 0),
    gpuHoursPerDay: findings.reduce((a, f) => a + (f.impact?.gpuHoursPerDay ?? 0), 0),
    coverage,
  };
}

/**
 * Groups of already-sorted findings. By category: groups by money, worst
 * severity breaking ties, Coverage always last and flagged. By severity:
 * critical first. None: one group, Coverage still folded into its own row.
 */
export function groupFindings(sorted: readonly MlFinding[], by: GroupBy): FindingGroup[] {
  const coverage = sorted.filter((f) => f.category === MlFindingCategory.COVERAGE);
  const rest = sorted.filter((f) => f.category !== MlFindingCategory.COVERAGE);
  const coverageGroup = coverage.length ? [summarize("coverage", findingCategoryOf(MlFindingCategory.COVERAGE).label, coverage, true)] : [];
  if (by === "none") return [...(rest.length ? [summarize("all", "All findings", rest)] : []), ...coverageGroup];
  const keyOf = (f: MlFinding) => (by === "severity" ? f.severity : f.category);
  const map = new Map<number, MlFinding[]>();
  for (const f of rest) map.set(keyOf(f), [...(map.get(keyOf(f)) ?? []), f]);
  const groups = [...map.entries()].map(([k, fs]) => summarize(String(k), by === "severity" ? severityOf(k as MlSeverity).label : findingCategoryOf(k as MlFindingCategory).label, fs));
  if (by === "severity") groups.sort((a, b) => severityRank(Number(a.key) as MlSeverity) - severityRank(Number(b.key) as MlSeverity));
  else groups.sort((a, b) => b.usdPerDay - a.usdPerDay || Math.min(...a.findings.map((f) => severityRank(f.severity))) - Math.min(...b.findings.map((f) => severityRank(f.severity))));
  return [...groups, ...coverageGroup];
}

// ── Header numbers ────────────────────────────────────────────────────────────

export interface InboxKpis {
  open: number;
  critical: number;
  usdPerDay: number;
  gpuHoursPerDay: number;
  runsAtRisk: number;
  coverageGaps: number;
  coverageRatio: number;
}

/** Over the ACTIVE findings, whatever the filters (the page's question is the whole inbox). */
export function inboxKpis(findings: readonly MlFinding[], coverageRatio: number): InboxKpis {
  const active = findings.filter((f) => ACTIVE.has(f.state));
  const runs = new Set(active.filter((f) => f.category !== MlFindingCategory.COVERAGE && f.severity !== MlSeverity.INFO).flatMap((f) => f.subjects.filter((s) => s.kind === MlSubjectKind.RUN).map((s) => s.id)));
  return {
    open: active.length,
    critical: active.filter((f) => f.severity === MlSeverity.CRITICAL).length,
    usdPerDay: active.reduce((a, f) => a + impactUsd(f), 0),
    gpuHoursPerDay: active.reduce((a, f) => a + (f.impact?.gpuHoursPerDay ?? 0), 0),
    runsAtRisk: runs.size,
    coverageGaps: active.filter((f) => f.category === MlFindingCategory.COVERAGE).length,
    coverageRatio,
  };
}

// ── Fix and diff ──────────────────────────────────────────────────────────────

export const fixKindLabel = (k: MlFixKind | undefined) => (k === MlFixKind.KCL_PR ? "KCL PR" : k === MlFixKind.COMMAND ? "Command" : k === MlFixKind.EXPLAIN ? "Explain" : "—");

export interface DiffLine {
  kind: "file" | "hunk" | "add" | "del" | "ctx";
  text: string;
}

export function parseDiff(diff: string): DiffLine[] {
  return diff
    .replace(/\n$/, "")
    .split("\n")
    .map((text) => ({
      kind: text.startsWith("+++") || text.startsWith("---") ? "file" : text.startsWith("@@") ? "hunk" : text.startsWith("+") ? "add" : text.startsWith("-") ? "del" : "ctx",
      text,
    }));
}

/** "#412" from a GitHub PR URL. */
export function prNumber(url: string): string {
  const m = /\/pull\/(\d+)/.exec(url);
  return m ? `#${m[1]}` : "PR";
}
