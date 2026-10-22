// Copyright (c) 2025 Reliant Labs

/**
 * What an alert rule's `expr` may be, checked before anything is shown.
 *
 * Two rule sets apply, and the second is stricter than the first:
 *
 *   - forge `pkg/alerting.ValidateExpr`, which the control plane runs again
 *     before every evaluation: one read-only SELECT, no writes, DDL, SETTINGS,
 *     FORMAT, system tables or outside table functions, at most 16 KiB. It is a
 *     lexical check, ported here token for token.
 *   - the `forge.AlertRule` KCL schema: `expr` must start with SELECT or WITH,
 *     and must contain no `;` ANYWHERE, string literals included.
 *
 * A dashboard SQL panel is held to the first set already (the same engine and
 * limits as alert rules); the second only bites once the SQL is declared in KCL.
 */

/** forge alerting.MaxExprBytes. */
export const MAX_EXPR_BYTES = 16 << 10;

/** The placeholder the control plane binds to the id of the env a rule is declared for. */
export const ENV_ID_PARAM = "{reliant_env_id:String}";

const DENIED_WORDS = new Map<string, string>([
  ["insert", "writes are not allowed"],
  ["update", "writes are not allowed"],
  ["delete", "writes are not allowed"],
  ["drop", "DDL is not allowed"],
  ["alter", "DDL is not allowed"],
  ["create", "DDL is not allowed"],
  ["truncate", "DDL is not allowed"],
  ["attach", "DDL is not allowed"],
  ["detach", "DDL is not allowed"],
  ["rename", "DDL is not allowed"],
  ["optimize", "DDL is not allowed"],
  ["grant", "access control is not allowed"],
  ["revoke", "access control is not allowed"],
  ["kill", "KILL is not allowed"],
  ["system", "the system database is not readable"],
  ["set", "SET is not allowed"],
  ["use", "USE is not allowed"],
  ["execute", "EXECUTE AS is applied by the Querier, not the rule"],
  ["outfile", "INTO OUTFILE is not allowed"],
  ["settings", "SETTINGS are applied by the evaluator, not the rule"],
]);

const DENIED_FUNCTIONS = new Set([
  "url", "urlcluster", "file", "filecluster", "s3", "s3cluster", "remote", "remotesecure", "cluster", "clusterallreplicas",
  "hdfs", "hdfscluster", "mysql", "postgresql", "mongodb", "redis", "jdbc", "odbc", "sqlite", "executable", "input",
  "azureblobstorage", "azureblobstoragecluster", "gcs", "iceberg", "deltalake", "hudi",
]);

interface Token {
  kind: "word" | "semi" | "other";
  /** Lowercased. */
  text: string;
  /** A word immediately followed by `(`. */
  call: boolean;
}

const isWordStart = (c: string) => c === "_" || (c >= "a" && c <= "z") || (c >= "A" && c <= "Z") || c.charCodeAt(0) >= 0x80;
const isWordPart = (c: string) => isWordStart(c) || (c >= "0" && c <= "9");
const isSpace = (c: string) => c === " " || c === "\t" || c === "\n" || c === "\r" || c === "\f" || c === "\v";

/**
 * The end of the string literal, quoted identifier or comment starting at
 * `s[i]` (the index after it), or null when `s[i]` starts none. An unterminated
 * one reports `error`. A quote is closed by its own character; a backslash
 * escape and a doubled quote both work.
 */
function region(s: string, i: number): { end: number; error?: string } | null {
  const c = s[i];
  if (c === "-" && s[i + 1] === "-") {
    const nl = s.indexOf("\n", i);
    return { end: nl < 0 ? s.length : nl };
  }
  if (c === "#") {
    const nl = s.indexOf("\n", i);
    return { end: nl < 0 ? s.length : nl };
  }
  if (c === "/" && s[i + 1] === "*") {
    const close = s.indexOf("*/", i + 2);
    return close < 0 ? { end: s.length, error: "unterminated /* comment" } : { end: close + 2 };
  }
  if (c === "'" || c === '"' || c === "`") {
    for (let j = i + 1; j < s.length; j++) {
      if (s[j] === "\\") j++;
      else if (s[j] === c) {
        if (s[j + 1] === c) {
          j++;
          continue;
        }
        return { end: j + 1 };
      }
    }
    return { end: s.length, error: `unterminated ${c} quote` };
  }
  return null;
}

function nextIsParen(s: string, i: number): boolean {
  for (; i < s.length; i++) {
    if (!isSpace(s[i])) return s[i] === "(";
  }
  return false;
}

