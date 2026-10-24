// Copyright (c) 2025 Reliant Labs

/**
 * The metrics explorer's state and its three spellings: the URL (a shared
 * link), the CLI (`forge obs metrics query …`, or `forge obs query --panel -`)
 * and the panel itself. Pure TS — no React, no fetching — so every rule is
 * testable on its own and the model can be reused wherever a panel is explored.
 *
 * The explorer's QUERY is one forge.dashboards/v1 panel with `signal: "metrics"`
 * (DESIGN §3.1); everything else in ExplorerState is VIEW state, which lives in
 * the URL beside the panel and is never sent to the server.
 */

import type { Panel, Predicate, SeriesIntent } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";
import { compareParam, type CompareMode } from "../../Dashboards/dashboardModel";
import { canonicalPanel, decodePanelParam, encodePanelParam, MAX_PANEL_PARAM_CHARS } from "./canonical";
import { seriesLabel } from "./metricSemantics";

export { allowedAggs, catalogGroup, defaultSeriesFor } from "./metricSemantics";
export { canonicalPanel } from "./canonical";

export const CHARTS = ["line", "area", "bar", "number", "table"] as const;
export type ChartKind = (typeof CHARTS)[number];

export interface ExplorerState {
  /** The query: a metrics panel. */
  panel: Panel;
  /** The metric whose detail pane is open. */
  detailMetric?: string;
  /** The attribute key the breakdown grid is browsing. */
  breakdownKey?: string;
  /** Series labels hidden from the chart. */
  hidden: string[];
  chart: ChartKind;
  compare: CompareMode;
  /** Which tab is open: the chart (default) or the SQL tab. */
  tab?: "sql";
  /**
   * A `kind: "sql"` panel the SQL tab is editing and running: what "Open as SQL
   * panel" makes of the compiled statements, or what a pivot opens (an alert
   * rule's expr). Set, the SQL tab opens on it.
   */
  sqlPanel?: Panel;
}

/** The panel the explorer opens on with nothing chosen. */
export function emptyPanel(): Panel {
  return { id: "explore", title: "Explore", kind: "line", signal: "metrics", series: [] };
}

export function emptyState(): ExplorerState {
  return { panel: emptyPanel(), hidden: [], chart: "line", compare: "off" };
}

const KIND_OF_CHART: Record<ChartKind, string> = { line: "line", area: "line", bar: "bar", number: "number", table: "table" };

function chartOf(param: string | undefined, kind: string): ChartKind {
  if (param === "area" && kind === "line") return "area";
  switch (kind) {
    case "bar":
    case "number":
    case "table":
      return kind;
    default:
      return "line";
  }
}

/** A label list as one URL value: each label percent-encoded (a label may hold a comma), joined by commas. */
function encodeLabels(labels: readonly string[]): string | undefined {
  return labels.length ? labels.map(encodeURIComponent).join(",") : undefined;
}

function decodeLabels(value: string | undefined): string[] {
  if (!value) return [];
  return value.split(",").flatMap((part) => {
    try {
      return [decodeURIComponent(part)];
    } catch {
      return [];
    }
  });
}

/**
 * The state a URL names. Anything missing or damaged falls back to its
 * default — an unreadable `q` opens an empty explorer, never an error page:
 * a stale or hand-edited link must still land somewhere usable.
 */
export function stateFromSearch(search: Record<string, string | undefined>): ExplorerState {
  const panel = decodePanelParam(search.q) ?? emptyPanel();
  const state: ExplorerState = {
    panel,
    hidden: decodeLabels(search.hide),
    chart: chartOf(search.chart, panel.kind),
    compare: compareParam(search.compare),
  };
  if (search.m) state.detailMetric = search.m;
  if (search.bd) state.breakdownKey = search.bd;
  const sqlPanel = decodePanelParam(search.sql);
  if (sqlPanel?.kind === "sql" && sqlPanel.sql) state.sqlPanel = sqlPanel;
  if (search.tab === "sql" || state.sqlPanel) state.tab = "sql";
  return state;
}

/** A SQL panel as the URL carries it: the statement alone, so it can be edited without a query attached. */
export function sqlPanelOf(sql: string, title = "SQL"): Panel {
  return { id: "explore-sql", title, kind: "sql", signal: "metrics", series: [], sql };
}

/**
 * The URL keys for a state. `q` is base64url of the panel's canonical JSON,
 * dropped when it would not fit a link (`linkProblem` says so); every other key
 * is dropped at its default, so the same view is always the same URL.
 */
