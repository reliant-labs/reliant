// Copyright (c) 2025 Reliant Labs

/**
 * The chat list's project filter, choose N of M (owner, 2026-11-19): which
 * projects' chats the sidebar lists. Pure: no React, no router.
 *
 *   none chosen   every project (`/`), as before;
 *   one           that project, through the single-project filter old builds
 *                 read and write: `/project/<id>` and workspaceState's
 *                 `projectFilterId` (projectStore.setProjectFilter);
 *   two or more   `/?projects=<id>,<id>` on the chat home, and ListChats'
 *                 `project_ids` (the union of the projects).
 *
 * A chat list cache key names its scope as one string (hooks/chat-queries
 * chatKeys.list): a project id, or for several the PROJECTS_SCOPE_PREFIX
 * followed by the ids sorted and comma-joined, so one set of projects is one
 * cache entry whatever order it was picked in.
 */

/** The most ids one list asks for (ListChatsRequest.project_ids max_items). */
export const MAX_FILTER_PROJECTS = 200;

/** A cache scope naming several projects starts with this; no project id does. */
export const PROJECTS_SCOPE_PREFIX = "projects:";

/** The ids of `ids`, each once, none empty, in their order, at most MAX_FILTER_PROJECTS. */
export function normalizeProjectIds(ids: Iterable<unknown>): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  for (const id of ids) {
    if (typeof id !== "string") continue;
    const trimmed = id.trim();
    if (!trimmed || seen.has(trimmed)) continue;
    seen.add(trimmed);
    out.push(trimmed);
    if (out.length === MAX_FILTER_PROJECTS) break;
  }
  return out;
}

/**
 * The `?projects=` search value, read: comma-separated ids. The router's
 * default parser JSON-parses a value it can, so a lone number or an array
 * arrives as one; anything else is no filter.
 */
export function parseProjectsParam(value: unknown): string[] {
  if (typeof value === "string") return normalizeProjectIds(value.split(","));
  if (Array.isArray(value)) return normalizeProjectIds(value.map((v) => (typeof v === "number" ? String(v) : v)));
  if (typeof value === "number") return normalizeProjectIds([String(value)]);
  return [];
}

/** The `?projects=` value for `ids`; undefined (no param) for fewer than two. */
export function formatProjectsParam(ids: readonly string[]): string | undefined {
  const normal = normalizeProjectIds(ids);
  return normal.length >= 2 ? normal.join(",") : undefined;
}

/** Whether two id lists name the same set of projects. */
export function sameProjectSet(a: readonly string[], b: readonly string[]): boolean {
  if (a.length !== b.length) return false;
  const set = new Set(a);
  return b.every((id) => set.has(id));
}

/**
 * The chat list scope of a filter: null for every project, the id for one,
 * and for several the prefix and the sorted ids (one cache entry per set).
 */
export function chatListScope(ids: readonly string[] | string | null | undefined): string | null {
  if (ids == null) return null;
  if (typeof ids === "string") return ids || null;
  const normal = normalizeProjectIds(ids);
  if (normal.length === 0) return null;
  if (normal.length === 1) return normal[0];
  return `${PROJECTS_SCOPE_PREFIX}${[...normal].sort().join(",")}`;
}

/** The project ids a list scope names: [] for every project. */
export function projectIdsOfScope(scope: string | null): string[] {
  if (!scope) return [];
  if (scope.startsWith(PROJECTS_SCOPE_PREFIX)) return scope.slice(PROJECTS_SCOPE_PREFIX.length).split(",").filter(Boolean);
  return [scope];
}

/** Whether a list of `scope` (null: every project) lists `projectId`'s chats. */
export function scopeListsProject(scope: string | null, projectId: string | undefined): boolean {
  if (scope === null) return true;
  if (!projectId) return false;
  return projectIdsOfScope(scope).includes(projectId);
}

/** The heading's summary of a filter of several: "3 projects". */
export function projectsSummary(count: number): string {
  return `${count} ${count === 1 ? "project" : "projects"}`;
}
