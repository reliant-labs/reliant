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
 *            2. the person's last pick in this tab;
 *            3. the project the app is working in (projectStore), else the
 *               most recently active one.
 *          A route that names a project also becomes the last pick.
 *   many   several projects: "Select several projects…" in the switcher
 *          (hooks/useChatProjectFilter, `?projects=<id>,<id>`).
 *   all    every project ("All projects" in the switcher): `?projects=all`.
 *
 * In `many` and `all` the nav's Chats group by project and an app page shows
 * every project in scope (Forge/Console/severalProjects.tsx); the FOCUSED
 * project (the app the nav's sections open) is the route's app, else
 * `?focus=`, else the last one picked.
 *
 * The URL names the scope on every page (`projects`, `focus`: routeSchemas
 * PROJECTS_CARRIED, kept by the root route). Storage only fills a tab's
 * first bare home (this tab's default, else the last scope any tab showed:
 * lib/urlState/shellScope.ts scopeDefault), and the tab's last pick, started
 * from that default, fills a page that names none until a URL of the page
 * load names one; so no tab ever follows another's choice, and Back brings
 * back each entry's scope.
 */

import { useEffect, useMemo } from "react";
import { create } from "zustand";

import { useChat } from "@/hooks/chat-queries";
import { chatShellRouteOf } from "@/lib/chatRoute";
import { orgFreePath } from "@/lib/paths/org";
import { focusOfSearch, scopeOfSearch } from "@/lib/urlState/shellScope";
import { publishShellScope, seededScope, useShellScopeStore } from "@/lib/urlState/shellScopeStore";
import { useActiveOrgStore } from "@/store/activeOrgStore";
import {
  appRowKey,
  compareByActivity,
  ownAppsOf,
  type AppChipModel,
  type CatalogRow,
  type ProjectCatalog,
  type ProjectCatalogRow,
} from "@/services/projects/catalog";
import { useProjectStore } from "@/store/projectStore";

import { useSelectorCatalog } from "../../Projects/ProjectSelector";
import { consolePageOf, type ConsolePage } from "../../Forge/Console/paths";
import { isSampleApp, SAMPLE_APP, SAMPLE_APP_LABEL } from "../../Forge/Sample/sampleApp";

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
 * The catalog row `ref` names. An app resolves to the project whose own code
 * deploys it (ownAppsOf: not a parent checkout that merely holds its repo),
 * else to a project holding it anywhere, else to its own app-only row.
 */
export function rowForRef(catalog: ProjectCatalog | undefined, ref: ShellProjectRef): CatalogRow | undefined {
  if (!catalog) return undefined;
  if (ref.kind === "project") return catalog.rows.find((row) => row.kind === "project" && row.project.id === ref.id);
  const holds = (apps: readonly AppChipModel[]) => apps.some((app) => app.name === ref.name && app.link !== "not_hosted");
  const projects = catalog.rows.filter((row): row is ProjectCatalogRow => row.kind === "project");
  const owner = projects.find((row) => holds(ownAppsOf(row))) ?? projects.find((row) => holds(row.apps));
  return owner ?? catalog.rows.find((row) => row.kind === "app" && row.app.name === ref.name);
}

/** The project's deployed app: a live app of its own (an app-only row is its app); null when none is deployed. */
export function deployedAppOf(row: CatalogRow | undefined): string | null {
  if (!row) return null;
  if (row.kind === "app") return row.app.name;
  const live = ownAppsOf(row).find((app) => app.state === "live");
  if (live) return live.name;
  if (row.partial && row.project.is_forge && row.project.forge_project_name) return row.project.forge_project_name.trim() || null;
  return null;
}

/**
 * The app the nav's sections open for `row`: its deployed app, else the app
 * its own forge.yaml names (not deployed yet; a nested repository's app is
 * not the project's, ownAppsOf), else the project's own name. Every
 * project has the same sections (owner, 2026-11-20); on an app with nothing
 * deployed each page says what it will show and how to deploy.
 */
export function sectionsAppOf(row: CatalogRow | undefined): string | null {
  if (!row) return null;
  const deployed = deployedAppOf(row);
  if (deployed) return deployed;
  if (row.kind === "project") return ownAppsOf(row)[0]?.name ?? (row.project.forge_project_name?.trim() || row.name);
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
    case "service":
      return { kind: "services" };
    default:
      return page;
  }
}

// ── The last pick ───────────────────────────────────────────────────────────

/**
 * Whether a route's project may become the last pick (or the focus): any but
 * the forge tour's sample app (Forge/Sample/sampleApp.ts), so leaving the tour
 * leaves no sample in the nav, on a reload or in another tab.
 */
export function remembersPick(key: string): boolean {
  return key !== refKey({ kind: "app", name: SAMPLE_APP });
}

