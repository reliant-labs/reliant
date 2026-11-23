// Copyright (c) 2025 Reliant Labs

/**
 * The chat list's project filter, choose N of M (lib/chatProjectFilter): what
 * the sidebar lists, read and changed in one place.
 *
 *   none   every project: `/`, no filter;
 *   one    the single-project filter old builds read and write, unchanged:
 *          `/project/<id>` and workspaceState's `projectFilterId`
 *          (projectStore.setProjectFilter);
 *   many   `?projects=<id>,<id>` on the chat home, held here while the person
 *          is on another page (the Inbox keeps the sidebar it had).
 *
 * The URL is the truth on the chat home: a refresh or a shared link keeps the
 * choice, and arriving at `/` without the param is every project, as arriving
 * there from `/project/<id>` always was. The choice is also remembered in the
 * person's `sidebar.projects` setting (`filter`, per org), so a new tab that
 * opens on the bare home starts with the last choice.
 *
 * `useChatProjectFilter({ sync: { pathname, search } })` runs the URL,
 * remembered-choice and org bridges, from the page's location: the sidebar
 * (which the shell hands the location) calls it so, once. Every other reader
 * omits it. Navigation goes through the app's registered router, as
 * projectStore's does, so the hook needs no router context of its own.
 */

import { useCallback, useEffect, useMemo, useRef } from "react";
import { create } from "zustand";

import { homePath } from "@/lib/paths";
import { orgFreePath } from "@/lib/paths/org";
import { formatProjectsParam, normalizeProjectIds, parseProjectsParam, sameProjectSet } from "@/lib/chatProjectFilter";
import { useOrgScope } from "@/lib/orgScope";
import { leaveChatPageFor, useProjectStore } from "@/store/projectStore";
import { useWorkspaceStateStore } from "@/store/workspaceStateStore";

import { useProjects } from "./project-queries";
import { useSidebarProjectPrefs } from "./sidebar-project-queries";

export type ChatProjectFilterMode = "all" | "one" | "many";

export interface ChatProjectFilter {
  mode: ChatProjectFilterMode;
  /** The projects chosen: [] for every project. */
  ids: readonly string[];
  /** Choose these projects ([] for every project), and remember the choice. */
  set: (projectIds: readonly string[]) => void;
  /** Every project again. */
  clear: () => void;
}

interface ManyState {
  /** Two or more projects; [] otherwise (one is projectFilterId's). */
  many: string[];
  setMany: (ids: string[]) => void;
}

/** The filter of several projects, held for the tab. */
export const useChatProjectFilterStore = create<ManyState>((set) => ({
  many: [],
  setMany: (ids) => set((state) => (sameProjectSet(state.many, ids) ? state : { many: ids })),
}));

/** The filter now, outside React (the list's scope for an imperative read). */
export function chatProjectFilterIds(): string[] {
  const one = useWorkspaceStateStore.getState().projectFilterId;
  if (one) return [one];
  return useChatProjectFilterStore.getState().many;
}

/** The projects the chat list is filtered to ([] for every project), re-rendering on a change. */
export function useChatProjectFilterIds(): readonly string[] {
  return useFilterState().ids;
}

type RouterLike = {
  state: { location: { pathname: string } };
  navigate: (opts: { to: string; search?: unknown; replace?: boolean }) => unknown;
};

function getRouter(): RouterLike | null {
  return (globalThis as { __RELIANT_ROUTER?: RouterLike }).__RELIANT_ROUTER ?? null;
}

/** Whether a pathname is the chat shell, where the filter lives in the URL. */
function isChatShellPath(pathname: string): boolean {
  const path = orgFreePath(pathname);
  return path === "/" || path.startsWith("/project/");
}

// The remembered choice is applied once per tab, on the first bare home.
let restoredThisTab = false;

/** Tests: apply the remembered choice again, as a new tab would. */
export function resetChatProjectFilterForTests(): void {
  restoredThisTab = false;
  useChatProjectFilterStore.setState({ many: [] });
}

/** The filter's mode and ids, from the two stores. */
function useFilterState(): { mode: ChatProjectFilterMode; ids: readonly string[] } {
  const one = useWorkspaceStateStore((s) => s.projectFilterId);
  const many = useChatProjectFilterStore((s) => s.many);
  return useMemo(() => {
    if (one) return { mode: "one", ids: [one] };
    if (many.length >= 2) return { mode: "many", ids: many };
    return { mode: "all", ids: [] };
  }, [one, many]);
}

/** The page's location, for the bridges (the sidebar's props). */
export interface ChatProjectFilterLocation {
  pathname: string;
  search: Readonly<Record<string, unknown>>;
}

