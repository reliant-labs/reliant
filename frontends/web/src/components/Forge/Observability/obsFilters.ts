// Copyright (c) 2025 Reliant Labs

/**
 * Attribute filters and group-by, as they live in the URL.
 *
 * ── ONE TOKEN PER CONDITION ──────────────────────────────────────────────────
 *
 * A filter is a short string a person can read in an address bar and edit by
 * hand, not a JSON object:
 *
 *   service.version:v1.8.2        is
 *   status!:error                 is not
 *   http.route~orders             contains (case-insensitive)
 *   k8s.container.name:*          exists
 *
 * The key is a dotted identifier (never contains `:` `!` `~`), so the first of
 * those characters ends it and EVERYTHING after the operator is the value —
 * colons, spaces and all. There is nothing to escape.
 *
 * Two `is` tokens on one key mean EITHER: `service.version:v1` and
 * `service.version:v2` become one `in [v1, v2]` predicate. That is how a
 * reader compares two versions — pick a second value — without an operator
 * of its own in the grammar.
 */

import type { AttributeFilter } from "@/services/controlPlane/observability/types";

import { isAttributeKeyShaped, MAX_FILTERS, MAX_GROUP_BY, MAX_IN_VALUES } from "./attributes";

/** What a reader can pick per token. `in` is not one: it is two `eq`s on one key. */
export type FilterTokenOp = "eq" | "neq" | "contains" | "exists";

export interface FilterToken {
  key: string;
  op: FilterTokenOp;
  /** Empty for exists. */
  value: string;
}

export const FILTER_OP_LABELS: Record<FilterTokenOp, string> = {
  eq: "is",
  neq: "is not",
  contains: "contains",
  exists: "exists",
};

const TOKEN = /^([A-Za-z0-9_][A-Za-z0-9_.\-/]{0,127})(!:|:|~)(.*)$/s;

/** A token from the URL, or null for anything that is not one (a hand-edited URL narrows, never breaks). */
export function parseFilterToken(token: string): FilterToken | null {
  const match = TOKEN.exec(token);
  if (!match) return null;
  const [, key, sign, value] = match;
  if (sign === ":" && value === "*") return { key, op: "exists", value: "" };
  if (value === "" || value.length > 1024) return null;
  return { key, op: sign === "!:" ? "neq" : sign === "~" ? "contains" : "eq", value };
}

export function formatFilterToken(filter: FilterToken): string {
  switch (filter.op) {
    case "exists":
      return `${filter.key}:*`;
    case "neq":
      return `${filter.key}!:${filter.value}`;
    case "contains":
      return `${filter.key}~${filter.value}`;
    default:
      return `${filter.key}:${filter.value}`;
  }
}

/** Every valid token, in URL order, without duplicates. */
export function filterTokensOf(param: readonly string[] | undefined): FilterToken[] {
  const seen = new Set<string>();
  const out: FilterToken[] = [];
  for (const raw of param ?? []) {
    const token = parseFilterToken(raw);
    if (!token) continue;
    const canonical = formatFilterToken(token);
    if (seen.has(canonical)) continue;
    seen.add(canonical);
    out.push(token);
  }
  return out;
}

/** The URL value for a token list: absent when empty, so a clean view has a clean link. */
export function filterParam(tokens: readonly FilterToken[]): string[] | undefined {
  return tokens.length ? tokens.map(formatFilterToken) : undefined;
}

/**
 * The tokens as QueryScope.filters: `is` tokens on one key merge into one
 * `in`, everything else passes through, and the list is capped at the
 * server's limits rather than sent to be refused.
 */
export function scopeFiltersOf(tokens: readonly FilterToken[]): AttributeFilter[] {
  const eqByKey = new Map<string, string[]>();
  const out: (AttributeFilter | { mergeKey: string })[] = [];
  for (const token of tokens) {
    if (token.op === "eq") {
      const values = eqByKey.get(token.key);
      if (values) {
        if (values.length < MAX_IN_VALUES && !values.includes(token.value)) values.push(token.value);
        continue;
      }
      eqByKey.set(token.key, [token.value]);
      out.push({ mergeKey: token.key });
      continue;
    }
    out.push(token.op === "exists" ? { key: token.key, op: "exists" } : { key: token.key, op: token.op, value: token.value });
  }
  return out
    .map((entry): AttributeFilter => {
      if (!("mergeKey" in entry)) return entry;
      const values = eqByKey.get(entry.mergeKey) ?? [];
      return values.length === 1
        ? { key: entry.mergeKey, op: "eq", value: values[0] }
        : { key: entry.mergeKey, op: "in", values };
    })
    .slice(0, MAX_FILTERS);
}

/** `groupBy` from the URL: shaped keys, no repeats, at most three. */
export function groupByOf(param: readonly string[] | undefined): string[] {
  const out: string[] = [];
  for (const key of param ?? []) {
    if (!isAttributeKeyShaped(key) || out.includes(key)) continue;
    out.push(key);
    if (out.length === MAX_GROUP_BY) break;
  }
  return out;
}

export function groupByParam(keys: readonly string[]): string[] | undefined {
  return keys.length ? [...keys] : undefined;
}

/** "api · prod" — a group's values in group-by order, for a label. */
export function groupLabel(group: Record<string, string>, keys: readonly string[]): string {
  const ordered = keys.length ? keys.map((key) => group[key] ?? "") : Object.values(group);
  return ordered.map((value) => value || "(none)").join(" · ");
}
