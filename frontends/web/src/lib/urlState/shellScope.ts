// Copyright (c) 2026 Reliant Labs

/**
 * THE SHELL'S SCOPE IN THE URL (owner, 2026-11-24: the URL is the only source
 * of view state). Which projects the left nav and the chat list are about:
 *
 *   one    a project:   `/project/<id>` on the chat home; `?projects=<id>` on
 *                       any other page; or implied by the page itself (a
 *                       chat's project, `/new?project=`, an app's console)
 *   many   several:     `?projects=<id>,<id>,…`
 *   all    every one:   `?projects=all`
 *
 * and, while several or all are in view, which one the nav's app sections
 * open: `?focus=<catalog key>` (a project id, or `app:<name>`).
 *
 * Both params ride along on every navigation that does not name them
 * (`retainShellScope`, the root route's search middleware), so a link from
 * a chat to the Inbox keeps the projects on screen, and a refresh, Back or a
 * shared link shows the same scope. A page that names its own project (the
 * chat home's `/project/<id>`) drops `projects` (`dropShellScope`).
 *
 * A URL that names no scope at all is filled from this tab's last scope,
 * else the last one chosen in any tab (lib/urlState/tabDefault.ts), with a
 * replace (components/Layout/ShellScopeSync.tsx): storage only ever seeds an
 * empty URL, and no tab follows another tab's choice.
 */

import { orgFreePath } from "@/lib/paths/org";
import { useActiveOrgStore } from "@/store/activeOrgStore";

import {
  defineSearchParam,
  hasParam,
  readParam,
  retainSearchParams,
  stringParam,
  writeParam,
  type SearchMiddlewareArgs,
  type SearchRecord,
} from "./searchParams";
import { createTabDefault, stringCodec } from "./tabDefault";

export type ShellScope = { mode: "all" } | { mode: "one"; projectId: string } | { mode: "many"; projectIds: string[] };

/** `?projects=` read as every project. */
export const ALL_PROJECTS_PARAM = "all";

/** The most ids one scope names (ListChatsRequest.project_ids max_items). */
const MAX_PROJECTS = 200;

function idsOf(raw: unknown): string[] | null {
  const value = typeof raw === "number" ? String(raw) : raw;
  const parts = Array.isArray(value) ? value.map((v) => (typeof v === "number" ? String(v) : v)) : typeof value === "string" ? value.split(",") : null;
  if (!parts) return null;
  const out: string[] = [];
  for (const part of parts) {
    if (typeof part !== "string") continue;
    const id = part.trim();
    if (id && !out.includes(id)) out.push(id);
    if (out.length === MAX_PROJECTS) break;
  }
  return out;
}

export function sameScope(a: ShellScope | undefined, b: ShellScope | undefined): boolean {
  if (!a || !b) return a === b;
  if (a.mode !== b.mode) return false;
  if (a.mode === "one" && b.mode === "one") return a.projectId === b.projectId;
  if (a.mode === "many" && b.mode === "many") {
    return a.projectIds.length === b.projectIds.length && a.projectIds.every((id) => b.projectIds.includes(id));
  }
  return true;
}

/** A scope of `ids`: none is every project, one is that project, more are several. */
export function scopeOfIds(ids: readonly string[]): ShellScope {
  const clean = idsOf([...ids]) ?? [];
  if (clean.length === 0) return { mode: "all" };
  if (clean.length === 1) return { mode: "one", projectId: clean[0] };
  return { mode: "many", projectIds: clean };
}

/** The scope's project ids: [] for every project. */
export function idsOfScope(scope: ShellScope): string[] {
  return scope.mode === "one" ? [scope.projectId] : scope.mode === "many" ? [...scope.projectIds] : [];
}

export const projectsParam = defineSearchParam<ShellScope>({
  key: "projects",
  parse: (raw) => {
    if (raw === ALL_PROJECTS_PARAM) return { mode: "all" };
    const ids = idsOf(raw);
    if (!ids || ids.length === 0) return undefined;
    return scopeOfIds(ids);
  },
  serialize: (scope) => (scope.mode === "all" ? ALL_PROJECTS_PARAM : idsOfScope(scope).join(",")),
  equals: sameScope,
});

export const focusParam = stringParam("focus");

