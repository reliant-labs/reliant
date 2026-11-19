// Copyright (c) 2025 Reliant Labs

/**
 * THE CHAT SHELL FOLLOWS ITS URL (control-plane
 * docs/design/unified-shell-and-projects.md §9 R1–R3; lib/chatRoute.ts).
 *
 * Mounted once, by ModernApp, on every chat-shell page. For the page that
 * committed:
 *
 *   /c/<chatId>[/t/<threadId>]   opens that chat (its project and workspace
 *                                follow) when it is not the open one: a
 *                                direct load, a refresh, Back and Forward.
 *                                The thread is the path's; choosing another
 *                                navigates (push), so Back closes it.
 *   /new?project=&machine=&ws=   shows the new-chat view in that context
 *   / and /project/<id>          the workspace restore's chat, once it has
 *                                one: the URL is replaced with its `/c/<id>`
 *
 * Each runs when the URL changes, never when the store does: a chat the app
 * opened or closed by itself moves the URL instead (lib/chatUrlFollow.ts,
 * installed here), so the store never reopens what the person just archived.
 *
 * Two view states ride in the chat's search, both written with a replace
 * (they are how the page looks, not steps Back retraces) and both kept as
 * the person leaves them, so a refresh, or a Back to the chat, returns them:
 *
 *   panel  the right panel's open tab (absent: as the workspace had it)
 *   view   Detailed or Simplified (absent: the person's preference)
 */

import { useCallback, useEffect, useMemo, useRef } from "react";
import { useMatch, useRouter, useRouterState } from "@tanstack/react-router";

import { useChatViewOverride } from "@/components/Chat/thread-views/useChatThreadMode";
import { getChatFromCache } from "@/hooks/chat-queries";
import { chatShellRouteOf, latestPathname, navigateToChatUrl, patchNewChatUrl, type ChatShellRoute } from "@/lib/chatRoute";
import { takeMovedChatNotice, type ChatRouteData } from "@/lib/chatRouteResolve";
import { followActiveChat } from "@/lib/chatUrlFollow";
import { isElectron } from "@/lib/constants";
import { openChatInWorkspace } from "@/lib/openChatInWorkspace";
import { toast } from "@/lib/toast-manager";
import { CHAT_PANELS, type ChatPanel, type ChatView, type NewChatSearch } from "@/routeSchemas";
import { useChatStore } from "@/store/chatStore";
import { applyNewChat, isNewChatApplied, selectSharedChat } from "@/store/openChat";
import { useCurrentWorktreeState, useWorkspaceStateStore, type RightSidebarTab } from "@/store/workspaceStateStore";

// The panel values are the right panel's tabs, exactly.
const PANELS_ARE_TABS: readonly RightSidebarTab[] = CHAT_PANELS;
type TabsArePanels = RightSidebarTab extends ChatPanel ? true : never;
const TABS_ARE_PANELS: TabsArePanels = true;
void PANELS_ARE_TABS;
void TABS_ARE_PANELS;

export interface ChatRouteView {
  /** The chat-shell page committed; null on any other page. */
  route: ChatShellRoute | null;
  /** What the chat URL resolved to (lib/chatRouteResolve.ts); undefined off a chat's URL. */
  data: ChatRouteData | undefined;
  /** The thread the URL opens, for the chat it names. */
  thread: { chatId: string; threadId: string | null } | null;
  /** Open a thread of the URL's chat (null: all threads). A push: Back closes it. */
  onSelectThread: (threadId: string | null) => void;
}

export interface ChatRouteOptions {
  /** The shell can act: the backend is up and the workspace restore is done. */
  ready: boolean;
  /** The right panel's visibility, which ModernApp owns. */
  panelOpen: boolean;
  setPanelOpen: (open: boolean) => void;
  projectId: string | null;
  worktreeId: string | null;
}

interface MatchLike {
  id: string;
  pathname: string;
  search?: Record<string, unknown>;
  loaderData?: unknown;
}

interface NavigatingRouter {
  latestLocation: { pathname: string };
  state: { status: string };
  navigate: (options: { to: string; search: (prev: Record<string, unknown>) => Record<string, unknown>; replace: boolean }) => unknown;
}

/** Set (or drop, for undefined) one search key of the page on screen, with a replace. */
function replaceSearch(router: NavigatingRouter, key: string, value: string | undefined): void {
  void router.navigate({
    to: router.latestLocation.pathname,
    search: (prev) => {
      const next = { ...prev };
      if (value === undefined) delete next[key];
      else next[key] = value;
      return next;
    },
    replace: true,
  });
}