export function searchFromState(s: ExplorerState): Record<string, string | undefined> {
  const panel = { ...s.panel, kind: KIND_OF_CHART[s.chart] };
  const q = encodePanelParam(panel);
  return {
    q: q.length > MAX_PANEL_PARAM_CHARS ? undefined : q,
    m: s.detailMetric || undefined,
    bd: s.breakdownKey || undefined,
    hide: encodeLabels(s.hidden),
    chart: s.chart === "area" ? "area" : undefined,
    compare: s.compare === "off" ? undefined : s.compare,
    ...sqlSearch(s),
  };
}

/** The SQL tab's keys: `sql` carries the panel when it fits a link, and `tab` only when nothing else implies it. */
function sqlSearch(s: ExplorerState): { tab?: string; sql?: string } {
  const sql = s.sqlPanel?.sql ? encodePanelParam(s.sqlPanel) : undefined;
  const fits = sql !== undefined && sql.length <= MAX_PANEL_PARAM_CHARS;
  return { sql: fits ? sql : undefined, tab: s.tab === "sql" && !fits ? "sql" : undefined };
}

/** Why the state cannot be a link, in words for the reader; null when it can. */
export function linkProblem(s: ExplorerState): string | null {
  const size = encodePanelParam({ ...s.panel, kind: KIND_OF_CHART[s.chart] }).length;
  if (size <= MAX_PANEL_PARAM_CHARS) return null;
  return `This query is too large for a link (${(size / 1024).toFixed(1)} KB of ${MAX_PANEL_PARAM_CHARS / 1024} KB). Copy the panel JSON instead.`;
}

/**
 * The URL that opens the explorer on `panel`: `base` (the explorer's path)
 * plus the state's keys. A panel too large for a link yields the bare path —
 * check `linkProblem` first when linking to a broken explorer would be worse
 * than not linking.
 */
export function explorerHref(base: string, panel: Panel, extra: Partial<ExplorerState> = {}): string {
  const chart = extra.chart ?? chartOf(undefined, panel.kind);
  const search = searchFromState({ hidden: [], compare: "off", ...extra, panel, chart });
  const params = new URLSearchParams();
  for (const key of Object.keys(search).sort()) {
    const value = search[key];
    if (value !== undefined) params.set(key, value);
  }
  const query = params.toString();
  return query ? `${base}?${query}` : base;
}

// ── Breakdown ────────────────────────────────────────────────────────────────

/** How many values the breakdown grid shows. */
export const BREAKDOWN_TOP = 8;

const ID_UNSAFE = /[^A-Za-z0-9_.-]+/g;

/**
 * The panel the breakdown grid runs for one attribute: the same query with
 * `key` as its only group-by, so each value of the key is its own line. A table
 * or bar keeps the `top` heaviest groups (dashspec's `limit`); a line panel has
 * no limit in dashspec, so the series cap is the server's and the view shows the
 * first `top` — see `topSeries`.
 */
export function breakdownPanel(p: Panel, key: string, top: number): Panel {
  const out: Panel = {
    ...p,
    id: `${p.id}-by-${key}`.replace(ID_UNSAFE, "-"),
    title: `${p.title || "Explore"} by ${key}`,
    groupBy: [key],
  };
  if (p.kind === "table" || p.kind === "bar") out.limit = Math.max(1, Math.min(200, Math.floor(top)));
  else delete out.limit;
  return out;
}

/** The first `top` of a result's series, for a line breakdown. */
export function topSeries<T>(series: readonly T[], top: number): T[] {
  return series.slice(0, Math.max(0, top));
}

// ── Copy as CLI ──────────────────────────────────────────────────────────────

// `=` is safe inside a word but not at its start (zsh expands `=cmd`); `!` is never bare (history expansion).
const SAFE_WORD = /^[A-Za-z0-9_@%+:,./-][A-Za-z0-9_@%+:,./=-]*$/;

