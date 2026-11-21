// Copyright (c) 2025 Reliant Labs

/**
 * Open a chat in the main pane, from any project.
 *
 * The one path every "open this chat" goes through — a sidebar row, the tray,
 * a run, a notification — so the context (project, workspace, viewers) always
 * follows the chat being opened. The sidebar does not change: it spans every
 * project, so opening a chat of another project never unmounts the list or
 * the activity of the chats running elsewhere.
 */

import type { Chat } from "../api/client";
import { beginWorkspaceOpen, findProject, openChatInWorkspace, ChatProjectUnavailableError } from "../lib/openChatInWorkspace";
export { findProject, ChatProjectUnavailableError } from "../lib/openChatInWorkspace";
import { getChatFromCache, removeChatFromListCache, seedChatDetail } from "../hooks/chat-queries";
import { sharingServiceKeys } from "../hooks/sharing-service-hooks_gen";
import { queryClient } from "../lib/query-client";
import { toast } from "../lib/toast-manager";
import { orgKeys } from "../services/controlPlane/orgs";
import { resolveMainWorktree, resolveWorktree } from "../hooks/worktree-queries";
import { useChatStore } from "./chatStore";
import { useChatNavigationStore } from "./chatNavigationStore";
import { useProjectStore } from "./projectStore";
import { useWorkspaceStateStore } from "./workspaceStateStore";
import { useWorktreeStore } from "./worktreeStore";
import { useNewChatMachineStore } from "./newChatMachineStore";
import { useNewChatAppStore } from "./newChatAppStore";
import { markNewChatApplied, navigateToChatUrl, navigateToNewChatUrl, newChatAppliedKey, newChatTargetKey } from "../lib/chatRoute";
import type { NewChatSearch } from "../routeSchemas";


/**
 * Open a chat: go to its URL (`/c/<id>`, a push, so Back returns to what was
 * on screen), and select it at once, then follow its project/workspace with
 * main's identity fence. All source entry points share the same epoch as the
 * main helper.
 *
 * The URL is the authority (lib/chatRoute.ts): the route would open the chat
 * by itself once it commits (hooks/useChatRoute.ts). Selecting here as well
 * keeps a click's stream request in the click's own task, as before chats
 * had URLs; the route then finds the chat already open.
 */
export function openChat(chat: Chat, isCurrent?: () => boolean): Promise<void> {
  if (isCurrent && !isCurrent()) return Promise.resolve();
  seedChatDetail(chat);
  navigateToChatUrl(chat.id, { orgId: chat.orgId });
  return openChatInWorkspace(chat, { navigate: true, isCurrent });
}

/**
 * Open a chat another member shared with the org, read-only, in the main
 * pane. Its project and workspace are its owner's, not the viewer's, so
 * neither context follows it (openChat would refuse: the project is not one
 * of theirs). It is not remembered as the context project's last chat either.
 */
export function openSharedChat(chat: Chat): void {
  seedChatDetail(chat);
  navigateToChatUrl(chat.id, { orgId: chat.orgId });
  selectSharedChat(chat);
}

/** Show a shared chat (openSharedChat) without touching the URL: the route's own open. */
export function selectSharedChat(chat: Chat): void {
  beginWorkspaceOpen();
  seedChatDetail(chat);
  // Shared again after the viewer lost it: this open is the person asking,
  // so the stream asks the server again (it remembers refusals otherwise).
  useChatStore.getState().selectChat(chat, { deferPersist: true, retryRefused: true });
  useChatNavigationStore.getState().navigateToChat(chat.id);
}

/**
 * The server stopped streaming a chat to this person (StreamChatUpdates
 * refused it). For another member's shared chat that means the viewer lost
 * it — made private, deleted, or they left the org — and the server re-checks
 * on an interval, so this is when an open view learns it. Close the view,
 * drop what was cached of the chat, and say why. Returns whether it did: a
 * chat of the person's own is not this function's to close.
 */
export function leaveSharedChat(chatId: string): boolean {
  const chat = getChatFromCache(chatId);
  if (!chat?.viewerAccess) return false;
  const chatStore = useChatStore.getState();
  if (chatStore.activeChatId === chatId) {
    chatStore.clearCurrentChat(null);
    toast.info("This chat is no longer shared with you.");
  }
  void useChatNavigationStore.getState().removeFromQueue(chatId);
  chatStore.evictChat(chatId);
  removeChatFromListCache(chat.projectId, chatId);
  void queryClient.invalidateQueries({ queryKey: sharingServiceKeys.all });
  // Losing it may mean losing the org: re-read membership, so a person
  // removed from it stops seeing the org and what it shared.
  void queryClient.invalidateQueries({ queryKey: orgKeys.mine });
  return true;
}

/**
 * The project a global "New chat" starts in: the sidebar's filter when it
 * names one, else the open chat's project (the context project), else the
 * last one used.
 */
export function newChatTargetProjectId(): string | null {
  const filter = useWorkspaceStateStore.getState().projectFilterId;
  if (filter) return filter;
  const context = useProjectStore.getState().currentProject?.id;
  if (context) return context;
  return useWorkspaceStateStore.getState().lastProjectId;
}