export function useChatRoute({ ready, panelOpen, setPanelOpen, projectId, worktreeId }: ChatRouteOptions): ChatRouteView {
  const router = useRouter() as unknown as NavigatingRouter;
  const match = useMatch({ strict: false }) as unknown as MatchLike;
  const route = useMemo(() => chatShellRouteOf(match.pathname), [match.pathname]);
  const search = match.search ?? {};
  const data = route?.kind === "chat" ? (match.loaderData as ChatRouteData | undefined) : undefined;
  const activeChatId = useChatStore((state) => state.activeChatId);
  // Each URL is acted on once, when it commits (or once the shell is ready for it).
  const locationKey = `${match.id}|${JSON.stringify(search)}`;

  // The URL follows what the app opens and closes by itself.
  useEffect(() => followActiveChat(router), [router]);

  // ── The chat ──────────────────────────────────────────────────────────────
  const routeChat = route?.kind === "chat" && data?.kind === "chat" && data.chat.id === route.chatId ? data.chat : null;
  const openedFor = useRef<string | null>(null);
  useEffect(() => {
    if (!routeChat) return;
    // A link into the wrong org was sent here: say so once, on the page it landed on.
    const notice = takeMovedChatNotice(routeChat.id);
    if (notice) toast.info(notice);
  }, [routeChat]);
  useEffect(() => {
    if (!ready || !routeChat || openedFor.current === match.id) return;
    openedFor.current = match.id;
    if (useChatStore.getState().activeChatId === routeChat.id) return;
    if (routeChat.viewerAccess) {
      selectSharedChat(routeChat);
      return;
    }
    openChatInWorkspace(routeChat, { navigate: true, openEntry: "deep_link" }).catch((error) => {
      void toast.error(error);
    });
  }, [ready, routeChat, match.id]);

  // ── The new-chat view ─────────────────────────────────────────────────────
  const newSearch: NewChatSearch | null =
    route?.kind === "new"
      ? {
          project: typeof search.project === "string" ? search.project : undefined,
          machine: typeof search.machine === "string" ? search.machine : undefined,
          ws: typeof search.ws === "string" ? search.ws : undefined,
        }
      : null;
  const appliedFor = useRef<string | null>(null);
  useEffect(() => {
    if (!ready || !newSearch || appliedFor.current === locationKey) return;
    appliedFor.current = locationKey;
    if (isNewChatApplied(newSearch)) return;
    applyNewChat({ projectId: newSearch.project, daemonId: newSearch.machine, worktreeId: newSearch.ws }).catch((error) => {
      void toast.error(error);
    });
    // newSearch is derived from locationKey.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ready, locationKey]);
  // The draft's project and workspace as the view shows them (the project
  // chip, the workspace picker), so a refresh keeps them.
  useEffect(() => {
    if (!ready || route?.kind !== "new" || activeChatId !== null || !projectId) return;
    if (newSearch?.project === projectId && (newSearch.ws ?? null) === worktreeId) return;
    patchNewChatUrl({ project: projectId, ws: worktreeId ?? undefined });
  }, [ready, route?.kind, activeChatId, projectId, worktreeId, newSearch?.project, newSearch?.ws]);

  // ── The home: the restored chat's URL ─────────────────────────────────────
  const routerStatus = useRouterState({ select: (state) => state.status });
  useEffect(() => {
    if (!ready || route?.kind !== "home" || !activeChatId) return;
    // A navigation on its way decides; the home's URL is only replaced in place.
    if (routerStatus === "pending" || chatShellRouteOf(latestPathname())?.kind !== "home") return;
    navigateToChatUrl(activeChatId, { orgId: getChatFromCache(activeChatId)?.orgId, replace: true });
  }, [ready, route?.kind, activeChatId, routerStatus]);

  // ── The thread ────────────────────────────────────────────────────────────
  const routeChatId = route?.kind === "chat" ? route.chatId : null;
  const routeThreadId = route?.kind === "chat" ? route.threadId : null;
  const thread = useMemo(() => (routeChatId ? { chatId: routeChatId, threadId: routeThreadId } : null), [routeChatId, routeThreadId]);
  const onSelectThread = useCallback(
    (threadId: string | null) => {
      if (!routeChatId) return;
      navigateToChatUrl(routeChatId, { orgId: getChatFromCache(routeChatId)?.orgId, threadId });
    },
    [routeChatId],
  );

  // ── View state: Detailed mode ─────────────────────────────────────────────
  const onChat = route?.kind === "chat";
  const urlView = onChat && (search.view === "detailed" || search.view === "simplified") ? (search.view as ChatView) : null;
  useEffect(() => {
    useChatViewOverride.setState({ routed: onChat, view: onChat ? urlView : null });
  }, [onChat, urlView]);
  useEffect(() => () => useChatViewOverride.setState({ routed: false, view: null }), []);
  const overrideView = useChatViewOverride((state) => state.view);
  const lastOverride = useRef(overrideView);
  useEffect(() => {
    // Only a change of the override is written (a toggle): the URL's own changes set it (above).
    if (lastOverride.current === overrideView) return;
    lastOverride.current = overrideView;
    if (!onChat || overrideView === urlView) return;
    replaceSearch(router, "view", overrideView ?? undefined);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [overrideView]);

  // ── View state: the right panel ───────────────────────────────────────────
  const tab = useCurrentWorktreeState(projectId, worktreeId).rightSidebarTab;
  const urlPanel = onChat && (CHAT_PANELS as readonly unknown[]).includes(search.panel) ? (search.panel as ChatPanel) : undefined;
  // The chat's workspace is the one on screen: what the panel shows is its.
  const inChatContext = onChat && !!routeChat && activeChatId === routeChat.id && !!projectId;
  const panelAppliedFor = useRef<string | null>(null);
  // Set while the panel the URL named is being applied: the mirror waits for it to render.
  const panelApplying = useRef(false);
  useEffect(() => {
    if (!inChatContext || panelAppliedFor.current === match.id) return;
    panelAppliedFor.current = match.id;
    // The in-app browser exists only in the desktop app.
    if (!urlPanel || (urlPanel === "browser" && !isElectron())) return;
    if (panelOpen && tab === urlPanel) return;
    panelApplying.current = true;
    if (!panelOpen) setPanelOpen(true);
    if (tab !== urlPanel && projectId) useWorkspaceStateStore.getState().setRightSidebarTab(projectId, worktreeId, urlPanel);
    // Applied once per chat page; the mirror below keeps the URL after it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [inChatContext, match.id]);
  useEffect(() => {
    if (!inChatContext || panelAppliedFor.current !== match.id) return;
    if (panelApplying.current) {
      panelApplying.current = false;
      return;
    }
    const shown: ChatPanel | undefined = panelOpen ? tab : undefined;
    if (shown !== urlPanel) replaceSearch(router, "panel", shown);
  }, [inChatContext, match.id, panelOpen, tab, urlPanel, router]);

  return { route, data, thread, onSelectThread };
}
