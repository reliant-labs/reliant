// Copyright (c) 2025 Reliant Labs

/**
 * The prompt "Ask Reliant" puts in a new chat's composer, as a pure function of
 * REFERENCES.
 *
 * ── REFERENCES ONLY, NEVER TELEMETRY ─────────────────────────────────────────
 *
 * The console is looking at an issue, a trace, an alert or a panel; the agent
 * that picks it up has the forge CLI and its own credentials, so it fetches the
 * details itself. This module therefore writes ids, environment names, rule
 * names, a window and the panel's own definition — and never a message, a
 * stack, a log line or an attribute value, even though the console has them on
 * screen. Telemetry carries customer data and secrets; the composer's draft is
 * persisted, synced and sent to a model, so none of it goes in by copy-paste.
 * What a subject carries is therefore a closed list of reference fields, and
 * the type below is that list: there is nowhere to hand this module a message.
 *
 * Every command quoted here was checked against `reliant forge obs <verb>
 * --help`. A change to a verb's syntax belongs in this file and its test.
 */

import type { Panel } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";

/** The environment a reference lives in: a name, or null when the console cannot say. */
export type AskEnv = string | null;

export type AskSubject =
  | { kind: "issue"; issueId: string; env: AskEnv }
  | { kind: "trace"; traceId: string; env: AskEnv }
  | { kind: "graph"; env: AskEnv; orgId: string; projectId?: string; envIds: string[]; startMs?: number; endMs?: number; nodeId?: string; edgeId?: string; rpcId?: string; traceIds: string[]; origin: string; truncated: boolean; provisional: boolean }
  | { kind: "alert"; rule: string; fingerprint: string; env: AskEnv }
  | { kind: "panel"; panel: Panel; dashboard?: string; window: RangePreset; env: AskEnv };

export interface AskContext {
  /** The forge project's name (forge.yaml), when the console knows it. */
  forgeProject?: string;
  /** Every environment of the project, offered when a subject's own is unknown. */
  envChoices?: readonly string[];
}

/** The placeholder a command carries when the environment is unknown. */
const ENV_PLACEHOLDER = "<env>";

/** A Go duration for `--since`: the CLI parses "168h", not "7d". */
const SINCE: Record<RangePreset, string> = { "15m": "15m", "1h": "1h", "6h": "6h", "24h": "24h", "7d": "168h" };

const SAFE_ARG = /^[A-Za-z0-9_@%+=:,./-]+$/;

/** One shell word. Ids are already safe; a rule or metric name is quoted if it is not. */
export function shellWord(value: string): string {
  const flat = value.replace(/[\r\n]+/g, " ");
  return SAFE_ARG.test(flat) ? flat : `'${flat.replace(/'/g, `'\\''`)}'`;
}

/** Free text from a user-authored definition (a panel title), made safe for one prose line. */
function oneLine(value: string, max = 120): string {
  const flat = Array.from(value, (ch) => (ch.charCodeAt(0) < 0x20 || ch === "\u007f" || ch === "`" ? " " : ch)).join("").replace(/\s+/g, " ").trim();
  return flat.length > max ? `${flat.slice(0, max - 1)}…` : flat;
}

const cmd = (...words: string[]) => `\`${["reliant", "forge", "obs", ...words].join(" ")}\``;

function where(subject: AskSubject, ctx: AskContext): string {
  const project = ctx.forgeProject ? ` (forge project ${oneLine(ctx.forgeProject, 80)})` : "";
  if (subject.env) return ` in environment ${oneLine(subject.env, 80)}${project}`;
  const choices = ctx.envChoices?.length ? `, one of ${ctx.envChoices.map((name) => oneLine(name, 80)).join(", ")}` : "";
  return ` in an environment the console could not name${choices}${project}`;
}

const envArg = (subject: AskSubject) => (subject.env ? shellWord(subject.env) : ENV_PLACEHOLDER);

const FETCH = "Fetch the details yourself with the forge CLI";

