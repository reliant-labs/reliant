// Copyright (c) 2025 Reliant Labs

import { defaultParseSearch, defaultStringifySearch } from "@tanstack/react-router";

/**
 * `path` with `search` as its query string, in the router's own encoding
 * (arrays and objects as JSON), so a built href parses back to the search
 * object it came from. Undefined and empty values are dropped.
 */
export function withSearch(path: string, search: Record<string, unknown> = {}): string {
  const kept = Object.entries(search).filter(([, value]) => value !== undefined && value !== null && value !== "");
  // Every console link builds its href on every render, and the router's
  // encoder JSON.parses each string value to decide whether to quote it (a
  // thrown SyntaxError per plain string: about a second of a phone's main
  // thread per tour). The answer is a pure function of the entries, so it is
  // remembered by them.
  let key = path;
  for (const [name, value] of kept) key += `\u0000${name}\u0001${typeof value}\u0001${typeof value === "object" ? JSON.stringify(value) : String(value)}`;
  const known = hrefs.get(key);
  if (known !== undefined) return known;
  const href = `${path}${defaultStringifySearch(Object.fromEntries(kept))}`;
  if (hrefs.size >= HREF_LIMIT) hrefs.clear();
  hrefs.set(key, href);
  return href;
}

/**
 * `href` (a path, maybe with a query) with `search` merged into its query, in
 * the router's encoding: what withSearch does for a path that already has one.
 */
export function withMoreSearch(href: string, search: Record<string, unknown>): string {
  const at = href.indexOf("?");
  if (at < 0) return withSearch(href, search);
  return withSearch(href.slice(0, at), { ...defaultParseSearch(href.slice(at)), ...search });
}

/** withSearch's answers, by entries. A console visit builds a few hundred distinct hrefs. */
const hrefs = new Map<string, string>();
const HREF_LIMIT = 2_000;
