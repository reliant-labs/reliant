// Copyright (c) 2025 Reliant Labs

/**
 * "Open as SQL panel": one statement a metrics panel compiles to (QueryPanel
 * with `explain`), rewritten as the statement of a `kind: "sql"` panel — the
 * starting point for raw SQL (DESIGN §3.6, D2 condition 1).
 *
 * A compiled statement binds every value as a positional parameter
 * (`{p3:Int64}`); a SQL panel binds only the scope's own parameters (dashspec
 * sql.go: `{from}`, `{to}`, `{step}`, `{env_ids}`, `{envs}`, `{services}`), so
 * the rest are written in as literals and the window becomes the panel's:
 *
 *   fromUnixTimestamp64Nano({p:Int64})  →  {from:DateTime64(9)} / {to:DateTime64(9)},
 *                                          or an offset from the nearer one (a lookback)
 *   toDateTime({p:UInt32})              →  the same, in seconds
 *   toIntervalSecond({p:UInt32})        →  toIntervalSecond({step:UInt32}) when it is the step
 *   {p:Array(String)} = the scope's envs →  {env_ids:Array(String)}
 *   anything else                       →  its literal
 *
 * The org's database qualifier is dropped from the telemetry tables: a SQL
 * panel runs as the org's own read-only user, whose default database they are.
 */

import type { CompiledStatement, TimeWindow } from "@/services/controlPlane/observability/types";

/** The parameters a SQL panel's statement binds from the scope (forge dashspec sql.go). */
export const SCOPE_PARAMS = ["from", "to", "step", "env_ids", "envs", "services"] as const;

const PARAM = /\{([A-Za-z_][A-Za-z0-9_]*):([^{}]+)\}/g;

export interface SqlPanelContext {
  /** The window the statement was compiled for. */
  window: TimeWindow;
  /** The step it was compiled with (PanelResult.stepSeconds); 0 when unknown. */
  stepSeconds: number;
  /** The environment ids in scope (empty: every environment). */
  envIds: readonly string[];
}

/** A ClickHouse string literal. */
export function stringLiteral(value: string): string {
  return `'${value.replace(/\\/g, "\\\\").replace(/'/g, "\\'")}'`;
}

const NUMERIC_TYPE = /^(U?Int(8|16|32|64|128|256)|Float(32|64))$/;
const NUMBER = /^-?(\d+(\.\d+)?([eE][+-]?\d+)?|inf|nan)$/;

/** A parameter's value as a literal of its type. */
function literalOf(type: string, value: string): string {
  if (NUMERIC_TYPE.test(type) && NUMBER.test(value)) return value;
  if (type === "String") return stringLiteral(value);
  // obsquery binds arrays already spelled as ClickHouse literals (`['a','b']`).
  if (type.startsWith("Array(") && value.startsWith("[")) return value;
  return `CAST(${stringLiteral(value)}, ${stringLiteral(type)})`;
}

/** `{from:…}` or `{to:…}` itself, or an offset from the nearer of them. */
function relativeTime(valueSec: number, window: TimeWindow, from: string, to: string): string {
  const startSec = window.startMs / 1000;
  const endSec = window.endMs / 1000;
  if (Math.abs(valueSec - startSec) < 0.0005) return from;
  if (Math.abs(valueSec - endSec) < 0.0005) return to;
  const nearStart = Math.abs(valueSec - startSec) <= Math.abs(valueSec - endSec);
  const anchor = nearStart ? from : to;
  const delta = valueSec - (nearStart ? startSec : endSec);
  const seconds = Math.round(Math.abs(delta) * 1000) / 1000;
  const interval = Number.isInteger(seconds) ? `toIntervalSecond(${seconds})` : `toIntervalMillisecond(${Math.round(Math.abs(delta) * 1000)})`;
  return `(${anchor} ${delta < 0 ? "-" : "+"} ${interval})`;
}

function sameSet(a: readonly string[], b: readonly string[]): boolean {
  if (a.length !== b.length) return false;
  const set = new Set(a);
  return b.every((value) => set.has(value));
}

/** The values of an `Array(String)` literal (`['a','b']`), or null when it is not one. */
function stringsOfArray(literal: string): string[] | null {
  const inner = literal.trim();
  if (!inner.startsWith("[") || !inner.endsWith("]")) return null;
  const out: string[] = [];
  const re = /'((?:[^'\\]|\\.)*)'/g;
  let match: RegExpExecArray | null;
  while ((match = re.exec(inner))) out.push(match[1].replace(/\\(.)/g, "$1"));
  return out;
}

/** The statement of a SQL panel equivalent to `statement` over the scope's window. */
export function sqlPanelStatement(statement: CompiledStatement, ctx: SqlPanelContext): string {
  const params = statement.params;
  const value = (name: string): string | undefined => (Object.prototype.hasOwnProperty.call(params, name) ? params[name] : undefined);
  let sql = statement.sql;

  // The window: nanosecond and second timestamps, as the scope's from/to.
  sql = sql.replace(/fromUnixTimestamp64Nano\(\{([A-Za-z0-9_]+):Int64\}\)/g, (whole, name: string) => {
    const raw = value(name);
    if (raw === undefined || !/^-?\d+$/.test(raw)) return whole;
    return relativeTime(Number(BigInt(raw) / 1000n) / 1_000_000, ctx.window, "{from:DateTime64(9)}", "{to:DateTime64(9)}");
  });
  sql = sql.replace(/toDateTime\(\{([A-Za-z0-9_]+):UInt32\}\)/g, (whole, name: string) => {
    const raw = value(name);
    if (raw === undefined || !/^\d+$/.test(raw)) return whole;
    return relativeTime(Number(raw), ctx.window, "toDateTime({from:DateTime64(9)})", "toDateTime({to:DateTime64(9)})");
  });
  // The step, when it is the one the panel ran at.
  if (ctx.stepSeconds > 0) {
    sql = sql.replace(/toIntervalSecond\(\{([A-Za-z0-9_]+):(?:UInt32|Int64)\}\)/g, (whole, name: string) =>
      value(name) === String(ctx.stepSeconds) ? "toIntervalSecond({step:UInt32})" : whole
    );
  }
  // Everything else: the scope's environments stay bound, the rest becomes literals.
  return sql
    .replace(PARAM, (whole, name: string, type: string) => {
      if ((SCOPE_PARAMS as readonly string[]).includes(name)) return whole;
      const raw = value(name);
      if (raw === undefined) return whole;
      if (type === "Array(String)" && ctx.envIds.length > 0) {
        const values = stringsOfArray(raw);
        if (values && sameSet(values, ctx.envIds)) return "{env_ids:Array(String)}";
      }
      return literalOf(type.trim(), raw);
    })
    .replace(/\b[A-Za-z_][A-Za-z0-9_]*\.(otel_[A-Za-z0-9_]+)\b/g, "$1");
}

/** The positional parameters a statement still carries after the rewrite: what the SQL panel could not bind. */
export function unboundParams(sql: string): string[] {
  const out = new Set<string>();
  for (const match of sql.matchAll(PARAM)) if (!(SCOPE_PARAMS as readonly string[]).includes(match[1])) out.add(match[1]);
  return [...out];
}
