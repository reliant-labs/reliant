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
 * Release versions, timestamps and an alert's state are references too; an
 * issue's title, culprit and frames and an alert's labels and annotations are
 * not (they are read off telemetry), so `obs issue` and `obs status` fetch them.
 *
 * An intent ("investigate" or "fix", the console's two actions) appends one
 * instruction saying what the chat is for.
 *
 * Every command quoted here was checked against `reliant forge obs <verb>
 * --help`. A change to a verb's syntax belongs in this file and its test.
 */

import type { Panel } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";

/** The environment a reference lives in: a name, or null when the console cannot say. */
export type AskEnv = string | null;

export type AskSubject =
  | {
      kind: "issue";
      issueId: string;
      env: AskEnv;
      /** The trace of its latest event: an id, like the issue's. */
      latestTraceId?: string;
      /** The release (or build version) it was first and last seen in. */
      firstVersion?: string;
      lastVersion?: string;
    }
  | { kind: "trace"; traceId: string; env: AskEnv }
  | { kind: "graph"; env: AskEnv; orgId: string; projectId?: string; envIds: string[]; startMs?: number; endMs?: number; nodeId?: string; edgeId?: string; rpcId?: string; traceIds: string[]; origin: string; truncated: boolean; provisional: boolean }
  | {
      kind: "alert";
      rule: string;
      fingerprint: string;
      env: AskEnv;
      state?: "firing" | "pending";
      /** When it entered that state. */
      sinceMs?: number | null;
    }
  | { kind: "panel"; panel: Panel; dashboard?: string; window: RangePreset; env: AskEnv }
  /** A workload that is crash-looping or degraded, or a Job whose latest run failed. */
  | {
      kind: "workload";
      name: string;
      env: AskEnv;
      /** A Job (migration, backup), whose runs are read rather than its pods. */
      job?: boolean;
      /** The console's verdict, a reference: "crash-looping", "degraded", "failed". */
      state?: string;
      /** The release it runs (or the failed run ran). */
      release?: string;
    }
  /** A release whose health check said it made things worse, or could not say. */
  | { kind: "release"; version: string; env: AskEnv; verdict?: "fail" | "inconclusive" }
  /** A deploy that failed (a deploy run, or its job). */
  | { kind: "deploy-failed"; env: AskEnv; runId?: string; version?: string }
  /** A browser session that hit errors. */
  | { kind: "session"; sessionId: string; env: AskEnv }
  /** Nothing is deployed here yet: get the app running. */
  | { kind: "deploy"; env: AskEnv };

/**
 * What the chat is for. "investigate": find the cause and explain it, change
 * nothing. "fix": find it and fix it on the chat's own branch. Absent: the
 * prompt states the subject and how to read it, and the reader says what they want.
 */
export type AskIntent = "investigate" | "fix";

/** How the reader's agent spells the forge CLI: Reliant's bundled `reliant forge`, or a standalone `forge`. Same commands. */
export type AskCli = "reliant forge" | "forge";

export interface AskContext {
  /** The forge project's name (forge.yaml), when the console knows it. */
  forgeProject?: string;
  /** Every environment of the project, offered when a subject's own is unknown. */
  envChoices?: readonly string[];
  intent?: AskIntent;
  /** The CLI the commands name. Default "reliant forge". */
  cli?: AskCli;
  /**
   * Who reads the prompt. "reliant" (default): a Reliant chat that has a
   * workspace of its own. "external": the reader's own agent (Claude Code,
   * Codex, Cursor), pasted from the handoff sheet — the prompt then claims no
   * workspace or branch, because there is none.
   */
  host?: "reliant" | "external";
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

const cliOf = (ctx: AskContext): AskCli => ctx.cli ?? "reliant forge";

const cmd = (ctx: AskContext, ...words: string[]) => `\`${[cliOf(ctx), "obs", ...words].join(" ")}\``;

function where(subject: AskSubject, ctx: AskContext): string {
  const project = ctx.forgeProject ? ` (forge project ${oneLine(ctx.forgeProject, 80)})` : "";
  if (subject.env) return ` in environment ${oneLine(subject.env, 80)}${project}`;
  const choices = ctx.envChoices?.length ? `, one of ${ctx.envChoices.map((name) => oneLine(name, 80)).join(", ")}` : "";
  return ` in an environment the console could not name${choices}${project}`;
}

const envArg = (subject: AskSubject) => (subject.env ? shellWord(subject.env) : ENV_PLACEHOLDER);

const FETCH = "Fetch the details yourself with the forge CLI";

/** The releases an issue was seen in, as one prose line; "" when the console knows neither. */
function versionsLine(first: string | undefined, last: string | undefined): string {
  const from = first ? oneLine(first, 80) : "";
  const to = last ? oneLine(last, 80) : "";
  if (from && to && from !== to) return `\nFirst seen in ${from}, last seen in ${to}.`;
  if (from || to) return `\nSeen in ${from || to}.`;
  return "";
}

function issuePrompt(subject: Extract<AskSubject, { kind: "issue" }>, ctx: AskContext): string {
  const id = shellWord(subject.issueId);
  const env = envArg(subject);
  const trace = subject.latestTraceId ? ` (its latest event's trace: ${cmd(ctx, "trace", shellWord(subject.latestTraceId), env)})` : "";
  return `Issue ${id}${where(subject, ctx)}.${versionsLine(subject.firstVersion, subject.lastVersion)}\n\n${FETCH}, starting with ${cmd(ctx, "issue", id, env)}${trace}.`;
}

function tracePrompt(subject: Extract<AskSubject, { kind: "trace" }>, ctx: AskContext): string {
  const id = shellWord(subject.traceId);
  const env = envArg(subject);
  return `Trace ${id}${where(subject, ctx)}.\n\n${FETCH}, starting with ${cmd(ctx, "trace", id, env)} (its logs: ${cmd(ctx, "logs", env, "--trace", id)}).`;
}

function alertPrompt(subject: Extract<AskSubject, { kind: "alert" }>, ctx: AskContext): string {
  const rule = shellWord(subject.rule);
  const env = envArg(subject);
  const state = subject.state ? `\n${subject.state === "firing" ? "Firing" : "Pending"}${subject.sinceMs ? ` since ${new Date(subject.sinceMs).toISOString()}` : ""}.` : "";
  return (
    `Alert rule ${rule}${subject.fingerprint ? ` (fingerprint ${shellWord(subject.fingerprint)})` : ""}${where(subject, ctx)}.${state}\n\n` +
    `${FETCH}: ${cmd(ctx, "status", env, "--json", "--fields", "alerts")} lists what is firing, and ` +
    `${cmd(ctx, "alerts", "check", "--env", env, "--rule", rule, "--sql")} prints the rule's expression.`
  );
}

const forgeCmd = (ctx: AskContext, ...words: string[]) => `\`${[cliOf(ctx), ...words].join(" ")}\``;

function workloadPrompt(subject: Extract<AskSubject, { kind: "workload" }>, ctx: AskContext): string {
  const name = shellWord(subject.name);
  const env = envArg(subject);
  const what = subject.job ? "Job" : "Workload";
  const verdict = subject.state ? oneLine(subject.state, 60) : "";
  const state = subject.job ? `: its latest run ${verdict || "needs attention"}` : verdict ? ` is ${verdict}` : " needs attention";
  const release = subject.release ? `\nIt runs release ${oneLine(subject.release, 80)}.` : "";
  const read = subject.job
    ? `${cmd(ctx, "workloads", env, "--service", name, "--jobs", "all")} lists its runs and why the last one failed`
    : `${cmd(ctx, "workloads", env, "--service", name)} says why (restarts, the last termination reason, the latest warning)`;
  return `${what} ${name}${where(subject, ctx)}${state}.${release}\n\n${FETCH}: ${read}, and ${cmd(ctx, "logs", env, shellWord(`service:${subject.name}`), "--since", "1h")} has its recent logs.`;
}

function releasePrompt(subject: Extract<AskSubject, { kind: "release" }>, ctx: AskContext): string {
  const version = shellWord(subject.version);
  const env = envArg(subject);
  const verdict = subject.verdict === "fail" ? " Its health check says it made things worse." : subject.verdict === "inconclusive" ? " Its health check could not prove it good." : "";
  return (
    `Release ${version}${where(subject, ctx)}.${verdict}\n\n` +
    `${FETCH}: ${cmd(ctx, "check", env, "--release", version, "--json")} compares the window after it with the window before, and ` +
    `${cmd(ctx, "changes", env, "--since", "24h")} lists what else changed around it.`
  );
}

function deployFailedPrompt(subject: Extract<AskSubject, { kind: "deploy-failed" }>, ctx: AskContext): string {
  const env = envArg(subject);
  const run = subject.runId ? ` (deploy run ${shellWord(subject.runId)})` : "";
  const version = subject.version ? `\nIt was deploying ${oneLine(subject.version, 80)}.` : "";
  return (
    `A deploy${run} failed${where(subject, ctx)}.${version}\n\n` +
    `${FETCH}: ${forgeCmd(ctx, "env", "status", env)} shows the bound release, the rollout and its gates, and ` +
    `${cmd(ctx, "changes", env, "--since", "24h")} lists the failed deploy with what changed before it.`
  );
}

function sessionPrompt(subject: Extract<AskSubject, { kind: "session" }>, ctx: AskContext): string {
  const id = shellWord(subject.sessionId);
  const env = envArg(subject);
  return `Browser session ${id}${where(subject, ctx)} hit errors.\n\n${FETCH}, starting with ${cmd(ctx, "session", id, env)}: its failed requests and errors carry trace ids for ${cmd(ctx, "trace", "<id>", env)}.`;
}

function deployPrompt(subject: Extract<AskSubject, { kind: "deploy" }>, ctx: AskContext): string {
  const env = envArg(subject);
  const project = ctx.forgeProject ? ` (forge project ${oneLine(ctx.forgeProject, 80)})` : "";
  const target = subject.env
    ? `environment ${oneLine(subject.env, 80)}`
    : `an environment${ctx.envChoices?.length ? ` (one of ${ctx.envChoices.map((name) => oneLine(name, 80)).join(", ")})` : ""}; ask me which before deploying`;
  return (
    `Deploy this app${project} to ${target}.\n\n` +
    `From the project's checkout, run ${forgeCmd(ctx, "env", "deploy", env)}. Then prove it is good with ${forgeCmd(ctx, "obs", "check", env, "--wait", "--json")} and tell me what it says. ` +
    `If the environment is not set up yet, or the deploy is queued on billing, say what it needs instead of working around it.`
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
export function metricsQueryCommand(panel: Panel, env: string, since: string, ctx: AskContext = {}): string | null {
  const [series] = panel.series;
  if (panel.signal !== "metrics" || panel.series.length !== 1 || !series.field || panel.formula || panel.ratio) return null;
  if (!["line", "number", "table", "bar"].includes(panel.kind)) return null;
  const agg = cliAgg(series.agg, series.quantile);
  const filters = cliPredicates("--filter", panel.where ?? []);
  const seriesFilters = cliPredicates("--where", series.where ?? [], "A:");
  if (!agg || !filters || !seriesFilters || (panel.groupBy ?? []).length > 3) return null;
  const groups = (panel.groupBy ?? []).flatMap((key) => ["--group-by", shellWord(key)]);
  return cmd(ctx, "metrics", "query", env, shellWord(series.field), "--agg", agg, ...groups, ...filters, ...seriesFilters, "--since", since);
}

function panelPrompt(subject: Extract<AskSubject, { kind: "panel" }>, ctx: AskContext): string {
  const { panel, window } = subject;
  const since = SINCE[window];
  const env = envArg(subject);
  const dashboard = subject.dashboard ? ` of dashboard "${oneLine(subject.dashboard)}"` : "";
  const head = `Dashboard panel "${oneLine(panel.title || panel.id)}"${dashboard}${where(subject, ctx)}, last ${window}.`;
  const spec = JSON.stringify(panelQuery(panel));
  const query = spec.length <= MAX_PANEL_SPEC_CHARS ? `Query: ${spec}` : `Query: too long to quote; read panel ${shellWord(panel.id)} from the dashboard.`;

  const exact = subject.env ? metricsQueryCommand(panel, env, since, ctx) : null;
  const raw = RAW_VERB[panel.signal];
  const rawRead = raw ? `, or read the raw signal with ${cmd(ctx, ...raw, env, "--since", since)}` : "";
  const run = exact
    ? `${exact} runs this panel's query`
    : `save the Query above as panel.json and run ${cmd(ctx, "query", env, "--panel", "panel.json", "--since", since)}${rawRead}`;
  return `${head}\n${query}\n\n${FETCH}: ${run}.`;
}

/**
 * The structured `obs` subcommand that reads a signal as it is, without the
 * panel's shaping, as its words. A signal with none (profiles) gets no raw
 * read: `obs query` needs --sql, --sql-file or --panel, and `obs profile` reads
 * only a local ClickStack.
 */
const RAW_VERB: Record<string, readonly string[]> = { traces: ["traces"], logs: ["logs"], metrics: ["metrics", "list"] };

const INVESTIGATE = "Find the root cause and explain it, with the evidence for it. Don't change any code yet: say what you would change, and why.";

const FIX =
  "Then fix it. This chat has a workspace of its own on its own git branch, so the main checkout is untouched. " +
  "Find the root cause, make the smallest correct change, add a test that fails without it, and run the tests that cover it. " +
  "Finish with the root cause, the change, and how you verified it.";

/** FIX for the reader's own agent: it has no workspace of its own to claim, so it is told to branch. */
const FIX_EXTERNAL =
  "Then fix it on a new branch: find the root cause, make the smallest correct change, add a test that fails without it, " +
  "run the tests that cover it, and report the root cause, the change and how you verified it.";

const FIX_ALERT = " If the rule is what is wrong (a threshold or expression that doesn't match how the service really behaves), fix the rule where the project declares it, in its KCL, rather than the service.";

/** The instruction an intent adds, after the subject and how to read it. */
function intentLine(subject: AskSubject, ctx: AskContext): string {
  if (ctx.intent === "investigate") return `\n\n${INVESTIGATE}`;
  if (ctx.intent === "fix") return `\n\n${ctx.host === "external" ? FIX_EXTERNAL : FIX}${subject.kind === "alert" ? FIX_ALERT : ""}`;
  return "";
}

export function buildAskReliantPrompt(subject: AskSubject, ctx: AskContext = {}): string {
  return subjectPrompt(subject, ctx) + intentLine(subject, ctx);
}

function subjectPrompt(subject: AskSubject, ctx: AskContext): string {
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
    case "workload":
      return workloadPrompt(subject, ctx);
    case "release":
      return releasePrompt(subject, ctx);
    case "deploy-failed":
      return deployFailedPrompt(subject, ctx);
    case "session":
      return sessionPrompt(subject, ctx);
    case "deploy":
      return deployPrompt(subject, ctx);
  }
}

// ── The handoff sheet's commands ─────────────────────────────────────────────

/** One command the handoff sheet offers to copy, as the reader's shell runs it. */
export interface AskAgentCommand {
  /** The CLI verb, for analytics (`obs issue`, `env status`). */
  verb: string;
  command: string;
}

/** A forge subcommand path, one word per level (`["obs", "alerts", "check"]`), as `forge <path> --help` names it. */
type CliPath = readonly [string, ...string[]];

const plain = (ctx: AskContext, path: CliPath, ...args: string[]): AskAgentCommand => ({
  verb: path.join(" "),
  command: [cliOf(ctx), ...path, ...args].join(" "),
});

/**
 * The commands that read a subject, for the reader to run themselves (the
 * handoff sheet's "Or run it yourself"): the same verbs, spelled the same way,
 * as the prompt quotes. `obs profile` is never offered: it reads only a local
 * ClickStack (`forge env up`), not a hosted environment.
 */
export function askAgentCommands(subject: AskSubject, ctx: AskContext = {}): AskAgentCommand[] {
  const env = envArg(subject);
  switch (subject.kind) {
    case "issue": {
      const id = shellWord(subject.issueId);
      const trace = subject.latestTraceId ? shellWord(subject.latestTraceId) : null;
      return [
        plain(ctx, ["obs", "issue"], id, env),
        ...(trace ? [plain(ctx, ["obs", "trace"], trace, env), plain(ctx, ["obs", "logs"], env, "--trace", trace)] : []),
      ];
    }
    case "trace": {
      const id = shellWord(subject.traceId);
      return [plain(ctx, ["obs", "trace"], id, env), plain(ctx, ["obs", "logs"], env, "--trace", id)];
    }
    case "alert":
      return [
        plain(ctx, ["obs", "status"], env, "--json", "--fields", "alerts"),
        plain(ctx, ["obs", "alerts", "check"], "--env", env, "--rule", shellWord(subject.rule), "--sql"),
      ];
    case "workload": {
      const name = shellWord(subject.name);
      return [
        subject.job ? plain(ctx, ["obs", "workloads"], env, "--service", name, "--jobs", "all") : plain(ctx, ["obs", "workloads"], env, "--service", name),
        plain(ctx, ["obs", "logs"], env, shellWord(`service:${subject.name}`), "--since", "1h"),
      ];
    }
    case "release":
      return [plain(ctx, ["obs", "check"], env, "--release", shellWord(subject.version), "--json"), plain(ctx, ["obs", "changes"], env, "--since", "24h")];
    case "deploy-failed":
      return [plain(ctx, ["env", "status"], env), plain(ctx, ["obs", "changes"], env, "--since", "24h")];
    case "session":
      return [plain(ctx, ["obs", "session"], shellWord(subject.sessionId), env)];
    case "deploy":
      return [plain(ctx, ["env", "deploy"], env), plain(ctx, ["obs", "check"], env, "--wait", "--json")];
    case "panel": {
      const since = SINCE[subject.window];
      const exact = subject.env ? metricsQueryCommand(subject.panel, env, since, ctx) : null;
      if (exact) return [{ verb: "obs metrics query", command: exact.slice(1, -1) }];
      // No raw read for this signal: the prompt's panel.json is the way in, and there is no file to name here.
      const raw = RAW_VERB[subject.panel.signal];
      return raw ? [plain(ctx, ["obs", ...raw], env, "--since", since)] : [];
    }
    case "graph":
      return [];
  }
}

/**
 * Gives the reader's agent forge's playbook: the skill that teaches the obs
 * verbs, and forge's MCP server (introspection, skills, lint, generate; `obs`
 * itself runs in the agent's shell).
 */
export function askAgentPlaybook(cli: AskCli = "reliant forge"): string[] {
  return [`${cli} skill load observability/query`, `claude mcp add forge -- ${cli} mcp`, `codex mcp add forge -- ${cli} mcp`];
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

/** What the workspace name says the chat is about, before it is slugged. */
function workspaceSubject(subject: AskSubject): string {
  switch (subject.kind) {
    case "graph":
      return subject.rpcId || subject.nodeId || subject.edgeId || "graph";
    case "issue":
      return subject.issueId;
    case "trace":
      return subject.traceId.slice(0, 12);
    case "alert":
      return subject.rule;
    case "panel":
      return subject.panel.title || subject.panel.id;
    case "workload":
      return subject.name;
    case "release":
      return subject.version;
    case "session":
      return subject.sessionId.slice(0, 12);
    case "deploy":
    case "deploy-failed":
      return subject.env ?? "";
  }
}

/** A workspace name for the chat: ask-<kind>-<what>-<nonce> (fix-… for a fix), a legal git branch. */
export function askWorkspaceName(subject: AskSubject, nonce: string, intent?: AskIntent): string {
  const slug = workspaceSubject(subject)
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 40)
    .replace(/-+$/g, "");
  return [intent === "fix" ? "fix" : "ask", subject.kind, slug, nonce].filter(Boolean).join("-");
}
