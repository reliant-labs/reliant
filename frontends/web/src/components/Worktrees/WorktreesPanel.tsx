import { useMemo, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
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
import { useWorktreeStore } from "../../store/worktreeStore";
import { useProjectStore } from "../../store/projectStore";
import { useChatList } from "../../hooks/chat-queries";
import { CreateWorktreeModal } from "./CreateWorktreeModal";
import { DiscoverWorktreesModal } from "./DiscoverWorktreesModal";
import { WorktreesDiscoverHint } from "./WorktreesDiscoverHint";
import { DeleteWorktreeModal } from "./DeleteWorktreeModal";
import { AddRepoModal } from "./AddRepoModal";
import { InitializeGitModal } from "../Git/InitializeGitModal";
import Button from "@/components/forge-ui/button";
import EmptyState from "../forge-ui/empty_state";
import SkeletonLoader from "../forge-ui/skeleton_loader";
import { chatsForWorkspace, sortActiveWorkspaces } from "./workspaceStatus";
import { useArchiveWorkspace, useOpenWorkspace } from "./useWorkspaceActions";
import DataTable, { type DataTableColumn } from "../forge-ui/data_table";
import { workspaceColumn, workspaceTable } from "./workspaceStyles";
import {
  BranchCell,
  GitStateCell,
  IconAction,
  TimeCell,
  WorkspaceNameCell,
} from "./WorkspaceTableParts";
import type { Chat } from "../../types/chat";
import {
  invalidateDiscoveredWorktrees,
  invalidateWorktreeLists,
  useCurrentWorktree,
  useWorktreeList,
  type Worktree,
} from "../../hooks/worktree-queries";
type WorkspaceRow = { id: string; worktree: Worktree };

interface WorktreesPanelProps {
  /** Padding around the panel. The viewer tab relies on the default. */
  paddingClass?: string;
  daemonId?: string;
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
 *
 * forge-ui's DataTable, flush in a frame that also holds "Load more". The
 * whole row selects the workspace (getRowAction); the chat and action cells
 * keep their own buttons.
 */
export function WorktreesPanel({
  paddingClass = "p-3",
  daemonId,
  onSelect,
  selectedId,
  onOpened,
}: WorktreesPanelProps) {
  const currentWorktree = useCurrentWorktree();
  const deletingId = useWorktreeStore((state) => state.deletingId);

  const currentProject = useProjectStore((state) => state.currentProject);
  const refreshCurrentProject = useProjectStore((state) => state.refreshCurrentProject);
  const { data: chats = [] } = useChatList(currentProject?.id);
  // First page on open; further pages on request. The panel is the one place
  // that lists workspaces for their own sake, so it is where paging lives.
  const {
    worktrees: allWorktrees,
    hasMore,
    isLoading,
    isLoadingMore,
    error: listError,
    loadMore: loadMoreWorktrees,
  } = useWorktreeList(currentProject?.id);
  const error = listError ? listError.message || "Failed to load worktrees" : null;

  const [showCreateModal, setShowCreateModal] = useState(false);
  const [showDiscoverModal, setShowDiscoverModal] = useState(false);
  const [showAddRepoModal, setShowAddRepoModal] = useState(false);
  const [showInitGitModal, setShowInitGitModal] = useState(false);

  const queryClient = useQueryClient();
  const archive = useArchiveWorkspace();
  const { openWorkspace, openChat } = useOpenWorkspace(onOpened);

  const worktrees = useMemo(
    () => sortActiveWorkspaces(allWorktrees.filter((worktree) => !worktree.deleted_at)),
    [allWorktrees],
  );
  const highlightedId = selectedId === undefined ? currentWorktree?.id : selectedId;

  const refreshWorktrees = () => invalidateWorktreeLists(queryClient);

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

  const columns: DataTableColumn<WorkspaceRow>[] = [
    {
      key: "name",
      header: "Workspace",
      className: "max-w-0 w-full @xl:w-auto @xl:max-w-xs",
      render: (_, { worktree }) => (
        <WorkspaceNameCell worktree={worktree} isCurrent={currentWorktree?.id === worktree.id} />
      ),
    },
    {
      key: "branch",
      header: "Branch",
      className: cn(workspaceColumn.branch, "max-w-[14rem]"),
      headerClassName: workspaceColumn.branch,
      render: (_, { worktree }) => <BranchCell worktree={worktree} />,
    },
    {
      key: "git",
      header: "Git",
      render: (_, { worktree }) => <GitStateCell worktree={worktree} />,
    },
    {
      key: "chat",
      header: "Chat",
      interactive: true,
      className: cn(workspaceColumn.chat, "max-w-[16rem]"),
      headerClassName: workspaceColumn.chat,
      render: (_, { worktree }) => (
        <LinkedChatCell
          chats={chatsForWorkspace(chats, worktree.id)}
          onOpen={(chat) => void openChat(chat)}
        />
      ),
    },
    {
      key: "lastActive",
      header: "Last active",
      className: workspaceColumn.time,
      headerClassName: workspaceColumn.time,
      render: (_, { worktree }) => <TimeCell value={worktree.last_active} />,
    },
    {
      key: "actions",
      header: "Actions",
      srHeader: true,
      interactive: true,
      className: "text-right",
      render: (_, { worktree }) => {
        const isArchiving = deletingId === worktree.id;
        return (
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
              hint={worktree.is_main ? "The main checkout can't be archived" : "Archive workspace"}
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
        );
      },
    },
  ];

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

      <WorktreesDiscoverHint projectId={currentProject.id} onReview={() => setShowDiscoverModal(true)} />

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
        <div className={workspaceTable.frame}>
          <DataTable<WorkspaceRow>
            flush
            ariaLabel={`Active workspaces in ${currentProject.name}`}
            columns={columns}
            data={worktrees.map((worktree) => ({ id: worktree.id, worktree }))}
            getRowKey={(row) => row.id}
            getRowAction={({ worktree }) => () => handleSelect(worktree)}
            getRowProps={({ worktree }) => ({
              "data-testid": `workspace-row-${worktree.id}`,
              "data-selected": highlightedId === worktree.id ? "true" : undefined,
              className: cn(
                highlightedId === worktree.id && "bg-primary/5",
                deletingId === worktree.id && "opacity-60",
              ),
            })}
          />
          {hasMore && (
            <div className="flex justify-center border-t border-border p-2">
              <button
                type="button"
                className="text-xs text-primary hover:underline disabled:opacity-60"
                disabled={isLoadingMore}
                onClick={loadMoreWorktrees}
                data-testid="load-more-workspaces"
              >
                {isLoadingMore ? "Loading…" : "Load more workspaces"}
              </button>
            </div>
          )}
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
        onClose={() => {
          setShowDiscoverModal(false);
          void invalidateDiscoveredWorktrees(queryClient, currentProject.id);
        }}
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
