// Copyright (c) 2025 Reliant Labs

/**
 * The workspace lifecycle: create, import, archive, permanently delete,
 * restore (unarchive, or recreate from its branch when its files are gone),
 * plus selecting a project's workspace at start-up.
 *
 * Each action is a WorktreeService write through `connectClient` followed by
 * the cache work that makes every reader see it — the row the server returned
 * goes into its GetWorktree entry, and the lists are patched and invalidated
 * (hooks/worktree-queries.ts). The other half of an action is the app state
 * around the workspace: the selection, the active chat that lived in it, its
 * saved panel state and browser tabs. That is why these are functions rather
 * than the generated mutation hooks: the sidebar, the stream and modals all
 * call them, several from outside React.
 */

import { ConnectError } from "@connectrpc/connect";
import type { QueryClient } from "@tanstack/react-query";

import { WorktreeService } from "../gen/reliant/v1/worktree_pb";
import { connectClient } from "../lib/connect";
import { retryAcrossWake } from "../lib/daemon-retry";
import { logger } from "../lib/logger";
import { beginWorkspaceOpen } from "../lib/openChatInWorkspace";
import { queryClient as appQueryClient } from "../lib/query-client";
import { toast } from "../lib/toast-manager";
import { useBrowserStore } from "../store/browserStore";
import { useChatNavigationStore } from "../store/chatNavigationStore";
import { useChatStore } from "../store/chatStore";
import { useWorkspaceStateStore } from "../store/workspaceStateStore";
import { useWorktreeStore } from "../store/worktreeStore";
import { getChatFromCache } from "./chat-queries";
import {
  announceCreated,
  announceCreatePending,
  cacheWorktreeRow,
  fetchWorktree,
  getCachedWorktree,
  invalidateWorktreeQueries,
  isWorktreeKnownGone,
  removeWorktreeFromLists,
  resolveMainWorktree,
  resolveWorktree,
  staleFromProto,
  worktreeFromProto,
  WorktreeStatus,
  type CreateWorktreeInput,
  type StaleWorktree,
  type Worktree,
} from "./worktree-queries";
import { worktreeServiceKeys } from "./worktree-service-hooks_gen";
import { inboxServiceKeys } from "./inbox-service-hooks_gen";

const client = connectClient(WorktreeService);

/** Durable Git recovery is NOT inbox undismiss or original-branch recreation. */
export async function restoreWorktreeRecovery(recoveryId: string, targetDaemonId: string, idempotencyKey: string) {
  const response = await client.restoreWorktreeRecovery({ recoveryId, targetDaemonId, idempotencyKey });
  await Promise.all([
    invalidateWorktreeQueries(appQueryClient),
    appQueryClient.invalidateQueries({ queryKey: inboxServiceKeys.all }),
  ]);
  return response;
}

/** Only this explicit action deletes the exact retained recovery branches. */
export async function deleteWorktreeRecovery(recoveryId: string, idempotencyKey: string) {
  const response = await client.deleteWorktreeRecovery({ recoveryId, idempotencyKey });
  await Promise.all([
    appQueryClient.invalidateQueries({ queryKey: worktreeServiceKeys.all }),
    appQueryClient.invalidateQueries({ queryKey: inboxServiceKeys.all }),
  ]);
  return response;
}

/**
 * Said once when a workspace action found its machine asleep: the server woke
 * it, and the action finishes by itself once the machine is back, so this is
 * news rather than an error.
 */
function announceMachineWaking() {
  toast.info("Waking up your machine — this will finish on its own.");
}

// Extract error message from various error types
function getErrorMessage(error: unknown, defaultMsg: string): string {
  if (error instanceof ConnectError) return error.message;
  if (error instanceof Error) return error.message;
  return defaultMsg;
}

function cleanupSuffix(deleteGitBranch?: boolean): string {
  return deleteGitBranch ? " (branch will be deleted once its files are removed)" : "";
}

// ── The active chat of an archived workspace ────────────────────────────────

type ArchivedWorktreeActiveChatSnapshot = {
  activeChatId: string | null;
  activeChatWasInWorktree: boolean;
};