/** Splits SQL into words, semicolons and other, dropping comments and the contents of literals. */
function lex(s: string): { tokens: Token[] } | { error: string } {
  const tokens: Token[] = [];
  for (let i = 0; i < s.length; ) {
    const c = s[i];
    if (c === "\0") return { error: "NUL byte" };
    if (isSpace(c)) {
      i++;
      continue;
    }
    const skipped = region(s, i);
    if (skipped) {
      if (skipped.error) return { error: skipped.error };
      if (c === "'" || c === '"' || c === "`") tokens.push({ kind: "other", text: "", call: false });
      i = skipped.end;
      continue;
    }
    if (c === ";") {
      tokens.push({ kind: "semi", text: "", call: false });
      i++;
    } else if (isWordStart(c)) {
      let j = i;
      while (j < s.length && isWordPart(s[j])) j++;
      tokens.push({ kind: "word", text: s.slice(i, j).toLowerCase(), call: nextIsParen(s, j) });
      i = j;
    } else {
      tokens.push({ kind: "other", text: "", call: false });
      i++;
    }
  }
  return { tokens };
}

/**
 * Why the control plane would refuse this `expr` as an alert rule, in words, or
 * null when it accepts it. A port of forge `alerting.ValidateExpr` plus the
 * `forge.AlertRule` schema's own checks.
 */
export function alertExprProblem(expr: string): string | null {
  if (new TextEncoder().encode(expr).length > MAX_EXPR_BYTES) return `longer than ${MAX_EXPR_BYTES} bytes`;
  const lexed = lex(expr);
  if ("error" in lexed) return lexed.error;
  const { tokens } = lexed;
  if (tokens.length === 0) return "empty";
  const first = tokens[0];
  if (first.kind !== "word" || (first.text !== "select" && first.text !== "with")) return "must start with SELECT";
  let sawSelect = false;
  for (const [i, t] of tokens.entries()) {
    if (t.kind === "semi" && i !== tokens.length - 1) return "more than one statement";
    if (t.kind !== "word") continue;
    if (t.text === "select") sawSelect = true;
    const denied = DENIED_WORDS.get(t.text);
    if (denied) return `"${t.text}": ${denied}`;
    if (t.text === "format" && !t.call) return "FORMAT is applied by the driver, not the rule";
    if (t.call && DENIED_FUNCTIONS.has(t.text)) return `table function ${t.text}() reads outside the owner`;
  }
  if (!sawSelect) return "no SELECT";
  if (expr.includes(";")) {
    return "contains a ';'. forge.AlertRule refuses a semicolon anywhere in expr, a string literal included: write \\x3B inside a string, and drop a trailing ;";
  }
  if (!/^\s*(select|with)\b/i.test(expr)) return "must start with SELECT or WITH: forge.AlertRule refuses a leading comment";
  return null;
}

/** A ClickHouse single-quoted string literal. */
export function chString(value: string): string {
  return `'${value.replace(/[\\'\n\r\t\0]/g, (c) => ({ "\\": "\\\\", "'": "\\'", "\n": "\\n", "\r": "\\r", "\t": "\\t", "\0": "\\0" })[c] ?? c)}'`;
}

/** "5 MINUTE" for a window in seconds: the largest unit that divides it. */
export function intervalOf(seconds: number): string {
  if (seconds % 3600 === 0) return `${seconds / 3600} HOUR`;
  if (seconds % 60 === 0) return `${seconds / 60} MINUTE`;
  return `${seconds} SECOND`;
}

/** What a dashboard SQL panel's scope parameters become in an alert rule. */
export interface SqlBindings {
  windowSeconds: number;
  /** The env the rule is declared in, for {envs}. */
  envName: string;
  /** The services in scope; none is every service. */
  services: readonly string[];
}

const PARAM = /^\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*:\s*([^{}]+?)\s*\}/;

/**
 * A SQL panel's statement with the dashboard's scope parameters
 * ({from:DateTime64(9)}, {to:…}, {step:UInt32}, {env_ids:Array(String)},
 * {envs:…}, {services:…}) turned into what an alert rule can evaluate: a
 * window ending now, and the env the rule is declared for. The evaluator binds
 * only {reliant_env_id:String}, so any other parameter is returned as `unbound`.
 * String literals and comments are left alone.
 */
export function bindPanelSql(sql: string, binds: SqlBindings): { sql: string; unbound: string[] } {
  const windowStart = `(now64(9) - INTERVAL ${intervalOf(binds.windowSeconds)})`;
  const values: Record<string, string> = {
    from: windowStart,
    to: "now64(9)",
    step: `toUInt32(${binds.windowSeconds})`,
    env_ids: `[${ENV_ID_PARAM}]`,
    envs: `[${chString(binds.envName)}]`,
    services: binds.services.length ? `[${binds.services.map(chString).join(", ")}]` : "CAST([], 'Array(String)')",
  };
  const unbound: string[] = [];
  let out = "";
  for (let i = 0; i < sql.length; ) {
    const skipped = region(sql, i);
    if (skipped) {
      out += sql.slice(i, skipped.end);
      i = skipped.end;
      continue;
    }
    if (sql[i] === "{") {
      const m = PARAM.exec(sql.slice(i));
      if (m) {
        const [whole, name] = m;
        if (Object.hasOwn(values, name)) out += values[name];
        else {
          out += whole;
          if (name !== "reliant_env_id") unbound.push(whole);
        }
        i += whole.length;
        continue;
      }
    }
    out += sql[i];
    i++;
  }
  return { sql: out, unbound };
}