export function useChatProjectFilter(options: { sync?: ChatProjectFilterLocation } = {}): ChatProjectFilter {
  const state = useFilterState();
  const prefs = useSidebarProjectPrefs();
  const { setFilter: remember } = prefs;

  /** Show `ids` (normalized), without remembering. */
  const apply = useCallback(
    (ids: string[]) => {
      const { projectFilterId } = useWorkspaceStateStore.getState();
      const router = getRouter();
      const pathname = router?.state.location.pathname ?? "";
      if (ids.length === 1) {
        useChatProjectFilterStore.getState().setMany([]);
        // The single-project filter old builds read: `/project/<id>`. From a
        // chat's page, its chats: the URL's chat wins over a selection.
        leaveChatPageFor(ids[0]);
        void useProjectStore.getState().setProjectFilter(ids[0]);
        return;
      }
      if (ids.length === 0) {
        useChatProjectFilterStore.getState().setMany([]);
        if (projectFilterId) {
          void useProjectStore.getState().setProjectFilter(null);
          return;
        }
        if (orgFreePath(pathname) === "/") {
          void router?.navigate({ to: homePath(), search: dropProjects, replace: true });
        }
        return;
      }
      useChatProjectFilterStore.getState().setMany(ids);
      // Out of the single-project filter without its navigation home (the
      // one below names the projects), so the bridge never sees a bare `/`.
      if (projectFilterId) useWorkspaceStateStore.getState().setProjectFilterId(null);
      if (isChatShellPath(pathname)) {
        void router?.navigate({ to: homePath(), search: withProjects(ids) });
      }
    },
    [],
  );

  const set = useCallback(
    (projectIds: readonly string[]) => {
      const ids = normalizeProjectIds(projectIds);
      remember(ids);
      apply(ids);
    },
    [apply, remember],
  );
  const clear = useCallback(() => set([]), [set]);

  // ── The bridges (the sidebar's call only) ────────────────────────────────

  const sync = options.sync !== undefined;
  const pathname = options.sync?.pathname ?? "";
  const onHome = sync && orgFreePath(pathname) === "/";
  const param = onHome ? parseProjectsParam(options.sync?.search.projects) : null;
  const paramKey = param?.join(",") ?? null;
  const { projects, isLoading: projectsLoading } = useProjects();
  const org = useOrgScope();

  // The single-project filter set from anywhere (a link, ⌘K) ends a filter of
  // several. Before the URL's bridge: a URL naming several, at load, wins
  // over a persisted single filter.
  const one = useWorkspaceStateStore((s) => s.projectFilterId);
  useEffect(() => {
    if (sync && one) useChatProjectFilterStore.getState().setMany([]);
  }, [sync, one]);

  // The URL, on the chat home: the param is the choice; none is every project.
  useEffect(() => {
    if (!sync || paramKey === null) return;
    const ids = paramKey ? paramKey.split(",") : [];
    if (ids.length === 1) {
      // A link naming one project is that project's filter.
      void useProjectStore.getState().setProjectFilter(ids[0]);
      return;
    }
    useChatProjectFilterStore.getState().setMany(ids.length >= 2 ? ids : []);
    if (ids.length >= 2 && useWorkspaceStateStore.getState().projectFilterId) {
      useWorkspaceStateStore.getState().setProjectFilterId(null);
    }
  }, [sync, paramKey]);

  // Another org: its projects, so none of the old org's choice. The org
  // becoming known (none → the tab's) is not a switch: the URL's choice is
  // that org's.
  const lastOrg = useRef(org.orgId);
  useEffect(() => {
    if (!sync) return;
    const previous = lastOrg.current;
    lastOrg.current = org.orgId;
    if (previous !== null && org.orgId !== null && previous !== org.orgId) {
      useChatProjectFilterStore.getState().setMany([]);
    }
  }, [sync, org.orgId]);

  // A new tab on the bare home starts with the remembered choice, once the
  // setting and the project list have loaded (a project since removed is
  // left out).
  const firstPath = useRef(pathname);
  const remembered = prefs.prefs.filter;
  const prefsSettled = prefs.writable || prefs.loadFailed;
  useEffect(() => {
    if (!sync || restoredThisTab || !prefsSettled || projectsLoading) return;
    restoredThisTab = true;
    if (orgFreePath(firstPath.current) !== "/" || paramKey || useWorkspaceStateStore.getState().projectFilterId) return;
    const known = new Set(projects.map((p) => p.id));
    const ids = remembered.filter((id) => known.has(id));
    if (ids.length > 0) apply(ids);
  }, [sync, prefsSettled, projectsLoading, projects, remembered, paramKey, apply]);

  return useMemo(() => ({ ...state, set, clear }), [state, set, clear]);
}

function dropProjects(prev: Record<string, unknown>): Record<string, unknown> {
  const { projects: _projects, ...rest } = prev;
  return rest;
}

function withProjects(ids: readonly string[]) {
  return (prev: Record<string, unknown>) => ({ ...dropProjects(prev), projects: formatProjectsParam(ids) });
}