/** Where a new chat starts: its project, workspace and machine, and the app it is about. */
export interface NewChatTarget {
  projectId?: string | null;
  worktreeId?: string | null;
  daemonId?: string | null;
  /** The app the chat is started from (Chat.app_name, newChatAppStore); none for every other new chat. */
  appName?: string | null;
}

/** The new-chat route's search for `target` (`/new?project=&machine=&ws=&app=`). */
export function newChatSearchOf(target: NewChatTarget): NewChatSearch {
  return {
    project: target.projectId ?? undefined,
    machine: target.daemonId ?? undefined,
    ws: target.worktreeId ?? undefined,
    app: target.appName ?? undefined,
  };
}

/** Whether the new-chat view already shows `search`'s draft context (the `/new` route applies each one once). */
export function isNewChatApplied(search: NewChatSearch): boolean {
  return newChatAppliedKey() === newChatTargetKey(search) && useChatStore.getState().activeChatId === null;
}

/**
 * Show the new-chat view, in `projectId` (default: newChatTargetProjectId)
 * and the workspace `worktreeId` (default: the open chat's, when it is in that
 * project; else the project's main workspace). `daemonId` names the machine
 * the chat should run on (newChatMachineStore); without it the view picks.
 * `appName` tags the chat with the app it is started from (newChatAppStore);
 * every new chat opened without one carries none, whatever was set before.
 *
 * Goes to `/new` with that context in its search (a push from a chat, a
 * replace when the new-chat view is already on screen), so a refresh keeps
 * the draft's project, machine and workspace, and applies it at once.
 */
export function openNewChat(target: NewChatTarget = {}): Promise<void> {
  const search = newChatSearchOf({ ...target, projectId: target.projectId ?? newChatTargetProjectId() });
  navigateToNewChatUrl(search);
  return applyNewChat(target);
}

/**
 * Show the new-chat view for `target` without touching the URL: the `/new`
 * route's own apply. Records the context as applied at once, with when it
 * is on screen (lib/chatRoute.ts newChatAppliedShown), so the route neither
 * applies it twice nor mirrors the view's old context over it meanwhile.
 */
export function applyNewChat(target: NewChatTarget = {}): Promise<void> {
  let settle: (shown: boolean) => void = () => {};
  const shown = new Promise<boolean>((resolve) => {
    settle = resolve;
  });
  markNewChatApplied(newChatSearchOf({ ...target, projectId: target.projectId ?? newChatTargetProjectId() }), shown);
  const done = showNewChat(target);
  done.then(
    () => settle(true),
    () => settle(false),
  );
  return done;
}

async function showNewChat(target: NewChatTarget): Promise<void> {
  const isLatest = beginWorkspaceOpen();
  const projectId = target.projectId ?? newChatTargetProjectId();
  if (projectId && target.daemonId) {
    useNewChatMachineStore.getState().offer({ projectId, daemonId: target.daemonId });
  }
  useNewChatAppStore.getState().set(projectId && target.appName ? { projectId, appName: target.appName } : null);
  const projectStore = useProjectStore.getState();
  let contextProjectId = projectStore.currentProject?.id;
  const activeChatId = useChatStore.getState().activeChatId;
  const isCurrent = () => isLatest() &&
    useChatStore.getState().activeChatId === activeChatId &&
    useProjectStore.getState().currentProject?.id === contextProjectId;
  const switching = !!projectId && contextProjectId !== projectId;
  if (switching) {
    const project = await findProject(projectId);
    if (!isCurrent()) return;
    if (!project) throw new ChatProjectUnavailableError();
    const selected = useProjectStore.getState().selectProject(project, {
      restoreChat: false,
      restoreWorkspace: false,
      isCurrent: isLatest,
    });
    contextProjectId = project.id;
    await selected;
    if (!isCurrent()) return;
  }
  if (target.worktreeId && contextProjectId) {
    const worktree = await resolveWorktree(target.worktreeId);
    if (!isCurrent()) return;
    if (worktree) {
      await useWorktreeStore.getState().switchWorktreeContext(contextProjectId, worktree, { isCurrent });
    }
    if (!isCurrent()) return;
    useChatStore.getState().clearCurrentChat(target.worktreeId);
    return;
  }

  if (switching) {
    // A new project starts on its main workspace, not on whichever workspace
    // the last chat there used.
    const main = contextProjectId ? await resolveMainWorktree(contextProjectId) : null;
    if (!isCurrent()) return;
    if (main && contextProjectId) {
      await useWorktreeStore.getState().switchWorktreeContext(contextProjectId, main, { isCurrent });
    }
    if (!isCurrent()) return;
    useChatStore.getState().clearCurrentChat(main?.id ?? null);
    return;
  }

  // Same project: keep the open chat's workspace.
  const chatStore = useChatStore.getState();
  const activeChat = chatStore.activeChatId ? getChatFromCache(chatStore.activeChatId) : undefined;
  const worktreeId = activeChat?.worktreeId || useWorktreeStore.getState().currentWorktreeId || null;
  chatStore.clearCurrentChat(worktreeId);
}
