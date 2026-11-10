// Copyright (c) 2025 Reliant Labs

/**
 * THE STORAGE BROWSER'S URL STATE, and the ListObjects request it means.
 *
 * Every view is a link (control-plane docs/storage-browser.md: "every view
 * state is in the URL"), so the page keeps nothing of its own that changes
 * what is shown. The keys, beside the console scope `env`:
 *
 *   bucket  the bucket: a BrowsableBucket name (the forge.Bucket's, never
 *           the cloud name)
 *   prefix  the level listed: "" / absent for the root, otherwise a common
 *           prefix ending in "/" (`avatars/2026/`). One that does not end in
 *           "/" is a typed narrowing: the folder up to its last "/", only the
 *           entries starting with the rest
 *   object  the open object's full key
 *   token   the listing page: the opaque next_page_token of the page before
 *           it (absent: the first page). It is sealed to the bucket and
 *           prefix, so changing either drops it
 *
 * Keys are arbitrary bytes to S3 and GCS: spaces, `%`, `#`, `?`, non-ASCII,
 * an empty segment (`a//b`), a leading `/`. Nothing here decodes or splits a
 * key except on "/", and the router's own JSON-aware search encoding carries
 * them through a link unchanged.
 *
 * Parsing never throws: a value that cannot be sent (too long, a bucket name
 * the server would refuse) is read as absent, so a hand-edited link opens a
 * wider view rather than an error page, as routeSchemas does for every
 * console key.
 */

import type { ListObjectsInput } from "@/services/controlPlane/storageBrowser/storageBrowserApi";

export interface StorageSearch {
  bucket?: string;
  prefix?: string;
  object?: string;
  token?: string;
}

/** Every StorageSearch key: what a link out of the browser carries, and what leaving it clears. */
export const STORAGE_SEARCH_KEYS = ["bucket", "prefix", "object", "token"] as const satisfies readonly (keyof StorageSearch)[];

/** The storage keys of a route's search, as strings (the route's schema already coerced them; anything else is dropped). */
export function pickStorageSearch(search: Record<string, unknown>): StorageSearch {
  const out: StorageSearch = {};
  for (const key of STORAGE_SEARCH_KEYS) {
    const value = search[key];
    if (typeof value === "string" && value !== "") out[key] = value;
  }
  return out;
}

/** The S3 and GCS key limit, which bounds a prefix and a key alike (ListObjectsRequest.prefix max_bytes). */
export const MAX_KEY_BYTES = 1024;
/** ListObjectsRequest.page_token max_len. */
export const MAX_TOKEN_LENGTH = 4096;
/** BrowsableBucket.name, as every request validates it. */
const BUCKET_NAME = /^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$/;

const encoder = new TextEncoder();

export function utf8Length(text: string): number {
  return encoder.encode(text).length;
}

export function isBucketName(name: string | undefined): name is string {
  return !!name && BUCKET_NAME.test(name);
}

/** A prefix or key the server would take: at most 1024 bytes. */
export function isSendableKey(key: string): boolean {
  return utf8Length(key) <= MAX_KEY_BYTES;
}

/** The level the URL lists: its prefix, or the root when it has none or one that cannot be sent. */
export function prefixOf(search: StorageSearch): string {
  const prefix = search.prefix ?? "";
  return isSendableKey(prefix) ? prefix : "";
}

/** The page the URL lists: its token, or the first page. */
export function tokenOf(search: StorageSearch): string {
  const token = search.token ?? "";
  return token.length <= MAX_TOKEN_LENGTH ? token : "";
}

/** The open object's key, when it is one the server would take. */
export function objectOf(search: StorageSearch): string | null {
  const key = search.object;
  return key && isSendableKey(key) ? key : null;
}

export interface ListContext {
  environmentId: string;
  bucket: string;
  access?: ListObjectsInput["access"];
  /** 0: the server's default. */
  pageSize?: number;
}

/**
 * The ListObjects request a URL means — without `openBucket`, which is not
 * the URL's to say: the hook decides it per tab session (storageAccess-model).
 */
export function listRequestOf(search: StorageSearch, ctx: ListContext): Omit<ListObjectsInput, "openBucket"> {
  return {
    environmentId: ctx.environmentId,
    bucket: ctx.bucket,
    prefix: prefixOf(search),
    pageToken: tokenOf(search),
    pageSize: ctx.pageSize ?? 0,
    ...(ctx.access ? { access: ctx.access } : {}),
  };
}