function getArchivedWorktreeActiveChatSnapshot(worktreeId: string): ArchivedWorktreeActiveChatSnapshot {
  const activeChatId = useChatStore.getState().activeChatId;
  const activeChat = activeChatId ? getChatFromCache(activeChatId) : null;
  return {
    activeChatId,
    activeChatWasInWorktree: activeChat?.worktreeId === worktreeId,
  };
}

async function clearArchivedWorktreeActiveChat(
  worktreeId: string,
  snapshot: ArchivedWorktreeActiveChatSnapshot,
  options?: { clearMissingChat?: boolean },
): Promise<void> {
  const { activeChatId, activeChatWasInWorktree } = snapshot;
  if (!activeChatId) return;

  const chatStore = useChatStore.getState();
  if (chatStore.activeChatId !== activeChatId) return;

  const activeChat = getChatFromCache(activeChatId);
  const shouldClearActiveChat =
    activeChatWasInWorktree ||
    activeChat?.worktreeId === worktreeId ||
    (options?.clearMissingChat && !activeChat);

  if (!shouldClearActiveChat) return;

  logger.info("[Worktrees] Active chat was archived, clearing it", {
    activeChatId,
    worktreeId,
    activeChatStillLoaded: Boolean(activeChat),
  });

  chatStore.clearCurrentChat(worktreeId);
  await useChatNavigationStore.getState().removeFromQueue(activeChatId);
}

/** Leave a workspace that is no longer usable: select the project's main one. */
async function leaveForMain(projectId: string, worktreeId: string, openFreshNewChat: boolean, qc: QueryClient) {
  const mainWorktree = await resolveMainWorktree(projectId, qc);
  if (mainWorktree) {
    logger.info("[Worktrees] Switching to main worktree after leaving current", {
      leftWorktreeId: worktreeId,
      mainWorktreeId: mainWorktree.id,
      openFreshNewChat,
    });
    await useWorktreeStore.getState().switchWorktreeContext(projectId, mainWorktree, { openFreshNewChat });
  } else {
    // No main worktree found, just clear selection
    useWorktreeStore.getState().selectWorktree(null, { skipWorkspaceStateSave: true });
  }
}

/** Forget a workspace's saved panel state and its browser tabs. */
function forgetWorktreeState(projectId: string, worktreeId: string) {
  useWorkspaceStateStore.getState().removeWorktreeState(projectId, worktreeId);
  useBrowserStore.getState().closeWorktreeTabs(worktreeId);
}

/**
 * Re-read a workspace that moved between the live and archived lists: its own
 * row (by id, so every reader sees the move whether or not a list is mounted)
 * and every list.
 */
async function refreshAfterArchiveChange(worktreeId: string, qc: QueryClient): Promise<void> {
  await Promise.all([
    fetchWorktree(worktreeId, { fresh: true, qc }).catch((error) => {
      logger.warn("[Worktrees] Could not re-read worktree after archive change", { worktreeId, error });
    }),
    invalidateWorktreeQueries(qc, worktreeId),
  ]);
}

// ── Actions ─────────────────────────────────────────────────────────────────

/**
 * Create a workspace and select it. A worktree always spans every nested repo
 * in the project; base_branches pins a base branch per repo (key = repo_id),
 * falling back to base_branch and then each repo's default branch.
 *
 * The row comes back CREATING with no path; the stream's settle event fills it
 * in (worktree-queries' handleWorktreeChanges) and replaces the pending toast.
 */
export async function createWorktree(data: CreateWorktreeInput, qc: QueryClient = appQueryClient): Promise<Worktree> {
  try {
    const { project_id: projectId, name, branch } = data;
    if (!projectId || !name || !branch) {
      throw new Error("project_id, name, and branch are required");
    }

    // The server refuses to record a workspace on an asleep machine; it
    // wakes the machine instead, and this retries once it is up.
    const response = await retryAcrossWake(
      () =>
        client.createWorktree({
          projectId,
          name,
          branch,
          baseBranch: data.base_branch,
          baseBranches: data.base_branches ?? {},
          chatId: data.chat_id,
          copyFiles: data.copy_files ?? [],
          force: data.force ?? false,
          sourceWorktreeId: data.source_worktree_id,
        }),
      { onWaking: announceMachineWaking },
    );
    if (!response.worktree) throw new Error("No worktree in response");

    cacheWorktreeRow(response.worktree, qc);
    void invalidateWorktreeQueries(qc, response.worktree.id);
    const worktree = worktreeFromProto(response.worktree);
    useWorktreeStore.getState().selectWorktree(worktree);

    // The create settles in the background: say so now, and let the settle
    // (ACTIVE or FAILED) replace this toast. A create that comes back
    // already ACTIVE has nothing left to wait for.
    if (worktree.status === WorktreeStatus.CREATING) {
      announceCreatePending(worktree);
    } else if (worktree.status === WorktreeStatus.ACTIVE) {
      announceCreated(worktree);
    }

    return worktree;
  } catch (error) {
    throw new Error(getErrorMessage(error, "Failed to create workspace"));
  }
}