/** The last pick's value for "All projects". */
export const ALL_PROJECTS_PICK = "__all__";
/** The last pick's value for a choice of several (the chat filter's, remembered in `sidebar.projects`). */
export const SEVERAL_PROJECTS_PICK = "__several__";

interface LastPickState {
  /** A project key, ALL_PROJECTS_PICK, or SEVERAL_PROJECTS_PICK. */
  key: string | null;
  /** The projects of the last choice of several (the tab's copy of `sidebar.projects`' filter). */
  several: string[];
  /** The project last picked on its own (the switcher, a section's "open alone"): the focus of a choice of several. */
  focus: string | null;
  set: (key: string) => void;
  setFocus: (key: string) => void;
  /** Choose several: SEVERAL_PROJECTS_PICK, remembering which. */
  setSeveral: (ids: readonly string[]) => void;
  /**
   * A URL of this page load has named a scope: from then on the URL alone
   * decides (Back to an entry that names none shows one project). Until
   * then the pick, started from the tab's default, fills a page that names
   * none.
   */
  urlDecided: boolean;
}

/**
 * The tab's memory of the pick: no storage of its own. It starts from the
 * tab's default (seededScope: this tab's last scope, else the last one any
 * tab showed) for the signed-in user and org, and is started again from
 * that org's when the tab switches org.
 */
function pickOfDefault(): Pick<LastPickState, "key" | "several" | "focus"> {
  const { scope, focus } = seededScope();
  if (!scope) return { key: null, several: [], focus: focus ?? null };
  if (scope.mode === "all") return { key: ALL_PROJECTS_PICK, several: [], focus: focus ?? null };
  if (scope.mode === "many") return { key: SEVERAL_PROJECTS_PICK, several: [...scope.projectIds], focus: focus ?? null };
  return { key: scope.projectId, several: [], focus: scope.projectId };
}

export const useShellLastPick = create<LastPickState>((set) => ({
  ...pickOfDefault(),
  urlDecided: false,
  set: (key) => {
    const project = key !== ALL_PROJECTS_PICK && key !== SEVERAL_PROJECTS_PICK;
    set((state) => ({ key, focus: project ? key : state.focus }));
  },
  setFocus: (focus) => set({ focus }),
  setSeveral: (ids) => set({ key: SEVERAL_PROJECTS_PICK, several: [...ids] }),
}));

