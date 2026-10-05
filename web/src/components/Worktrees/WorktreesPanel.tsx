import { useEffect, useMemo, useState } from "react";
import {
  AlertCircle,
  Archive,
  Download,
  FolderGit2,
  FolderOpen,
  Loader2,
  Plus,
  RefreshCw,
  Search,
} from "lucide-react";
import { cn } from "../../lib/utils";
import { useWorktreeStore, type Worktree } from "../../store/worktreeStore";
import { useProjectStore } from "../../store/projectStore";
import { useChatList } from "../../hooks/chat-queries";
import { CreateWorktreeModal } from "./CreateWorktreeModal";
import { DiscoverWorktreesModal } from "./DiscoverWorktreesModal";
import { DeleteWorktreeModal } from "./DeleteWorktreeModal";
import { AddRepoModal } from "./AddRepoModal";
import { InitializeGitModal } from "../Git/InitializeGitModal";
import { Button } from "../ui/Button";
import EmptyState from "../forge-ui/empty_state";
import SkeletonLoader from "../forge-ui/skeleton_loader";
import { chatsForWorkspace, sortActiveWorkspaces } from "./workspaceStatus";
import { useArchiveWorkspace, useOpenWorkspace } from "./useWorkspaceActions";
import { workspaceColumn, workspaceTable } from "./workspaceStyles";
import {
  BranchCell,
  GitStateCell,
  IconAction,
  TimeCell,
  WorkspaceNameCell,
} from "./WorkspaceTableParts";
import type { Chat } from "../../types/chat";

interface WorktreesPanelProps {
  /** Padding around the panel. The viewer tab relies on the default. */
  paddingClass?: string;
  daemonId?: string;
  includeArchivedOnLoad?: boolean;
  /**
   * Called when a workspace's name is clicked. Without it, clicking a name
   * switches the app into that workspace (the viewer tab's behaviour, where
   * the detail pane follows the current workspace).
   */
  onSelect?: (worktree: Worktree) => void;
  /** Highlighted row. Defaults to the app's current workspace. */
  selectedId?: string | null;
  /** Called after "Open workspace" / a chat link has switched the app into it. */
  onOpened?: () => void;
}

/**
 * The current project's active workspaces as a table: name, branch, the
 * working tree's git state, its most recent chat, and when it was last used.
 * Rendered by Settings → Workspaces and by the Workspaces viewer tab, whose
 * sidebar is narrow — the table is a container query, so columns drop out by
 * the width it actually gets rather than by viewport.
 */