/** Track a git checkout Reliant did not make, and select it. */
export async function importWorktree(
  data: { path: string; name?: string; project_id: string; repo_id: string; confirm_move: boolean },
  qc: QueryClient = appQueryClient,
): Promise<Worktree> {
  try {
    const response = await client.importWorktree({
      projectId: data.project_id,
      path: data.path,
      name: data.name,
      repoId: data.repo_id,
      confirmMove: data.confirm_move,
    });
    if (!response.worktree) throw new Error("No worktree in response");

    cacheWorktreeRow(response.worktree, qc);
    // Every list, and discovery: the checkout is no longer an outside one.
    void qc.invalidateQueries({ queryKey: worktreeServiceKeys.all });
    const worktree = worktreeFromProto(response.worktree);
    useWorktreeStore.getState().selectWorktree(worktree);

    toast.success(`Workspace "${worktree.name}" imported successfully`, { duration: 4000 });
    return worktree;
  } catch (error) {
    const errorMessage = getErrorMessage(error, "Failed to import workspace");
    toast.error(errorMessage);
    throw new Error(errorMessage);
  }
}

/** Remove git's records of worktrees whose directories are gone. */
export async function pruneWorktrees(projectId: string, qc: QueryClient = appQueryClient): Promise<StaleWorktree[]> {
  const response = await client.pruneWorktrees({ projectId });
  void qc.invalidateQueries({ queryKey: worktreeServiceKeys.all });
  return response.pruned.map(staleFromProto);
}

/**
 * ONLY archives (sets deleted_at). Never permanently deletes. Optional
 * cleanup: delete the git branch once the machine removes the files.
 */
export async function archiveWorktree(
  id: string,
  options?: { deleteGitBranch?: boolean },
  qc: QueryClient = appQueryClient,
): Promise<void> {
  const store = useWorktreeStore.getState();
  store.setDeletingId(id);
  try {
    // Read the workspace BEFORE archiving it.
    const worktree = getCachedWorktree(id, qc);
    const worktreeName = worktree?.name || "Workspace";
    const wasCurrentWorktree = useWorktreeStore.getState().currentWorktreeId === id;
    const activeChatSnapshot = getArchivedWorktreeActiveChatSnapshot(id);

    await retryAcrossWake(() => client.archiveWorktree({ worktreeId: id, deleteGitBranch: options?.deleteGitBranch ?? false }), {
      onWaking: announceMachineWaking,
    });

    await clearArchivedWorktreeActiveChat(id, activeChatSnapshot);

    removeWorktreeFromLists(qc, id, "live");
    await refreshAfterArchiveChange(id, qc);

    const currentProject = worktree?.project_id;
    if (currentProject) {
      // Reload chats to remove archived ones from the list
      await useChatStore.getState().loadChats();
      await clearArchivedWorktreeActiveChat(id, activeChatSnapshot, { clearMissingChat: true });

      forgetWorktreeState(currentProject, id);
      logger.info("[Worktrees] Cleaned up workspace state and browser tabs for archived worktree", { worktreeId: id });

      // If we archived the currently selected worktree, switch to main worktree
      if (wasCurrentWorktree) await leaveForMain(currentProject, id, true, qc);
    }

    toast.success(`${worktreeName} archived${cleanupSuffix(options?.deleteGitBranch)}`, { duration: 4000 });
  } catch (error) {
    toast.error(getErrorMessage(error, "Failed to archive workspace"));
  } finally {
    useWorktreeStore.getState().setDeletingId(null);
  }
}

/**
 * Archives a live workspace, or permanently deletes an archived one (the
 * server deletes the row only once its machine removed the directory).
 */
