// Copyright (c) 2026 Reliant Labs

/**
 * THE PROJECT SHELL'S SCOPE (U6b, prototype): which project(s) every row of
 * the left nav is about — its chats, its app's sections.
 *
 *   one    one project (the default). Derived, in order, from:
 *            1. the route — an app's console (`/forge/p/<app>`,
 *               `/o/<org>/apps/<app>`) is the project whose app it is, else
 *               the app alone (an app-only row); `/project/<id>` and
 *               `/projects/<id>` name it; a chat (`/c/<chatId>`) is in its
 *               own project, so a chat always shows its own project; `/new`
 *               carries it in `?project=`;
 *            2. the person's last pick (localStorage);
 *            3. the project the app is working in (projectStore), else the
 *               most recently active one.
 *          A route that names a project also becomes the last pick.
 *   many   several projects: the chat list's filter of several
 *          (hooks/useChatProjectFilter, `?projects=` + `sidebar.projects`).
 *   all    every project ("All projects" in the switcher).
 *
 * In `many` and `all` the nav's Chats group by project and an app page names
 * its app in an in-page picker; the FOCUSED project (the app the nav's
 * sections open) is the route's app, else the last one picked.
 */

import { useEffect, useMemo } from "react";
import { create } from "zustand";

import { useChat } from "@/hooks/chat-queries";
import { useSidebarProjectPrefs } from "@/hooks/sidebar-project-queries";
import { useChatProjectFilterIds, useChatProjectFilterStore } from "@/hooks/useChatProjectFilter";
import { chatShellRouteOf } from "@/lib/chatRoute";
import { orgFreePath } from "@/lib/paths/org";
import {
  appRowKey,
  compareByActivity,
  type CatalogRow,
  type ProjectCatalog,
  type ProjectCatalogRow,
} from "@/services/projects/catalog";
import { useProjectStore } from "@/store/projectStore";

import { useSelectorCatalog } from "../../Projects/ProjectSelector";
import { consolePageOf, type ConsolePage } from "../../Forge/Console/paths";

/** A project by id, or an app with no code here by name. */
export type ShellProjectRef = { kind: "project"; id: string } | { kind: "app"; name: string };

const APP_KEY_PREFIX = appRowKey("");

/** The ref's key in the catalog's terms: a project id, or `app:<name>`. */
export function refKey(ref: ShellProjectRef): string {
  return ref.kind === "project" ? ref.id : appRowKey(ref.name);
}

export function refOfKey(key: string): ShellProjectRef {
  return key.startsWith(APP_KEY_PREFIX) ? { kind: "app", name: key.slice(APP_KEY_PREFIX.length) } : { kind: "project", id: key };
}

function decode(segment: string): string {
  try {
    return decodeURIComponent(segment);
  } catch {
    return segment;
  }
}

/** What the route says: a project or app, a chat whose project is still to be read, or nothing. */
export type RouteProject = { ref: ShellProjectRef; explicit: boolean } | { chatId: string } | null;

const PROJECT_PAGE = /^\/projects?\/([^/]+)/;

/**
 * `explicit`: the route is about the project itself (its chats, its page), so
 * it ends a choice of several; a chat or an app page within the choice does not.
 */
export function routeProjectOf(pathname: string, search: Record<string, unknown>): RouteProject {
  const console = consolePageOf(pathname);
  if (console) return { ref: { kind: "app", name: console.project }, explicit: false };
  const project = PROJECT_PAGE.exec(orgFreePath(pathname));
  if (project) return { ref: { kind: "project", id: decode(project[1]) }, explicit: true };
  const chat = chatShellRouteOf(pathname);
  if (chat?.kind === "chat") return { chatId: chat.chatId };
  if (chat?.kind === "new" && typeof search.project === "string" && search.project) {
    return { ref: { kind: "project", id: search.project }, explicit: false };
  }
  return null;
}

/** Whether `pathname` is a chat page: a chat, a thread, the new-chat view, the chat home. */
export function isChatPage(pathname: string): boolean {
  return chatShellRouteOf(pathname) !== null;
}

/**
 * The catalog row `ref` names. An app resolves to the project whose code
 * deploys it, when there is one, else to its own app-only row.
 */
export function rowForRef(catalog: ProjectCatalog | undefined, ref: ShellProjectRef): CatalogRow | undefined {
  if (!catalog) return undefined;
  if (ref.kind === "project") return catalog.rows.find((row) => row.kind === "project" && row.project.id === ref.id);
  const owner = catalog.rows.find(
    (row): row is ProjectCatalogRow => row.kind === "project" && row.apps.some((app) => app.name === ref.name && app.link !== "not_hosted"),
  );
  return owner ?? catalog.rows.find((row) => row.kind === "app" && row.app.name === ref.name);
}