export function WorktreesPanel({
  paddingClass = "p-3",
  daemonId,
  includeArchivedOnLoad = false,
  onSelect,
  selectedId,
  onOpened,
}: WorktreesPanelProps) {
  const allWorktrees = useWorktreeStore((state) => state.worktrees);
  const currentWorktree = useWorktreeStore((state) => state.currentWorktree);
  const loadWorktrees = useWorktreeStore((state) => state.loadWorktrees);
  const isLoading = useWorktreeStore((state) => state.isLoading);
  const deletingId = useWorktreeStore((state) => state.deletingId);
  const error = useWorktreeStore((state) => state.error);

  const currentProject = useProjectStore((state) => state.currentProject);
  const refreshCurrentProject = useProjectStore((state) => state.refreshCurrentProject);
  const { data: chats = [] } = useChatList(currentProject?.id);

  const [showCreateModal, setShowCreateModal] = useState(false);
  const [showDiscoverModal, setShowDiscoverModal] = useState(false);
  const [showAddRepoModal, setShowAddRepoModal] = useState(false);
  const [showInitGitModal, setShowInitGitModal] = useState(false);

  const archive = useArchiveWorkspace();
  const { openWorkspace, openChat } = useOpenWorkspace(onOpened);

  const worktrees = useMemo(
    () => sortActiveWorkspaces(allWorktrees.filter((worktree) => !worktree.deleted_at)),
    [allWorktrees],
  );
  const highlightedId = selectedId === undefined ? currentWorktree?.id : selectedId;

  const refreshWorktrees = () => {
    if (currentProject) {
      return loadWorktrees(currentProject.id, { includeArchived: includeArchivedOnLoad });
    }
    return Promise.resolve();
  };

  useEffect(() => {
    if (currentProject) {
      loadWorktrees(currentProject.id, { includeArchived: includeArchivedOnLoad });
    }
  }, [currentProject, includeArchivedOnLoad, loadWorktrees]);

  const handleSelect = (worktree: Worktree) => {
    if (onSelect) onSelect(worktree);
    else void openWorkspace(worktree);
  };

  if (!currentProject) {
    return (
      <div className={cn("forge-ui", paddingClass)}>
        <EmptyState
          icon={<FolderGit2 className="h-6 w-6" />}
          title="No project open"
          description="Workspaces belong to a project. Open a project to see the workspaces its chats are using."
        />
      </div>
    );
  }

  if (!currentProject.is_git_repo) {
    return (
      <div className={cn("forge-ui", paddingClass)}>
        <EmptyState
          icon={<AlertCircle className="h-6 w-6" />}
          title="Workspaces need a git repository"
          description={`Each workspace is a git worktree on its own branch, so ${currentProject.name} has to be a git repository first. Initializing git doesn't change your files.`}
          actionLabel="Initialize git"
          onAction={() => setShowInitGitModal(true)}
        />
        <InitializeGitModal
          isOpen={showInitGitModal}
          onClose={() => setShowInitGitModal(false)}
          onSuccess={async () => {
            await refreshCurrentProject();
            await refreshWorktrees();
          }}
          projectId={currentProject.id}
          projectName={currentProject.name}
        />
      </div>
    );
  }

  const onlyMain = worktrees.length > 0 && worktrees.every((worktree) => worktree.is_main);

  return (
    <div className={cn("forge-ui flex flex-col gap-3", paddingClass)} data-testid="active-workspaces">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p className="text-xs text-muted-foreground">
          {worktrees.length === 1 ? "1 workspace" : `${worktrees.length} workspaces`}
        </p>
        <div className="flex flex-wrap items-center gap-1.5">
          <IconAction
            label="Refresh workspaces"
            icon={<RefreshCw className={cn("h-3.5 w-3.5", isLoading && "animate-spin")} />}
            onClick={() => void refreshWorktrees()}
            disabled={isLoading}
          />
          {daemonId && (
            <Button
              variant="outline"
              size="sm"
              leftIcon={<Download className="h-3.5 w-3.5" />}
              onClick={() => setShowAddRepoModal(true)}
              title="Clone a repository onto this machine"
            >
              Clone repository
            </Button>
          )}
          <Button
            variant="outline"
            size="sm"
            leftIcon={<Search className="h-3.5 w-3.5" />}
            onClick={() => setShowDiscoverModal(true)}
            title="Find git worktrees that already exist on disk and add them here"
          >
            Import existing
          </Button>
          <Button
            variant="primary"
            size="sm"
            leftIcon={<Plus className="h-3.5 w-3.5" />}
            onClick={() => setShowCreateModal(true)}
          >
            New workspace
          </Button>
        </div>
      </div>

      {error && (
        <div
          role="alert"
          className="flex items-start gap-2 rounded-md border border-danger-border bg-danger-surface px-3 py-2 text-xs text-danger-ink"
        >
          <AlertCircle className="mt-0.5 h-3.5 w-3.5 flex-shrink-0" aria-hidden="true" />
          <span>{error}</span>
        </div>
      )}

      {isLoading && worktrees.length === 0 ? (
        <div className="rounded-lg border border-border bg-card">
          <SkeletonLoader variant="table-row" count={3} />
        </div>
      ) : worktrees.length === 0 ? (
        <EmptyState
          icon={<FolderGit2 className="h-6 w-6" />}
          title="No workspaces yet"
          description="When you branch a chat, Reliant gives it its own git worktree on a new branch, so the agent can change code without touching your main checkout. Branch a chat, or create a workspace directly."
          actionLabel="New workspace"
          onAction={() => setShowCreateModal(true)}
        />
      ) : (
        <div className={workspaceTable.wrapper}>
          <table className={workspaceTable.table}>
            <caption className="sr-only">
              Active workspaces in {currentProject.name}: branch, git state, linked chat, and when
              each was last used.
            </caption>
            <thead>
              <tr className={workspaceTable.headRow}>
                <th scope="col" className={workspaceTable.headCell}>
                  Workspace
                </th>
                <th scope="col" className={cn(workspaceTable.headCell, workspaceColumn.branch)}>
                  Branch
                </th>
                <th scope="col" className={workspaceTable.headCell}>
                  <span className="sr-only @lg:not-sr-only">Git</span>
                </th>
                <th scope="col" className={cn(workspaceTable.headCell, workspaceColumn.chat)}>
                  Chat
                </th>
                <th scope="col" className={cn(workspaceTable.headCell, workspaceColumn.time)}>
                  Last active
                </th>
                <th scope="col" className={cn(workspaceTable.headCell, "text-right")}>
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {worktrees.map((worktree) => {
                const isArchiving = deletingId === worktree.id;
                return (
                  <tr
                    key={worktree.id}
                    data-testid={`workspace-row-${worktree.id}`}
                    data-selected={highlightedId === worktree.id || undefined}
                    className={cn(
                      workspaceTable.row,
                      highlightedId === worktree.id && "bg-primary/5",
                      isArchiving && "opacity-60",
                    )}
                  >
                    <td className={cn(workspaceTable.cell, "max-w-0 w-full @xl:w-auto @xl:max-w-xs")}>
                      <WorkspaceNameCell
                        worktree={worktree}
                        isCurrent={currentWorktree?.id === worktree.id}
                        onSelect={() => handleSelect(worktree)}
                      />
                    </td>
                    <td className={cn(workspaceTable.cell, workspaceColumn.branch, "max-w-[14rem]")}>
                      <BranchCell worktree={worktree} />
                    </td>
                    <td className={workspaceTable.cell}>
                      <GitStateCell worktree={worktree} />
                    </td>
                    <td className={cn(workspaceTable.cell, workspaceColumn.chat, "max-w-[16rem]")}>
                      <LinkedChatCell
                        chats={chatsForWorkspace(chats, worktree.id)}
                        onOpen={(chat) => void openChat(chat)}
                      />
                    </td>
                    <td className={cn(workspaceTable.cell, workspaceColumn.time)}>
                      <TimeCell value={worktree.last_active} />
                    </td>
                    <td className={cn(workspaceTable.cell, "whitespace-nowrap text-right")}>
                      <div className="inline-flex items-center gap-0.5">
                        <IconAction
                          label={`Open ${worktree.name}`}
                          hint="Open workspace"
                          icon={<FolderOpen className="h-3.5 w-3.5" />}
                          onClick={() => void openWorkspace(worktree)}
                          testId={`open-workspace-${worktree.id}`}
                        />
                        <IconAction
                          label={`Archive ${worktree.name}`}
                          hint={
                            worktree.is_main
                              ? "The main checkout can't be archived"
                              : "Archive workspace"
                          }
                          icon={
                            isArchiving ? (
                              <Loader2 className="h-3.5 w-3.5 animate-spin" />
                            ) : (
                              <Archive className="h-3.5 w-3.5" />
                            )
                          }
                          onClick={() => archive.requestArchive(worktree)}
                          disabled={worktree.is_main || isArchiving}
                          testId={`archive-workspace-${worktree.id}`}
                        />
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}

      {onlyMain && (
        <p className="text-pretty text-xs text-muted-foreground">
          Only the main checkout so far. Branch a chat, or create a workspace, to have an agent
          work on something in parallel on its own branch.
        </p>
      )}

      <CreateWorktreeModal
        isOpen={showCreateModal}
        onClose={() => setShowCreateModal(false)}
        onWorktreeCreated={() => {
          setShowCreateModal(false);
          void refreshWorktrees();
        }}
        projectId={currentProject.id}
      />
      <DiscoverWorktreesModal
        isOpen={showDiscoverModal}
        onClose={() => setShowDiscoverModal(false)}
        onWorktreesImported={() => refreshWorktrees()}
        projectId={currentProject.id}
      />
      {daemonId && (
        <AddRepoModal
          isOpen={showAddRepoModal}
          onClose={() => setShowAddRepoModal(false)}
          daemonId={daemonId}
        />
      )}
      <DeleteWorktreeModal
        key={archive.pending?.id ?? "none"}
        isOpen={archive.pending !== null}
        onClose={archive.cancel}
        worktree={archive.pending}
        chatCount={archive.pending ? chatsForWorkspace(chats, archive.pending.id).length : 0}
        onConfirmDelete={archive.confirm}
      />
    </div>
  );
}

/** The workspace's most recent chat, as a link that opens it. */
function LinkedChatCell({ chats, onOpen }: { chats: Chat[]; onOpen: (chat: Chat) => void }) {
  const latest = chats[0];
  if (!latest) {
    return <span className="text-xs text-muted-foreground">No chats</span>;
  }
  const title = latest.title || "Untitled chat";
  return (
    <div className="flex min-w-0 items-center gap-1.5">
      <button
        type="button"
        onClick={() => onOpen(latest)}
        aria-label={`Open chat ${title}`}
        className="min-w-0 truncate rounded-sm text-left text-xs text-foreground hover:text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        {title}
      </button>
      {chats.length > 1 && (
        <span className="flex-shrink-0 text-2xs tabular-nums text-muted-foreground">
          +{chats.length - 1}
        </span>
      )}
    </div>
  );
}
