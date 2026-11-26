// Copyright (c) 2026 Reliant Labs

/**
 * The phone's URL → store bridge, mounted once by MobileShell: its
 * ProjectFilterRouteSync (owner, 2026-11-24: the URL is the only source of
 * view state; the stores follow it, and a pick only navigates).
 *
 * - **Fills an empty URL**, with a replace (not a step Back retraces): a bare
 *   chat list or search gets the scope this surface last showed, else this
 *   tab's scope as the page loaded (seededScope; `?projects=`, else `all`),
 *   as the desktop's first bare home does (fillScope), and a bare
 *   `/m/new` names the project it opens on (`?project=`), so Back after a
 *   pick returns to it.
 * - **Publishes the URL's scope** as the shell's (shellScopeStore): the chat
 *   store's list load, a new chat's and search's readers outside React take
 *   it, and it becomes this tab's default for an empty URL.
 * - **Reloads the chat store's list** when the scope changes.
 * - **Moves the context project** (`projectStore.currentProject`, what the
 *   drawer names and the workspaces follow) to the project the URL names:
 *   `/m/new`'s, else the scope's one. Never on a chat's own page, whose chat
 *   decides (MobileChatScreen).
 */

import { useEffect, useMemo, useRef } from "react";
import { useNavigate, useRouterState } from "@tanstack/react-router";

import { useProjects } from "@/hooks/project-queries";
import { logger } from "@/lib/logger";
import { mobileChatRouteOf } from "@/lib/chatRoute";
import { orgFreePath } from "@/lib/paths/org";
import { mobileChatsPath, mobileNewChatPath, mobileSearchPath } from "@/lib/paths";
import type { SearchRecord } from "@/lib/urlState/searchParams";
import { projectsParam, scopeOfSearch, type ShellScope } from "@/lib/urlState/shellScope";
import { chatScopeOf, fillScope, oneProjectOf, publishShellScope, seededScope } from "@/lib/urlState/shellScopeStore";
import { useChatStore } from "@/store/chatStore";
import { useProjectStore } from "@/store/projectStore";
import { shellScopeSearch } from "../Layout/shellScopeNav";

import { newChatProjectOf, newChatProjectParam } from "./mobileScope";

/** The screens whose content is the scope's: a bare URL of one is filled. */
const SCOPED_SCREENS = new Set([mobileChatsPath(), mobileSearchPath()]);

export function MobileScopeSync() {
  const navigate = useNavigate();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const search = useRouterState({ select: (s) => s.location.search }) as SearchRecord;
  const path = orgFreePath(pathname).replace(/\/+$/, "") || "/";
  const { projects, isLoading } = useProjects();
  const contextProjectId = useProjectStore((s) => s.currentProject?.id ?? null);

  const urlScope = scopeOfSearch(search);
  const scopeKey = urlScope ? (projectsParam.serialize(urlScope) ?? null) : null;
  const scopeProjectId = urlScope ? oneProjectOf(urlScope) : null;
  const projectParam = newChatProjectParam(search);
  const onNewChat = path === mobileNewChatPath();
  const onChat = mobileChatRouteOf(path)?.kind === "chat";

  const listedKey = isLoading ? null : projects.map((p) => p.id).join(",");
  const listed = useMemo(() => (listedKey === null ? undefined : new Set(listedKey.split(",").filter(Boolean))), [listedKey]);

  // The scope this surface last showed: what a later empty URL is filled from.
  const shown = useRef<ShellScope | undefined>(undefined);

  // An empty URL is filled from what the page shows without it (a replace):
  // the scope this surface last showed, else the tab's default as it was when
  // the page loaded (seededScope), never the live default. A phone's first
  // frame can be the desktop shell's, whose nav publishes its one project
  // there before the hand-off to `/m/*`; how long that frame lasts depends
  // on the reads the hand-off waits for, so reading it raced them.
  useEffect(() => {
    if (SCOPED_SCREENS.has(path) && scopeKey === null) {
      const tabDefault = shown.current ?? seededScope().scope;
      const filled = fillScope({ tabDefault, setting: [], listed, contextProjectId: null });
      if (filled === "wait") return;
      const scope = shellScopeSearch(filled ?? { mode: "all" });
      void navigate({ to: pathname, search: ((prev: SearchRecord) => ({ ...prev, ...scope })) as never, replace: true });
      return;
    }
    if (onNewChat && !projectParam && listed) {
      const project = newChatProjectOf({ project: undefined, scope: urlScope, contextProjectId, listed });
      if (!project) return;
      void navigate({ to: pathname, search: ((prev: SearchRecord) => ({ ...prev, project })) as never, replace: true });
    }
    // urlScope is rebuilt every render; scopeKey is what changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path, pathname, scopeKey, onNewChat, projectParam, listed, contextProjectId, navigate]);

  // The URL's scope is the shell's, and this tab's default for an empty URL.
  useEffect(() => {
    const scope = scopeKey === null ? undefined : projectsParam.parse(scopeKey);
    if (scope) shown.current = scope;
    publishShellScope(scope ? { scope, focus: null } : null);
  }, [scopeKey]);
  useEffect(() => () => publishShellScope(null), []);

  // The chat store's list follows the scope (the first load is MobileShell's).
  const chatScope = urlScope ? chatScopeOf(urlScope) : null;
  const loadedScope = useRef<string | null | undefined>(undefined);
  useEffect(() => {
    const previous = loadedScope.current;
    loadedScope.current = chatScope;
    if (previous === undefined || previous === chatScope) return;
    void useChatStore.getState().loadChats();
  }, [chatScope]);

  // The context project follows the project the URL names.
  const target = onChat ? null : onNewChat ? (newChatProjectOf({ project: projectParam, scope: urlScope, contextProjectId: null, listed }) ?? null) : scopeProjectId;
  useEffect(() => {
    if (!target || target === contextProjectId) return;
    const project = projects.find((p) => p.id === target);
    if (!project) return;
    useProjectStore
      .getState()
      .selectProject(project, { restoreWorkspace: false })
      .catch((error: unknown) => logger.warn("[MobileScopeSync] Project switch failed", { projectId: target, error }));
  }, [target, contextProjectId, projects]);

  return null;
}