/** The project's deployed app: a live app of the project (an app-only row is its app); null when none is deployed. */
export function deployedAppOf(row: CatalogRow | undefined): string | null {
  if (!row) return null;
  if (row.kind === "app") return row.app.name;
  const live = row.apps.find((app) => app.state === "live");
  if (live) return live.name;
  if (row.partial && row.project.is_forge && row.project.forge_project_name) return row.project.forge_project_name.trim() || null;
  return null;
}

/**
 * The app the nav's sections open for `row`: its deployed app, else the app
 * its forge.yaml names (not deployed yet), else the project's own name. Every
 * project has the same sections (owner, 2026-11-20); on an app with nothing
 * deployed each page says what it will show and how to deploy.
 */
export function sectionsAppOf(row: CatalogRow | undefined): string | null {
  if (!row) return null;
  const deployed = deployedAppOf(row);
  if (deployed) return deployed;
  if (row.kind === "project") return row.apps[0]?.name ?? (row.project.forge_project_name?.trim() || row.name);
  return null;
}

/** The console section to land on in another app: the same section, never one that names this app's objects. */
export function sectionInAnotherApp(page: ConsolePage): ConsolePage {
  switch (page.kind) {
    case "workload":
      return { kind: "overview" };
    case "release":
      return { kind: "releases" };
    case "environment":
      return { kind: "environments" };
    case "dashboard":
      return { kind: "obs-view", view: "dashboards" };
    default:
      return page;
  }
}

// ── The last pick ───────────────────────────────────────────────────────────

/** The last pick's value for "All projects". */
export const ALL_PROJECTS_PICK = "__all__";
/** The last pick's value for a choice of several (the chat filter's, remembered in `sidebar.projects`). */
export const SEVERAL_PROJECTS_PICK = "__several__";

const LAST_PICK_KEY = "shell.currentProject";
const FOCUS_KEY = "shell.focusProject";

