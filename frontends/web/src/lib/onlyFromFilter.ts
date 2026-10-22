// Copyright (c) 2025 Reliant Labs

/**
 * "Only from": who may start a run, as the one filter clause a trigger's
 * filter already supports (internal/triggers/filter.go):
 *
 *   trigger.sender.verified && trigger.sender.id in ["U123", "U456"]
 *
 * The control writes that clause, AND-ed after whatever filter is already
 * there as `(<rest>) && <clause>`, and reads it back only when the filter has
 * exactly that shape. Anything else that mentions `trigger.sender` is a
 * filter someone wrote by hand; the control reports it as custom and leaves
 * it alone rather than guess at it.
 *
 * `verified` is part of the clause on purpose: an id the source could not
 * vouch for (an email whose DMARC failed) is a claim, and an allowlist of
 * claims admits whoever claims to be on it.
 *
 * The list holds ids, never names: on GitHub the numeric user id, which the
 * control shows by login (hooks/useSenderNames).
 */

/** Integrations whose senders a source can verify, so an allowlist means something. */
export const ONLY_FROM_INTEGRATIONS = ["slack", "github", "gmail"] as const;

export type OnlyFromIntegration = (typeof ONLY_FROM_INTEGRATIONS)[number];

export function isOnlyFromIntegration(integration: string | undefined): integration is OnlyFromIntegration {
  return (ONLY_FROM_INTEGRATIONS as readonly string[]).includes(integration ?? "");
}

/**
 * An allowlist entry in the form trigger.sender.id carries it: email addresses
 * are case-insensitive and arrive lowercased; a Slack user id and a GitHub
 * user id are kept exactly.
 */
export function normalizeSenderId(integration: string | undefined, raw: string): string {
  const id = raw.trim();
  return integration === "gmail" ? id.toLowerCase() : id;
}

/**
 * Whether id is a GitHub user id, the only thing trigger.sender.id carries on
 * GitHub: the number GitHub assigns once and never reuses. A login is not one
 * — it can be renamed, and the old one registered by someone else — so a
 * login in a GitHub list (written before senders were ids) matches nobody.
 */
export function isGitHubUserId(id: string): boolean {
  return /^[1-9][0-9]*$/.test(id);
}

/** A GitHub login as typed: trimmed, without a leading "@". */
export function gitHubLogin(raw: string): string {
  return raw.trim().replace(/^@/, "");
}

export type OnlyFromState =
  /** No sender clause: `rest` is the whole filter. */
  | { kind: "none"; rest: string }
  /** The control's clause, after `rest` (empty when the clause is all of it). */
  | { kind: "list"; ids: string[]; rest: string }
  /** The filter decides on the sender some other way; the control keeps out of it. */
  | { kind: "custom" };

const CLAUSE_PREFIX = "trigger.sender.verified && trigger.sender.id in [";
const CEL_STRING = String.raw`"(?:[^"\\]|\\.)*"`;
const LIST_BODY = new RegExp(String.raw`^\s*(?:${CEL_STRING}(?:\s*,\s*${CEL_STRING})*)?\s*$`);
const STRING_TOKEN = new RegExp(CEL_STRING, "g");

/** The clause for ids, as the control writes it. */
export function onlyFromClause(ids: readonly string[]): string {
  return `${CLAUSE_PREFIX}${ids.map((id) => JSON.stringify(id)).join(", ")}]`;
}

/** What the control makes of a filter. */
export function parseOnlyFrom(filter: string | undefined): OnlyFromState {
  const expr = (filter ?? "").trim();
  const mentionsSender = expr.includes("trigger.sender");
  const listed = parseClause(expr);
  if (listed) return listed;
  return mentionsSender ? { kind: "custom" } : { kind: "none", rest: expr };
}

/**
 * The filter with its Only-from list set to ids: the rest of the filter is
 * kept, an empty list removes the clause. null for a custom filter, which the
 * control does not rewrite.
 */
export function withOnlyFrom(filter: string | undefined, ids: readonly string[]): string | null {
  const state = parseOnlyFrom(filter);
  if (state.kind === "custom") return null;
  const unique = [...new Set(ids.filter((id) => id !== ""))];
  if (unique.length === 0) return state.rest;
  const clause = onlyFromClause(unique);
  return state.rest ? `(${state.rest}) && ${clause}` : clause;
}

function parseClause(expr: string): OnlyFromState | null {
  const at = expr.lastIndexOf(CLAUSE_PREFIX);
  if (at < 0 || !expr.endsWith("]")) return null;
  const body = expr.slice(at + CLAUSE_PREFIX.length, -1);
  if (!LIST_BODY.test(body)) return null;
  const ids = (body.match(STRING_TOKEN) ?? []).map((token) => JSON.parse(token) as string);

  const head = expr.slice(0, at);
  if (head === "") return { kind: "list", ids, rest: "" };
  // The only other shape written is "(<rest>) && <clause>", with the parens
  // around ALL of rest: "(a) || (b) && <clause>" is not it.
  if (!head.startsWith("(") || !head.endsWith(") && ")) return null;
  const rest = head.slice(1, -") && ".length);
  if (!wrapsWhole(rest) || rest.includes("trigger.sender")) return null;
  return { kind: "list", ids, rest: rest.trim() };
}

/** Whether "(" + rest + ")" is one group: rest's parens balance and never close early. */
function wrapsWhole(rest: string): boolean {
  let depth = 0;
  let quote: string | null = null;
  for (let i = 0; i < rest.length; i++) {
    const ch = rest[i]!;
    if (quote) {
      if (ch === "\\") i++;
      else if (ch === quote) quote = null;
      continue;
    }
    if (ch === '"' || ch === "'") quote = ch;
    else if (ch === "(") depth++;
    else if (ch === ")" && --depth < 0) return false;
  }
  return depth === 0 && quote === null;
}