function issuePrompt(subject: Extract<AskSubject, { kind: "issue" }>, ctx: AskContext): string {
  const id = shellWord(subject.issueId);
  return `Issue ${id}${where(subject, ctx)}.\n\n${FETCH}, starting with ${cmd("issue", id, envArg(subject))}.`;
}

function tracePrompt(subject: Extract<AskSubject, { kind: "trace" }>, ctx: AskContext): string {
  const id = shellWord(subject.traceId);
  const env = envArg(subject);
  return `Trace ${id}${where(subject, ctx)}.\n\n${FETCH}, starting with ${cmd("trace", id, env)} (its logs: ${cmd("logs", env, "--trace", id)}).`;
}

function alertPrompt(subject: Extract<AskSubject, { kind: "alert" }>, ctx: AskContext): string {
  const rule = shellWord(subject.rule);
  const env = envArg(subject);
  return (
    `Alert rule ${rule} (fingerprint ${shellWord(subject.fingerprint)})${where(subject, ctx)}.\n\n` +
    `${FETCH}: ${cmd("status", env, "--json", "--fields", "alerts")} lists what is firing, and ` +
    `${cmd("alerts", "check", "--env", env, "--rule", rule, "--sql")} prints the rule's expression.`
  );
}

/** The panel fields that define its query. Not its description or layout, which say nothing about the data. */
const QUERY_FIELDS = ["id", "title", "kind", "signal", "series", "ratio", "formula", "where", "groupBy", "columns", "funnel", "hasError", "limit", "sql", "unit"] as const;

/** The longest panel definition quoted in a prompt; a longer one is left on the dashboard. */
export const MAX_PANEL_SPEC_CHARS = 4000;

export function panelQuery(panel: Panel): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  for (const key of QUERY_FIELDS) {
    const value = panel[key];
    if (value !== undefined && value !== null && !(Array.isArray(value) && value.length === 0)) out[key] = value;
  }
  return out;
}

const QUANTILE_AGG: Record<number, string> = { 0.5: "p50", 0.95: "p95", 0.99: "p99" };

/** forge's panel aggregations that `obs metrics query --agg` spells the same way. */
function cliAgg(agg: string, quantile: number | undefined): string | null {
  if (agg === "quantile") return (quantile !== undefined && QUANTILE_AGG[quantile]) || null;
  return ["sum", "avg", "max", "last", "count"].includes(agg) ? agg : null;
}

type Predicate = NonNullable<Panel["where"]>[number];

function cliPredicates(flag: string, predicates: readonly Predicate[], prefix = ""): string[] | null {
  const out: string[] = [];
  for (const p of predicates) {
    if (p.value === undefined || (p.op !== "eq" && p.op !== "neq")) return null;
    out.push(flag, shellWord(`${prefix}${p.field}${p.op === "eq" ? "=" : "!="}${p.value}`));
  }
  return out;
}

/**
 * `obs metrics query` for a panel it can express exactly: one metrics series
 * with an aggregation, equality filters and at most three group-bys. Anything
 * else (a formula, a ratio, a regex filter, a distinct count) returns null, and
 * the prompt falls back to running the panel's own JSON.
 */
export function metricsQueryCommand(panel: Panel, env: string, since: string): string | null {
  const [series] = panel.series;
  if (panel.signal !== "metrics" || panel.series.length !== 1 || !series.field || panel.formula || panel.ratio) return null;
  if (!["line", "number", "table", "bar"].includes(panel.kind)) return null;
  const agg = cliAgg(series.agg, series.quantile);
  const filters = cliPredicates("--filter", panel.where ?? []);
  const seriesFilters = cliPredicates("--where", series.where ?? [], "A:");
  if (!agg || !filters || !seriesFilters || (panel.groupBy ?? []).length > 3) return null;
  const groups = (panel.groupBy ?? []).flatMap((key) => ["--group-by", shellWord(key)]);
  return cmd("metrics", "query", env, shellWord(series.field), "--agg", agg, ...groups, ...filters, ...seriesFilters, "--since", since);
}