/** The scope the search names, if any. */
export function scopeOfSearch(search: SearchRecord | null | undefined): ShellScope | undefined {
  return readParam(search, projectsParam);
}

/** The focus the search names, if any. */
export function focusOfSearch(search: SearchRecord | null | undefined): string | undefined {
  return readParam(search, focusParam);
}

/** `search` with `scope` (undefined: none) and, for several or all, `focus` (undefined drops it). */
export function withScope(search: SearchRecord, scope: ShellScope | undefined, focus?: string | null): SearchRecord {
  let next = writeParam(search, projectsParam, scope);
  if (focus !== undefined || !scope || scope.mode === "one") {
    next = writeParam(next, focusParam, scope && scope.mode !== "one" ? (focus ?? undefined) : undefined);
  }
  return next;
}

/** A search updater for `withScope`. */
export function scopeSearch(scope: ShellScope | undefined, focus?: string | null): (prev: SearchRecord) => SearchRecord {
  return (prev) => withScope(prev ?? {}, scope, focus);
}

const PROJECT_HOME = /^\/project\/([^/]+)\/?$/;

/** The project the chat home's path names (`/project/<id>`, either form). */
export function projectOfHomePath(pathname: string): string | undefined {
  const match = PROJECT_HOME.exec(orgFreePath(pathname));
  if (!match) return undefined;
  try {
    return decodeURIComponent(match[1]);
  } catch {
    return match[1];
  }
}

/**
 * The scope the URL itself names, before anything is implied or filled in:
 * `/project/<id>` (it ends any choice of several), else `?projects=`.
 * `explicit`: the path names it (the project's own chats).
 */
export function scopeOfUrl(pathname: string, search: SearchRecord | null | undefined): { scope: ShellScope; explicit: boolean } | null {
  const home = projectOfHomePath(pathname);
  if (home) return { scope: { mode: "one", projectId: home }, explicit: true };
  const scope = scopeOfSearch(search);
  return scope ? { scope, explicit: false } : null;
}

/** Whether the URL names a scope. */
export function urlNamesScope(pathname: string, search: SearchRecord | null | undefined): boolean {
  return !!projectOfHomePath(pathname) || hasParam(search, projectsParam);
}

// ── Navigation ──────────────────────────────────────────────────────────────

/**
 * The root route's search middleware: `projects` and `focus` ride along on a
 * navigation that does not name them (tour's retainTourParam is the model).
 * A navigation that names one, even as undefined, decides.
 */
export const retainShellScope: (args: SearchMiddlewareArgs) => SearchRecord = retainSearchParams([
  projectsParam as never,
  focusParam as never,
]);

/**
 * The chat home of one project (`/project/<id>`) names its scope in the
 * path: a `projects` carried along from the page before would contradict it.
 * Its route's search middleware, which runs inside the root's, so the root
 * sees the param named (as undefined) and does not put it back.
 */
export function dropShellScope({ search, next }: SearchMiddlewareArgs): SearchRecord {
  return { ...next(search), projects: undefined, focus: undefined };
}

// ── The default for an empty URL ────────────────────────────────────────────

/**
 * Whose defaults these are: the signed-in user's, in the tab's org (two
 * people sharing a browser never inherit each other's view, and one org's
 * projects never seed another's). Null while signed out, when nothing is
 * read or written.
 */
function userKey(): string | null {
  const { userId, effectiveId } = useActiveOrgStore.getState();
  return userId ? `${userId}.${effectiveId ?? "default"}` : null;
}

const scopeCodec = {
  parse: (raw: string) => projectsParam.parse(raw),
  serialize: (scope: ShellScope) => projectsParam.serialize(scope) ?? "",
};

/** This tab's last scope, else the last chosen in any tab. */
export const scopeDefault = createTabDefault<ShellScope>({
  key: () => {
    const user = userKey();
    return user ? `${user}.scope` : null;
  },
  ...scopeCodec,
});

/** This tab's last focus in several/all, else the last in any tab. */
export const focusDefault = createTabDefault<string>({
  key: () => {
    const user = userKey();
    return user ? `${user}.focus` : null;
  },
  ...stringCodec,
});

/** This tab's last context project (the one the workspace worked in), for a page that names none. */
export const contextProjectDefault = createTabDefault<string>({
  key: () => {
    const user = userKey();
    return user ? `${user}.contextProject` : null;
  },
  ...stringCodec,
});