function shellQuote(word: string): string {
  if (word === "") return "''";
  return SAFE_WORD.test(word) ? word : `'${word.replace(/'/g, `'\\''`)}'`;
}

/** Go's time.ParseDuration has no days: a range or compare offset is spelled in hours past a day. */
export function durationOf(range: RangePreset): string {
  return range === "7d" ? "168h" : range;
}

const FLAG_KINDS = ["line", "number", "table"];

function flagPredicate(p: Predicate): string | null {
  if (!p.field || p.value === undefined || p.value === "") return null;
  if (p.op === "eq") return `${p.field}=${p.value}`;
  if (p.op === "neq") return `${p.field}!=${p.value}`;
  return null;
}

function aggFlag(s: SeriesIntent): { agg: string; quantile?: number } | null {
  if (!s.agg) return null;
  if (s.agg === "quantile") return s.quantile && s.quantile > 0 && s.quantile <= 1 ? { agg: "quantile", quantile: s.quantile } : null;
  return { agg: s.agg };
}

/**
 * Whether the panel is something `forge obs metrics query`'s flags can say, and
 * if not, why. The flags are one metric per series (the first is the positional
 * metric, the rest `--query NAME=…`), equality filters, a formula, group-by,
 * `--top` and the three kinds; the first series is labelled "A" by the verb.
 * Anything beyond them goes through `forge obs query --panel -` instead, which
 * takes the panel verbatim.
 */
export function flagsProblem(p: Panel): string | null {
  if (p.signal !== "metrics") return "not a metrics panel";
  if (!FLAG_KINDS.includes(p.kind)) return `a ${p.kind} panel has no flag`;
  const series = p.series ?? [];
  if (series.length === 0 || series.length > 10) return "needs 1 to 10 series";
  if (p.ratio || p.sql || p.unit || (p.columns ?? []).length || p.funnel) return "uses a ratio, unit, columns or sql";
  if (p.limit && p.kind !== "table") return "a limit on a non-table panel";
  if ((p.groupBy ?? []).length > 3) return "too many group-bys";
  if (series[0]?.label !== seriesLabel(0)) return "the first series is not labelled A";
  const names = new Set<string>();
  for (const [i, s] of series.entries()) {
    if (!s.field || !s.metricType || aggFlag(s) === null || s.thresholds) return `series ${i + 1} is beyond the flags`;
    if (i > 0 && (!/^[A-Za-z0-9_.-]+$/.test(s.label) || names.has(s.label))) return `series ${i + 1} has a label the flags cannot name`;
    names.add(s.label);
    if ((s.where ?? []).some((w) => flagPredicate(w) === null)) return `series ${i + 1} has a filter beyond key=value`;
  }
  if ((p.where ?? []).some((w) => flagPredicate(w) === null)) return "a filter beyond key=value";
  return null;
}

/**
 * `forge obs metrics query` for the explorer's state — or, when the panel is
 * beyond its flags, `forge obs query --panel -` with the panel JSON. Either
 * reaches the same evaluator as the explorer, so the numbers come back the same.
 */
export function cliCommand(env: string, s: ExplorerState, range: RangePreset): { kind: "flags" | "panel"; text: string; panelJson?: string } {
  const panel = canonicalPanel({ ...s.panel, kind: KIND_OF_CHART[s.chart] });
  const since = `--since ${durationOf(range)}`;
  if (flagsProblem(panel) !== null) {
    const json = JSON.stringify(panel, null, 2);
    return { kind: "panel", text: `forge obs query ${shellQuote(env)} --panel - ${since} <<'EOF'\n${json}\nEOF`, panelJson: json };
  }
  const [first, ...rest] = panel.series;
  const parts = ["forge obs metrics query", shellQuote(env), shellQuote(first!.field!)];
  const a = aggFlag(first!)!;
  parts.push(`--agg ${a.agg}`);
  if (a.quantile !== undefined) parts.push(`--quantile ${a.quantile}`);
  for (const q of rest) {
    const agg = aggFlag(q)!;
    const spec = `${q.label}=${q.field}:${agg.agg}${agg.quantile !== undefined ? `:${agg.quantile}` : ""}`;
    parts.push(`--query ${shellQuote(spec)}`);
  }
  for (const [i, q] of panel.series.entries()) {
    const name = i === 0 ? seriesLabel(0) : q.label;
    for (const w of q.where ?? []) parts.push(`--where ${shellQuote(`${name}:${flagPredicate(w)}`)}`);
  }
  for (const w of panel.where ?? []) parts.push(`--filter ${shellQuote(flagPredicate(w)!)}`);
  for (const key of panel.groupBy ?? []) parts.push(`--group-by ${shellQuote(key)}`);
  if (panel.formula) parts.push(`--formula ${shellQuote(panel.formula)}`);
  if (panel.kind !== "line") parts.push(`--kind ${panel.kind}`);
  if (panel.limit) parts.push(`--top ${panel.limit}`);
  if (s.compare === "period") parts.push(`--compare ${durationOf(range)}`);
  else if (s.compare === "release") parts.push("--compare prev-release");
  parts.push(since);
  return { kind: "flags", text: parts.join(" ") };
}