function panelPrompt(subject: Extract<AskSubject, { kind: "panel" }>, ctx: AskContext): string {
  const { panel, window } = subject;
  const since = SINCE[window];
  const env = envArg(subject);
  const dashboard = subject.dashboard ? ` of dashboard "${oneLine(subject.dashboard)}"` : "";
  const head = `Dashboard panel "${oneLine(panel.title || panel.id)}"${dashboard}${where(subject, ctx)}, last ${window}.`;
  const spec = JSON.stringify(panelQuery(panel));
  const query = spec.length <= MAX_PANEL_SPEC_CHARS ? `Query: ${spec}` : `Query: too long to quote; read panel ${shellWord(panel.id)} from the dashboard.`;

  const exact = subject.env ? metricsQueryCommand(panel, env, since) : null;
  const run = exact
    ? `${exact} runs this panel's query`
    : `save the Query above as panel.json and run ${cmd("query", env, "--panel", "panel.json", "--since", since)}, or read the raw signal with ${cmd(RAW_VERB[panel.signal] ?? "query", env, "--since", since)}`;
  return `${head}\n${query}\n\n${FETCH}: ${run}.`;
}

/** The structured verb that reads a signal as it is, without the panel's shaping. */
const RAW_VERB: Record<string, string> = { traces: "traces", logs: "logs", metrics: "metrics list" };

export function buildAskReliantPrompt(subject: AskSubject, ctx: AskContext = {}): string {
  switch (subject.kind) {
    case "graph":
      return `Investigate this service graph${where(subject, ctx)}. References only: ${JSON.stringify({
        orgId: subject.orgId, projectId: subject.projectId, envIds: subject.envIds.slice(0, 50),
        startMs: subject.startMs, endMs: subject.endMs, nodeId: subject.nodeId, edgeId: subject.edgeId,
        rpcId: subject.rpcId, traceIds: subject.traceIds.slice(0, 3), origin: subject.origin,
        truncated: subject.truncated, provisional: subject.provisional,
      })}\n\nFetch evidence through authorized read-only observability tools. Missing observations are not zero traffic. Distinguish hypotheses from evidence; never invoke the explored RPC or mutate the app.`;
    case "issue":
      return issuePrompt(subject, ctx);
    case "trace":
      return tracePrompt(subject, ctx);
    case "alert":
      return alertPrompt(subject, ctx);
    case "panel":
      return panelPrompt(subject, ctx);
  }
}

/**
 * The environment of a trace: the scope's one environment when the reader
 * narrowed to it, else the one the root span's resource names by id. Null when
 * neither says (the scope spans environments and the span carries no id) — the
 * prompt then lists the choices instead of guessing, because `obs trace`
 * defaults to dev and would read the wrong one.
 */
export function traceEnvName(
  scopeEnvIds: readonly string[],
  environments: readonly { id: string; name: string }[],
  rootResource: Record<string, string> | undefined
): AskEnv {
  const id = scopeEnvIds.length === 1 ? scopeEnvIds[0] : rootResource?.["reliant.env.id"];
  return environments.find((env) => env.id === id)?.name ?? null;
}

/** A workspace name for the investigation: ask-<kind>-<what>-<nonce>, a legal git branch. */
export function askWorkspaceName(subject: AskSubject, nonce: string): string {
  const what =
    subject.kind === "graph" ? subject.rpcId || subject.nodeId || subject.edgeId || "graph" :
    subject.kind === "issue"
      ? subject.issueId
      : subject.kind === "trace"
        ? subject.traceId.slice(0, 12)
        : subject.kind === "alert"
          ? subject.rule
          : subject.panel.title || subject.panel.id;
  const slug = what
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 40)
    .replace(/-+$/g, "");
  return ["ask", subject.kind, slug, nonce].filter(Boolean).join("-");
}