/**
 * The next URL state after `patch`, keeping what still means the same thing:
 * another bucket drops the level, the object and the page; another level
 * drops the object and the page (the token is sealed to its prefix). Paging
 * keeps the open object. Empty values are dropped, so the URL holds only what
 * is set.
 */
export function patchStorageSearch(search: StorageSearch, patch: Partial<StorageSearch>): StorageSearch {
  const next: StorageSearch = { ...search, ...patch };
  if ("bucket" in patch && patch.bucket !== search.bucket) {
    next.prefix = patch.prefix;
    next.object = patch.object;
    next.token = patch.token;
  } else if ("prefix" in patch && (patch.prefix ?? "") !== (search.prefix ?? "")) {
    next.object = patch.object;
    next.token = patch.token;
  }
  for (const key of STORAGE_SEARCH_KEYS) {
    if (next[key] === undefined || next[key] === "") delete next[key];
  }
  return next;
}

// ── Names and the breadcrumb ────────────────────────────────────────────────

/** The folder a prefix lists: everything up to and including its last "/" ("" for the root). */
export function folderOf(prefix: string): string {
  return prefix.slice(0, prefix.lastIndexOf("/") + 1);
}

/** The level an object sits in: its key up to the last "/". */
export function parentPrefix(key: string): string {
  return folderOf(key);
}

/** The last segment of a key: the file name a download gets. */
export function basename(key: string): string {
  return key.slice(key.lastIndexOf("/") + 1);
}

/**
 * How an entry of the listing is named in its folder: its key (or common
 * prefix) after the folder being listed, without a folder's trailing "/".
 * Under a typed prefix (`docs/re`) names are still relative to the folder
 * (`docs/`), so `docs/readme.txt` reads `readme.txt`, not `adme.txt`.
 */
export function entryName(prefix: string, full: string): string {
  const folder = folderOf(prefix);
  const rest = full.startsWith(folder) ? full.slice(folder.length) : full;
  return rest.endsWith("/") ? rest.slice(0, -1) : rest;
}

/** A segment as it is shown: an empty one (`a//b`) needs a word, or it reads as nothing. */
export function shownSegment(segment: string): string {
  return segment === "" ? "(empty)" : segment;
}

export interface Crumb {
  /** The segment, as stored (may be ""). */
  segment: string;
  /** The prefix this crumb lists. */
  prefix: string;
  /** A typed narrowing (the prefix does not end in "/"): "starts with", not a folder. */
  partial: boolean;
}

/**
 * The breadcrumb for a prefix, after the bucket's root: one crumb per "/"
 * segment, each the prefix up to and including it. `a//b/` is three crumbs
 * (`a`, an empty one, `b`); `docs/re` ends in a partial crumb `re`.
 */
export function breadcrumbsOf(prefix: string): Crumb[] {
  if (!prefix) return [];
  const crumbs: Crumb[] = [];
  let start = 0;
  for (let slash = prefix.indexOf("/"); slash !== -1; slash = prefix.indexOf("/", start)) {
    crumbs.push({ segment: prefix.slice(start, slash), prefix: prefix.slice(0, slash + 1), partial: false });
    start = slash + 1;
  }
  if (start < prefix.length) crumbs.push({ segment: prefix.slice(start), prefix, partial: true });
  return crumbs;
}

// ── Paging by opaque token ──────────────────────────────────────────────────

/**
 * A listing pages forward only (the token is opaque and the backend has no
 * "previous"), so the way back is remembered: the trail is the tokens of the
 * pages this tab walked through at one level, in order ("" the first page).
 * A deep link to a later page starts a trail of its own, which has no way back
 * but to the first page.
 */
export function trailWith(trail: readonly string[], current: string): string[] {
  return trail.includes(current) ? [...trail] : [current];
}

/** The trail after following Next from `current` to `next`. */
export function trailAfterNext(trail: readonly string[], current: string, next: string): string[] {
  const walked = trailWith(trail, current);
  return [...walked.slice(0, walked.indexOf(current) + 1), next];
}

/** The page before `current` in the trail, or null when the trail does not know it. */
export function previousToken(trail: readonly string[], current: string): string | null {
  const index = trail.indexOf(current);
  return index > 0 ? trail[index - 1]! : null;
}

/** The 1-based page number of `current`, when the trail starts at the first page. */
export function pageNumber(trail: readonly string[], current: string): number | null {
  const index = trail.indexOf(current);
  return index >= 0 && trail[0] === "" ? index + 1 : null;
}
