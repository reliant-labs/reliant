// Copyright (c) 2026 Reliant Labs

/**
 * THE SHELL'S SCOPE ON SCREEN, for readers outside React (owner, 2026-11-24:
 * the URL is the only source of view state).
 *
 * The left nav resolves which projects the shell is about from the URL
 * (components/Layout/sidebar/shellProject.ts useShellProject) and publishes
 * it here, once, from the one sidebar on screen. The chat list's load, the
 * keyboard's chat order, a new chat's project, chat search and the tray read
 * it (`shellScopeNow()`), so none of them can disagree with the nav. With no
 * nav on screen (the phone's own screens) it is the chat list's filter
 * (workspace-state `projectFilterId`: one project, else every project).
 *
 * Memory only: what an empty URL starts from is the tab's default
 * (lib/urlState/shellScope.ts scopeDefault), written when a scope is
 * published, read through `seededScope()`.
 */

import { create } from "zustand";

import { chatListScope } from "@/lib/chatProjectFilter";
import { useWorkspaceStateStore } from "@/store/workspaceStateStore";

import { defaultsOwner, focusDefault, idsOfScope, sameScope, scopeDefault, type ShellScope } from "./shellScope";

interface PublishedScope {
  scope: ShellScope;
  /** Several or all: the catalog key the nav's app sections open. */
  focus: string | null;
}

export const useShellScopeStore = create<{ published: PublishedScope | null }>(() => ({ published: null }));

function filterScope(projectFilterId: string | null): ShellScope {
  return projectFilterId ? { mode: "one", projectId: projectFilterId } : { mode: "all" };
}

/** The scope the nav shows now, else the chat list's filter. */
export function shellScopeNow(): ShellScope {
  return useShellScopeStore.getState().published?.scope ?? filterScope(useWorkspaceStateStore.getState().projectFilterId);
}

/**
 * The nav publishes what it shows (null: it left the screen). A scope also
 * becomes this tab's default for an empty URL, and the seed of a new tab.
 */
export function publishShellScope(next: PublishedScope | null): void {
  const prev = useShellScopeStore.getState().published;
  const same = !!prev && !!next && sameScope(prev.scope, next.scope) && prev.focus === next.focus;
  if (!same && (prev || next)) useShellScopeStore.setState({ published: next });
  if (!next) return;
  // The tab's default as it was is read before this page writes over it, so
  // a later fill (seededScope) never takes the page's own first frame: the
  // desktop shell's, say, before a phone hands off to `/m/*`.
  seededScope();
  scopeDefault.set(next.scope);
  if (next.focus && next.scope.mode !== "one") focusDefault.set(next.focus);
}

/** The one project a scope names, else null (several or all). */
export function oneProjectOf(scope: ShellScope): string | null {
  return scope.mode === "one" ? scope.projectId : null;
}

/** The chat list cache scope of `scope` (lib/chatProjectFilter chatListScope): an id, several, or null for every project. */
export function chatScopeOf(scope: ShellScope): string | null {
  return chatListScope(idsOfScope(scope));
}

/** The one project the shell shows now, else null. */
export function shellProjectNow(): string | null {
  return oneProjectOf(shellScopeNow());
}

/** Re-renders with the one project the shell shows (null for several or all). */
export function useShellOneProject(): string | null {
  const published = useShellScopeStore((state) => state.published);
  const filter = useWorkspaceStateStore((state) => state.projectFilterId);
  return published ? oneProjectOf(published.scope) : filter;
}

/** Re-renders with the chat list cache scope the shell shows. */
export function useShellChatScope(): string | null {
  const published = useShellScopeStore((state) => state.published);
  const filter = useWorkspaceStateStore((state) => state.projectFilterId);
  return published ? chatScopeOf(published.scope) : filter;
}

// ── The default for an empty URL ────────────────────────────────────────────

let seed: { owner: string; scope: ShellScope | undefined; focus: string | undefined } | null = null;

/**
 * This tab's default as it was when the tab (or its user and org) started:
 * read once, before anything this page shows is published over it, so a
 * reload fills from the scope the tab had, not from the page's first frame.
 */
export function seededScope(): { scope: ShellScope | undefined; focus: string | undefined } {
  const owner = defaultsOwner();
  if (!owner) return { scope: undefined, focus: undefined };
  if (seed?.owner !== owner) seed = { owner, scope: scopeDefault.get(), focus: focusDefault.get() };
  return { scope: seed.scope, focus: seed.focus };
}

/** Tests: a tab that has published nothing and read no default yet. */
export function resetShellScopeForTests(): void {
  seed = null;
  useShellScopeStore.setState({ published: null });
}

/** What fills an empty URL, in order; `wait` until what decides it has loaded. */
export function fillScope({
  tabDefault,
  setting,
  listed,
  contextProjectId,
}: {
  /** This tab's own scope, else the last chosen in any tab (seededScope()). */
  tabDefault: ShellScope | undefined;
  /** The person's remembered choice of several (`sidebar.projects` filter), once read; undefined while loading. */
  setting: readonly string[] | undefined;
  /** The projects listed, once loaded; undefined while loading. */
  listed: ReadonlySet<string> | undefined;
  /** The project the workspace is in, once known (null: the caller shows it without a URL). */
  contextProjectId: string | null;
}): ShellScope | "wait" | null {
  if (tabDefault?.mode === "all") return tabDefault;
  if (tabDefault && listed) {
    // A project since removed is left out.
    const kept = idsOfScope(tabDefault).filter((id) => listed.has(id));
    if (kept.length === 1) return { mode: "one", projectId: kept[0] };
    if (kept.length >= 2) return { mode: "many", projectIds: kept };
  } else if (tabDefault) {
    return "wait";
  }
  if (setting === undefined || listed === undefined) return "wait";
  const remembered = setting.filter((id) => listed.has(id));
  if (remembered.length >= 2) return { mode: "many", projectIds: remembered };
  if (contextProjectId) return { mode: "one", projectId: contextProjectId };
  return null;
}
