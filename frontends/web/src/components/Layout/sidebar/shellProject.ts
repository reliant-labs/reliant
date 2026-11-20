// Copyright (c) 2026 Reliant Labs

/**
 * THE PROJECT SHELL'S CURRENT PROJECT (U6b, prototype): the one project every
 * row of the left nav is about — its chats, its app's sections. One at a
 * time; the switcher at the nav's top changes it.
 *
 * Derived, in order, from:
 *   1. the route — an app's console (`/forge/p/<app>`, `/o/<org>/apps/<app>`)
 *      is the project whose app it is, else the app alone (an app-only row);
 *      `/project/<id>` and `/projects/<id>` name it; a chat (`/c/<chatId>`) is
 *      in its own project, so a chat always shows its own project; `/new`
 *      carries it in `?project=`;
 *   2. the person's last pick (remembered in localStorage);
 *   3. the project the app is working in (projectStore), else the most
 *      recently active one.
 *
 * A route that names a project also becomes the last pick, so leaving a chat
 * for the Inbox keeps its project in the switcher.
 */

import { useEffect, useMemo } from "react";
import { create } from "zustand";

import { useChat } from "@/hooks/chat-queries";
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
export type RouteProject = { ref: ShellProjectRef } | { chatId: string } | null;

const PROJECT_PAGE = /^\/projects?\/([^/]+)/;

export function routeProjectOf(pathname: string, search: Record<string, unknown>): RouteProject {
  const console = consolePageOf(pathname);
  if (console) return { ref: { kind: "app", name: console.project } };
  const project = PROJECT_PAGE.exec(orgFreePath(pathname));
  if (project) return { ref: { kind: "project", id: decode(project[1]) } };
  const chat = chatShellRouteOf(pathname);
  if (chat?.kind === "chat") return { chatId: chat.chatId };
  if (chat?.kind === "new" && typeof search.project === "string" && search.project) {
    return { ref: { kind: "project", id: search.project } };
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

/**
 * The deployed app whose sections the nav shows for `row`: a live app of the
 * project (an app-only row is its app). A plain project list (the catalog not
 * answered) knows only the forge.yaml's name.
 */
export function deployedAppOf(row: CatalogRow | undefined): string | null {
  if (!row) return null;
  if (row.kind === "app") return row.app.name;
  const live = row.apps.find((app) => app.state === "live");
  if (live) return live.name;
  if (row.partial && row.project.is_forge && row.project.forge_project_name) return row.project.forge_project_name.trim() || null;
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

const LAST_PICK_KEY = "shell.currentProject";

function readLastPick(): string | null {
  try {
    return localStorage.getItem(LAST_PICK_KEY);
  } catch {
    return null;
  }
}

interface LastPickState {
  key: string | null;
  set: (key: string) => void;
}

export const useShellLastPick = create<LastPickState>((set) => ({
  key: readLastPick(),
  set: (key) => {
    try {
      localStorage.setItem(LAST_PICK_KEY, key);
    } catch {
      /* storage disabled: kept for this tab */
    }
    set({ key });
  },
}));

// ── The hook ────────────────────────────────────────────────────────────────

export interface ShellProject {
  /** The catalog key (a project id, or `app:<name>`); null with no project at all. */
  key: string | null;
  /** The switcher's label. */
  name: string | null;
  /** The code project, whose chats the nav lists; null for an app with no code here. */
  projectId: string | null;
  /** The deployed app whose sections the nav shows; null hides them. */
  app: string | null;
  row: CatalogRow | undefined;
}

export function useShellProject(pathname: string, search: Record<string, unknown>): ShellProject {
  const { catalog } = useSelectorCatalog(undefined);
  const route = useMemo(() => routeProjectOf(pathname, search), [pathname, search]);
  const chatId = route && "chatId" in route ? route.chatId : undefined;
  const { data: chat } = useChat(chatId);
  const chatProjectId = chatId && chat?.id === chatId ? chat.projectId : undefined;
  const lastPick = useShellLastPick((state) => state.key);
  const setLastPick = useShellLastPick((state) => state.set);
  const contextProjectId = useProjectStore((state) => state.currentProject?.id ?? null);
  const consoleApp = consolePageOf(pathname)?.project ?? null;

  const routeRef: ShellProjectRef | null =
    route && "ref" in route ? route.ref : chatProjectId ? { kind: "project", id: chatProjectId } : null;
  const routeRefKey = routeRef ? refKey(routeRef) : null;

  const resolved = useMemo((): ShellProject => {
    const candidates: ShellProjectRef[] = [];
    if (routeRef) candidates.push(routeRef);
    // While a chat's project is being read, the nav keeps the last pick rather than jumping.
    if (lastPick) candidates.push(refOfKey(lastPick));
    if (contextProjectId) candidates.push({ kind: "project", id: contextProjectId });
    let ref: ShellProjectRef | null = null;
    let row: CatalogRow | undefined;
    for (const candidate of candidates) {
      const found = rowForRef(catalog, candidate);
      // The route's own ref holds even when the catalog does not list it (an app it never read).
      if (found || candidate === routeRef || !catalog) {
        ref = candidate;
        row = found;
        break;
      }
    }
    if (!ref && catalog) {
      row = [...catalog.rows].sort((a, b) => (a.kind === b.kind ? compareByActivity(a, b) : a.kind === "project" ? -1 : 1))[0];
      if (row) ref = row.kind === "project" ? { kind: "project", id: row.project.id } : { kind: "app", name: row.app.name };
    }
    if (!ref) return { key: null, name: null, projectId: null, app: consoleApp, row: undefined };
    const key = row ? row.key : refKey(ref);
    const projectId = row ? (row.kind === "project" ? row.project.id : null) : ref.kind === "project" ? ref.id : null;
    const name = row ? row.name : ref.kind === "app" ? ref.name : null;
    // The console on screen always has its sections, whatever the catalog says.
    const app = consoleApp ?? deployedAppOf(row);
    return { key, name, projectId, app, row };
    // routeRef is rebuilt every render; its key is what changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [routeRefKey, lastPick, contextProjectId, catalog, consoleApp]);

  // A route that names a project becomes the last pick.
  useEffect(() => {
    if (routeRefKey && resolved.key && resolved.key !== lastPick) setLastPick(resolved.key);
  }, [routeRefKey, resolved.key, lastPick, setLastPick]);

  return resolved;
}