function read(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

function write(key: string, value: string): void {
  try {
    localStorage.setItem(key, value);
  } catch {
    /* storage disabled: kept for this tab */
  }
}

interface LastPickState {
  /** A project key, or ALL_PROJECTS_PICK. */
  key: string | null;
  /** The project last picked on its own (the switcher, an app page's picker): the focus of a choice of several. */
  focus: string | null;
  set: (key: string) => void;
  setFocus: (key: string) => void;
}

export const useShellLastPick = create<LastPickState>((set) => ({
  key: read(LAST_PICK_KEY),
  focus: read(FOCUS_KEY),
  set: (key) => {
    write(LAST_PICK_KEY, key);
    const project = key !== ALL_PROJECTS_PICK && key !== SEVERAL_PROJECTS_PICK;
    if (project) write(FOCUS_KEY, key);
    set((state) => ({ key, focus: project ? key : state.focus }));
  },
  setFocus: (focus) => {
    write(FOCUS_KEY, focus);
    set({ focus });
  },
}));

// ── The hook ────────────────────────────────────────────────────────────────

export type ShellMode = "one" | "many" | "all";

export interface ShellProject {
  mode: ShellMode;
  /** many: the chosen project ids; all: every project's; one: the project's, or none. */
  ids: readonly string[];
  /** The focused project's catalog key (a project id, or `app:<name>`); null with no project at all. */
  key: string | null;
  /** The focused project's name. */
  name: string | null;
  /** The focused code project, whose chats one-project mode lists; null for an app with no code here. */
  projectId: string | null;
  /** The app the nav's sections open (sectionsAppOf); null with no project. */
  app: string | null;
  /** Whether `app` has been deployed (the console on screen counts as known). */
  deployed: boolean;
  row: CatalogRow | undefined;
  catalog: ProjectCatalog | undefined;
}

function firstRow(catalog: ProjectCatalog | undefined): CatalogRow | undefined {
  if (!catalog) return undefined;
  return [...catalog.rows].sort((a, b) => (a.kind === b.kind ? compareByActivity(a, b) : a.kind === "project" ? -1 : 1))[0];
}

function refOfRow(row: CatalogRow): ShellProjectRef {
  return row.kind === "project" ? { kind: "project", id: row.project.id } : { kind: "app", name: row.app.name };
}

export function useShellProject(pathname: string, search: Record<string, unknown>): ShellProject {
  const { catalog } = useSelectorCatalog(undefined);
  const route = useMemo(() => routeProjectOf(pathname, search), [pathname, search]);
  const chatId = route && "chatId" in route ? route.chatId : undefined;
  const { data: chat } = useChat(chatId);
  const chatProjectId = chatId && chat?.id === chatId ? chat.projectId : undefined;
  const lastPick = useShellLastPick((state) => state.key);
  const focus = useShellLastPick((state) => state.focus);
  const setLastPick = useShellLastPick((state) => state.set);
  const contextProjectId = useProjectStore((state) => state.currentProject?.id ?? null);
  const filterIds = useChatProjectFilterIds();
  // A choice of several outlives a reload of any page: the filter holds it
  // only for the tab, the setting for good.
  const { prefs } = useSidebarProjectPrefs();
  const rememberedKey = prefs.filter.join(",");
  useEffect(() => {
    if (lastPick !== SEVERAL_PROJECTS_PICK || filterIds.length >= 2 || !rememberedKey.includes(",")) return;
    useChatProjectFilterStore.getState().setMany(rememberedKey.split(","));
  }, [lastPick, filterIds.length, rememberedKey]);
  const consoleApp = consolePageOf(pathname)?.project ?? null;

  const routeRef: ShellProjectRef | null =
    route && "ref" in route ? route.ref : chatProjectId ? { kind: "project", id: chatProjectId } : null;
  const routeRefKey = routeRef ? refKey(routeRef) : null;
  const explicit = !!route && "explicit" in route && route.explicit;

  const mode: ShellMode =
    filterIds.length >= 2 && !explicit ? "many" : lastPick === ALL_PROJECTS_PICK && !explicit ? "all" : "one";
  const filterKey = filterIds.join(",");

  const resolved = useMemo((): ShellProject => {
    const allIds = (catalog?.rows ?? []).flatMap((row) => (row.kind === "project" ? [row.project.id] : []));
    const ids = mode === "many" ? filterKey.split(",") : mode === "all" ? allIds : [];
    const candidates: ShellProjectRef[] = [];
    if (routeRef) candidates.push(routeRef);
    // In a choice of several, the focus is the last project picked among them.
    if (mode !== "one" && focus) candidates.push(refOfKey(focus));
    // While a chat's project is being read, the nav keeps the last pick rather than jumping.
    if (lastPick && lastPick !== ALL_PROJECTS_PICK && lastPick !== SEVERAL_PROJECTS_PICK) candidates.push(refOfKey(lastPick));
    if (mode === "many") candidates.push(...ids.map((id): ShellProjectRef => ({ kind: "project", id })));
    if (contextProjectId) candidates.push({ kind: "project", id: contextProjectId });
    let ref: ShellProjectRef | null = null;
    let row: CatalogRow | undefined;
    for (const candidate of candidates) {
      if (mode === "many" && candidate !== routeRef && candidate.kind === "project" && !ids.includes(candidate.id)) continue;
      const found = rowForRef(catalog, candidate);
      // The route's own ref holds even when the catalog does not list it (an app it never read).
      if (found || candidate === routeRef || !catalog) {
        ref = candidate;
        row = found;
        break;
      }
    }
    if (!ref) {
      row = firstRow(catalog);
      if (row) ref = refOfRow(row);
    }
    const base = { mode, ids: mode === "one" ? [] : ids, catalog };
    if (!ref) return { ...base, key: null, name: null, projectId: null, app: consoleApp, deployed: !!consoleApp, row: undefined };
    const key = row ? row.key : refKey(ref);
    const projectId = row ? (row.kind === "project" ? row.project.id : null) : ref.kind === "project" ? ref.id : null;
    const name = row ? row.name : ref.kind === "app" ? ref.name : null;
    // The console on screen always has its sections, whatever the catalog says.
    const app = consoleApp ?? sectionsAppOf(row) ?? (ref.kind === "app" ? ref.name : null);
    const deployed = !!deployedAppOf(row) || (!!consoleApp && !row);
    return { ...base, ids: mode === "one" ? (projectId ? [projectId] : []) : ids, key, name, projectId, app, deployed, row };
    // routeRef is rebuilt every render; its key is what changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [routeRefKey, lastPick, focus, contextProjectId, catalog, consoleApp, mode, filterKey]);

  // In one-project mode a route that names a project becomes the last pick;
  // in a choice of several only an explicit one does (and ends the choice).
  useEffect(() => {
    if (!routeRefKey || !resolved.key) return;
    if (mode === "one" ? resolved.key !== lastPick : explicit) setLastPick(resolved.key);
  }, [routeRefKey, resolved.key, lastPick, setLastPick, mode, explicit]);

  return resolved;
}

/**
 * Whether the shell shows several projects, and which (for pages under the
 * route outlet: an app page's picker, the Overview of several). The same
 * rule as useShellProject's mode, without the route.
 */
export function useShellSeveral(): { mode: "many" | "all" | null; rows: ProjectCatalogRow[] } {
  const filterIds = useChatProjectFilterIds();
  const lastPick = useShellLastPick((state) => state.key);
  const { catalog } = useSelectorCatalog(undefined);
  const mode = filterIds.length >= 2 ? "many" : lastPick === ALL_PROJECTS_PICK ? "all" : null;
  const filterKey = filterIds.join(",");
  const rows = useMemo(() => {
    if (!mode || !catalog) return [];
    const ids = filterKey.split(",");
    return catalog.rows.filter(
      (row): row is ProjectCatalogRow => row.kind === "project" && (mode === "all" || ids.includes(row.project.id)),
    );
  }, [mode, catalog, filterKey]);
  return { mode, rows };
}
