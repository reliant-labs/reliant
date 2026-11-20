// Copyright (c) 2026 Reliant Labs

/**
 * WHICH NAV VARIANT IS SHOWING (U6c, prototype): `?nav=a|b|c`, default b.
 *
 * The URL wins. A tab remembers the last one it was given (sessionStorage),
 * and the root route keeps `nav` on every navigation that does not name it
 * (retainNavParam, the same mechanism as `?tour=`), so a variant survives
 * moving around, a refresh and a shared link.
 */

import { useEffect, useMemo } from "react";

import type { NavVariant } from "./types";

export const NAV_VARIANTS: readonly NavVariant[] = ["a", "b", "c"];
export const DEFAULT_NAV_VARIANT: NavVariant = "b";
export const NAV_PARAM = "nav";
const STORAGE_KEY = "shell.navVariant";

export function parseNavVariant(value: unknown): NavVariant | null {
  return typeof value === "string" && (NAV_VARIANTS as readonly string[]).includes(value) ? (value as NavVariant) : null;
}

export function storedNavVariant(): NavVariant | null {
  try {
    return parseNavVariant(sessionStorage.getItem(STORAGE_KEY));
  } catch {
    return null;
  }
}

export function rememberNavVariant(variant: NavVariant): void {
  try {
    sessionStorage.setItem(STORAGE_KEY, variant);
  } catch {
    /* storage disabled: the URL still carries it */
  }
}

/** The variant for this search: the URL's, else the tab's last, else the default. `explicit`: one was chosen. */
export function resolveNavVariant(search: Record<string, unknown>): { variant: NavVariant; explicit: boolean } {
  const chosen = parseNavVariant(search[NAV_PARAM]) ?? storedNavVariant();
  return { variant: chosen ?? DEFAULT_NAV_VARIANT, explicit: chosen !== null };
}

/** The variant on screen; a variant in the URL becomes the tab's. */
export function useNavVariant(search: Record<string, unknown>): { variant: NavVariant; explicit: boolean } {
  const fromUrl = parseNavVariant(search[NAV_PARAM]);
  useEffect(() => {
    if (fromUrl) rememberNavVariant(fromUrl);
  }, [fromUrl]);
  // Read storage only when the URL changes, not on every render.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  return useMemo(() => resolveNavVariant(search), [fromUrl]);
}

/** `href` with `nav=<variant>` in its query (kept for a modified click: a new tab has no session yet). */
export function withNavParam(href: string, variant: NavVariant): string {
  const hashAt = href.indexOf("#");
  const hash = hashAt === -1 ? "" : href.slice(hashAt);
  const beforeHash = hashAt === -1 ? href : href.slice(0, hashAt);
  const queryAt = beforeHash.indexOf("?");
  const path = queryAt === -1 ? beforeHash : beforeHash.slice(0, queryAt);
  const params = new URLSearchParams(queryAt === -1 ? "" : beforeHash.slice(queryAt + 1));
  params.set(NAV_PARAM, variant);
  return `${path}?${params.toString()}${hash}`;
}

/**
 * Root-route search middleware: keep `?nav=` across navigations that do not
 * name it — a Link, a `navigate({ to })` or `navigate({ href })` with its own
 * search would otherwise drop the variant the owner is trying.
 */
export function retainNavParam({
  search,
  next,
}: {
  search: Record<string, unknown>;
  next: (search: Record<string, unknown>) => Record<string, unknown>;
}): Record<string, unknown> {
  const result = next(search);
  if (NAV_PARAM in result) return result;
  const kept = parseNavVariant(search[NAV_PARAM]) ?? storedNavVariant();
  return kept ? { ...result, [NAV_PARAM]: kept } : result;
}