export async function deleteWorktree(
  id: string,
  options?: { deleteGitBranch?: boolean },
  qc: QueryClient = appQueryClient,
): Promise<void> {
  useWorktreeStore.getState().setDeletingId(id);
  try {
    // Read the workspace BEFORE deleting it.
    const worktree = getCachedWorktree(id, qc);
    const isPermanentDelete = worktree?.deleted_at !== null && worktree?.deleted_at !== undefined;
    const worktreeName = worktree?.name || "Workspace";
    const wasCurrentWorktree = useWorktreeStore.getState().currentWorktreeId === id;
    const currentProject = worktree?.project_id;
    const activeChatSnapshot = getArchivedWorktreeActiveChatSnapshot(id);

    await retryAcrossWake(
      () => client.deleteWorktree({ worktreeId: id, deleteGitBranch: options?.deleteGitBranch ?? false }),
      { onWaking: announceMachineWaking },
    );

    if (isPermanentDelete) {
      removeWorktreeFromLists(qc, id, "all");
      qc.removeQueries({
        queryKey: worktreeServiceKeys.all,
        predicate: (query) =>
          query.queryKey.some(
            (part) => typeof part === "object" && part !== null && (part as { worktreeId?: string }).worktreeId === id,
          ),
      });
      void invalidateWorktreeQueries(qc, id);
    } else {
      await clearArchivedWorktreeActiveChat(id, activeChatSnapshot);
      removeWorktreeFromLists(qc, id, "live");
    }

    toast.success(
      `${worktreeName} ${isPermanentDelete ? "deleted permanently" : "archived"}${cleanupSuffix(options?.deleteGitBranch)}`,
      { duration: 4000 },
    );

    // Clean up workspace state and browser tabs (for both archive and permanent delete)
    if (currentProject) {
      forgetWorktreeState(currentProject, id);
      logger.info("[Worktrees] Cleaned up workspace state and browser tabs for deleted worktree", {
        worktreeId: id,
        isPermanentDelete,
      });
    }

    // An archive (not a permanent delete) moved the workspace between lists.
    if (!isPermanentDelete) {
      await refreshAfterArchiveChange(id, qc);
      if (currentProject) {
        // Reload chats to remove archived ones from the list
        await useChatStore.getState().loadChats();
        await clearArchivedWorktreeActiveChat(id, activeChatSnapshot, { clearMissingChat: true });
      }
    }

    // If we deleted/archived the currently selected worktree, switch to main worktree
    if (wasCurrentWorktree && currentProject) {
      await leaveForMain(currentProject, id, !isPermanentDelete, qc);
    }
  } catch (error) {
    toast.error(getErrorMessage(error, "Failed to delete workspace"));
  } finally {
    useWorktreeStore.getState().setDeletingId(null);
  }
}

/**
 * Bring an archived workspace back. One whose files were removed has nothing
 * to unarchive: it is rebuilt from its branch (RecreateWorktree), which also
 * puts back any work saved at removal. Throws on failure, after a toast.
 */
export async function unarchiveWorktree(id: string, qc: QueryClient = appQueryClient): Promise<void> {
  try {
    const worktree = getCachedWorktree(id, qc);
    const worktreeName = worktree?.name || "Workspace";

    let snapshotWarning = "";
    if (worktree?.cleanup_metadata?.directory_deleted) {
      snapshotWarning = (await client.recreateWorktree({ worktreeId: id })).snapshotWarning;
    } else {
      await client.unarchiveWorktree({ worktreeId: id });
    }

    removeWorktreeFromLists(qc, id, "all");
    await refreshAfterArchiveChange(id, qc);
    if (worktree?.project_id) {
      // Reload chats to bring back unarchived ones
      await useChatStore.getState().loadChats();
    }

    if (snapshotWarning) {
      toast.warning(`${worktreeName} restored, but its saved work was not applied. ${snapshotWarning}`, {
        duration: 15000,
      });
    } else {
      toast.success(`${worktreeName} restored`, { duration: 4000 });
    }
  } catch (error) {
    toast.error(getErrorMessage(error, "Failed to unarchive workspace"));
    throw error;
  }
}