useActiveOrgStore.subscribe((state, prev) => {
  if (state.userId !== prev.userId || state.effectiveId !== prev.effectiveId) useShellLastPick.setState(pickOfDefault());
});

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
  const pickedFocus = useShellLastPick((state) => state.focus);
  const setLastPick = useShellLastPick((state) => state.set);
  const contextProjectId = useProjectStore((state) => state.currentProject?.id ?? null);
  // What the URL names: `?projects=` (all, or several) and `?focus=`. Every
  // page carries them (the root's retainSearchParams), so the URL alone
  // decides: a page that names neither shows one project, and Back brings
  // back the scope each entry had. A tab's first bare home is filled from
  // the tab's default (hooks/useChatProjectFilter).
  const urlScope = scopeOfSearch(search);
  const urlAll = urlScope?.mode === "all";
  const urlProjectId = urlScope?.mode === "one" ? urlScope.projectId : null;
  const urlFocus = focusOfSearch(search) ?? null;
  const focus = urlFocus ?? pickedFocus;
  const consoleApp = consolePageOf(pathname)?.project ?? null;

  const routeRef: ShellProjectRef | null =
    route && "ref" in route ? route.ref : chatProjectId ? { kind: "project", id: chatProjectId } : null;
  const routeRefKey = routeRef ? refKey(routeRef) : null;
  const explicit = !!route && "explicit" in route && route.explicit;

  // Until a URL of this page load names a scope, the tab's pick fills a page that names none.
  const urlDecided = useShellLastPick((state) => state.urlDecided);
  const severalPick = useShellLastPick((state) => state.several);
  const fromPick = !urlScope && !urlDecided;
  const severalIds =
    urlScope?.mode === "many" ? urlScope.projectIds : fromPick && lastPick === SEVERAL_PROJECTS_PICK ? severalPick : [];
  const mode: ShellMode = explicit
    ? "one"
    : urlAll
      ? "all"
      : severalIds.length >= 2
        ? "many"
        : fromPick && lastPick === ALL_PROJECTS_PICK
          ? "all"
          : "one";
  const filterKey = severalIds.join(",");

  // The URL's All and focus are the tab's last pick too (Billing reads it; a pick of one project starts from it).
  useEffect(() => {
    if (urlScope && !useShellLastPick.getState().urlDecided) useShellLastPick.setState({ urlDecided: true });
    if (explicit) return;
    if (urlAll && lastPick !== ALL_PROJECTS_PICK) setLastPick(ALL_PROJECTS_PICK);
    if (urlFocus && urlFocus !== pickedFocus && remembersPick(urlFocus)) useShellLastPick.getState().setFocus(urlFocus);
  }, [urlScope, explicit, urlAll, urlFocus, lastPick, pickedFocus, setLastPick]);

  const resolved = useMemo((): ShellProject => {
    const allIds = (catalog?.rows ?? []).flatMap((row) => (row.kind === "project" ? [row.project.id] : []));
    const ids = mode === "many" ? filterKey.split(",") : mode === "all" ? allIds : [];
    const candidates: ShellProjectRef[] = [];
    // A carried single-project scope is as authoritative as All or several.
    // Only an explicit project path overrides it; a chat/app can be opened
    // within another project's shell without changing the list's scope.
    const scopeRef: ShellProjectRef | null = !explicit && urlProjectId ? { kind: "project", id: urlProjectId } : null;
    if (scopeRef) candidates.push(scopeRef);
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
      // URL refs hold even when the catalog does not list them yet.
      if (found || candidate === routeRef || candidate === scopeRef || !catalog) {
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
    const name = row ? row.name : ref.kind === "app" ? (isSampleApp(ref.name) ? SAMPLE_APP_LABEL : ref.name) : null;
    // The console on screen always has its sections, whatever the catalog says.
    const app = consoleApp ?? sectionsAppOf(row) ?? (ref.kind === "app" ? ref.name : null);
    const deployed = !!deployedAppOf(row) || (!!consoleApp && !row);
    return { ...base, ids: mode === "one" ? (projectId ? [projectId] : []) : ids, key, name, projectId, app, deployed, row };
    // routeRef is rebuilt every render; its key is what changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [routeRefKey, explicit, urlProjectId, lastPick, focus, contextProjectId, catalog, consoleApp, mode, filterKey]);

  // In one-project mode a route that names a project becomes the last pick;
  // in a choice of several only an explicit one does (and ends the choice).
  // The tour's sample app never does (remembersPick).
  useEffect(() => {
    if (!routeRefKey || !resolved.key || !remembersPick(resolved.key)) return;
    if (mode === "one" ? resolved.key !== lastPick : explicit) setLastPick(resolved.key);
  }, [routeRefKey, resolved.key, lastPick, setLastPick, mode, explicit]);

  return resolved;
}

/**
 * The sidebar's call: what the nav shows is the scope every other reader
 * takes (lib/urlState/shellScopeStore.ts shellScopeNow), and this tab's
 * default for an empty URL. An app with no code here names no project, so
 * the readers keep the chat list's filter.
 */
export function usePublishShellScope(shell: ShellProject, search: Record<string, unknown>): void {
  const named = shell.mode === "one" ? null : (focusOfSearch(search) ?? useShellLastPick.getState().focus ?? null);
  // The tour's sample app is never published as a focus: the published scope is every tab's default for an empty URL.
  const focus = named && remembersPick(named) ? named : null;
  const scopeKey =
    shell.mode === "one" ? (shell.projectId ? `one:${shell.projectId}` : "") : shell.mode === "many" ? `many:${shell.ids.join(",")}` : "all";
  useEffect(() => {
    if (!scopeKey) return publishShellScope(null);
    const [mode, ids = ""] = scopeKey.split(":");
    publishShellScope({
      scope: mode === "one" ? { mode: "one", projectId: ids } : mode === "many" ? { mode: "many", projectIds: ids.split(",") } : { mode: "all" },
      focus,
    });
  }, [scopeKey, focus]);
  useEffect(() => () => publishShellScope(null), []);
}

/**
 * Whether the shell shows several projects, and which (for pages under the
 * route outlet: an app page across several, the Overview of several): what the nav
 * on screen publishes (usePublishShellScope).
 */
export function useShellSeveral(): { mode: "many" | "all" | null; rows: ProjectCatalogRow[] } {
  const published = useShellScopeStore((state) => state.published);
  const { catalog } = useSelectorCatalog(undefined);
  const mode = published?.scope.mode === "many" ? "many" : published?.scope.mode === "all" ? "all" : null;
  const filterKey = published?.scope.mode === "many" ? published.scope.projectIds.join(",") : "";
  const rows = useMemo(() => {
    if (!mode || !catalog) return [];
    const ids = filterKey.split(",");
    return catalog.rows.filter(
      (row): row is ProjectCatalogRow => row.kind === "project" && (mode === "all" || ids.includes(row.project.id)),
    );
  }, [mode, catalog, filterKey]);
  return { mode, rows };
}