/**
 * Rebuild a live workspace's directory from its branch when it has gone
 * missing from disk. The server asks the machine that owns it; a workspace
 * that is still there is left untouched.
 */
export async function recreateWorktree(id: string, qc: QueryClient = appQueryClient) {
  const response = await client.recreateWorktree({ worktreeId: id });
  await refreshAfterArchiveChange(id, qc);
  return response;
}

export async function updateWorktreeStatus(
  id: string,
  status: WorktreeStatus,
  qc: QueryClient = appQueryClient,
): Promise<void> {
  try {
    const response = await client.updateWorktree({ worktreeId: id, status });
    if (response.worktree) cacheWorktreeRow(response.worktree, qc);
    void invalidateWorktreeQueries(qc, id);
  } catch (error) {
    logger.warn("[Worktrees] Could not update workspace status", { id, status, error });
  }
}

// ── Selection at start-up and project switches ─────────────────────────────

/**
 * Fetch just the project's main workspace and select it when nothing is
 * selected. What app start and project switches use: one cheap row, however
 * many workspaces the project holds.
 */
export async function loadMainWorktree(projectId: string | undefined, qc: QueryClient = appQueryClient): Promise<void> {
  if (!projectId) return;
  const isCurrent = beginWorkspaceOpen();
  const main = await resolveMainWorktree(projectId, qc);
  if (!isCurrent() || !main || useWorktreeStore.getState().currentWorktreeId) return;
  logger.info("[Worktrees] Auto-selecting main worktree after load", { worktreeId: main.id, name: main.name });
  useWorktreeStore.getState().selectWorktree(main, { skipWorkspaceStateSave: true });
}

/**
 * Select the project's last-used workspace, or main when none was saved (or
 * the saved one is gone, archived, or another project's). Fetches a workspace
 * the cache does not hold by id; resolves false when nothing could be selected.
 */
export async function restoreLastWorktree(
  projectId: string,
  qc: QueryClient = appQueryClient,
  options?: { isCurrent?: () => boolean },
): Promise<boolean> {
  const isCurrent = options?.isCurrent ?? beginWorkspaceOpen();
  if (!isCurrent()) return false;
  const workspaceState = useWorkspaceStateStore.getState();
  const lastWorktreeId = workspaceState.getProjectState(projectId).lastWorktreeId;

  const selectMainWorktree = async () => {
    const mainWorktree = await resolveMainWorktree(projectId, qc);
    if (!isCurrent()) return false;
    if (mainWorktree) {
      logger.info("[Worktrees] Selecting main worktree as default", {
        worktreeId: mainWorktree.id,
        name: mainWorktree.name,
        branch: mainWorktree.branch,
      });
      useWorktreeStore.getState().selectWorktree(mainWorktree, { skipWorkspaceStateSave: true });
      return true;
    }
    logger.warn("[Worktrees] No main worktree found for project", { projectId });
    useWorktreeStore.getState().selectWorktree(null, { skipWorkspaceStateSave: true });
    return false;
  };

  if (!lastWorktreeId) {
    logger.info("[Worktrees] No last worktree to restore, selecting main worktree");
    return selectMainWorktree();
  }

  // By id: the saved workspace need not be in any loaded page.
  const worktree = await resolveWorktree(lastWorktreeId, qc);
  if (!isCurrent()) return false;
  if (!worktree || worktree.deleted_at || (worktree.project_id && worktree.project_id !== projectId)) {
    logger.warn("[Worktrees] Last worktree is not restorable, selecting main worktree", { lastWorktreeId });
    // Forget the saved workspace only when it is definitely unusable. A failed
    // lookup (offline, a blip) says nothing about it, and clearing the pointer
    // then would lose the user's place for good.
    if (worktree || isWorktreeKnownGone(lastWorktreeId, qc)) {
      workspaceState.setLastWorktree(projectId, null);
    }
    return selectMainWorktree();
  }

  logger.info("[Worktrees] Restoring last worktree", { worktreeId: worktree.id, name: worktree.name });

  // Select without saving (we're restoring, not changing)
  useWorktreeStore.getState().selectWorktree(worktree, { skipWorkspaceStateSave: true });
  // Main stays cached whichever workspace is selected: the terminal, browser
  // and chat header fall back to it. One row.
  await resolveMainWorktree(projectId, qc);
  return true;
}

