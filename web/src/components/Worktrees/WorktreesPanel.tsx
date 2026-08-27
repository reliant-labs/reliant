import { useState, useEffect } from "react";
import { RefreshCw, Loader2, Search, AlertCircle, FolderGit2, Download, Plus } from "lucide-react";
import { cn } from "../../lib/utils";
import { useWorktreeStore } from "../../store/worktreeStore";
import { useProjectStore } from "../../store/projectStore";
import { CreateWorktreeModal } from "./CreateWorktreeModal";
import { DiscoverWorktreesModal } from "./DiscoverWorktreesModal";
import { AddRepoModal } from "./AddRepoModal";
import { InitializeGitModal } from "../Git/InitializeGitModal";
import { Button } from "../ui/Button";
import { WorktreeStatus } from "../../gen/reliant/v1/worktree_pb";
import { workspaceButton } from "./workspaceStyles";

interface WorktreesPanelProps {
  paddingClass?: string;
  daemonId?: string;
  includeArchivedOnLoad?: boolean;
}

export function WorktreesPanel({
  paddingClass = "",
  daemonId,
  includeArchivedOnLoad = false,
}: WorktreesPanelProps) {
  const allWorktrees = useWorktreeStore((state) => state.worktrees);
  const currentWorktree = useWorktreeStore((state) => state.currentWorktree);
  const loadWorktrees = useWorktreeStore((state) => state.loadWorktrees);
  const switchWorktreeContext = useWorktreeStore((state) => state.switchWorktreeContext);
  const isLoading = useWorktreeStore((state) => state.isLoading);
  const deletingId = useWorktreeStore((state) => state.deletingId);
  const error = useWorktreeStore((state) => state.error);

  const worktrees = allWorktrees.filter((worktree) => !worktree.deleted_at);

  const currentProject = useProjectStore((state) => state.currentProject);
  const refreshCurrentProject = useProjectStore((state) => state.refreshCurrentProject);
  const [showCreateModal, setShowCreateModal] = useState(false);
  const [showDiscoverModal, setShowDiscoverModal] = useState(false);
  const [showAddRepoModal, setShowAddRepoModal] = useState(false);
  const [showInitGitModal, setShowInitGitModal] = useState(false);

  const getStatusColor = (status: WorktreeStatus) => {
    switch (status) {
      case WorktreeStatus.ACTIVE:
        return "bg-status-active";
      case WorktreeStatus.COMPLETED:
        return "bg-status-completed";
      case WorktreeStatus.ABANDONED:
        return "bg-status-abandoned";
      case WorktreeStatus.MERGING:
        return "bg-status-merging";
      default:
        return "bg-muted-foreground";
    }
  };

  const getStatusLabel = (status: WorktreeStatus) => {
    switch (status) {
      case WorktreeStatus.ACTIVE:
        return "Active";
      case WorktreeStatus.COMPLETED:
        return "Completed";
      case WorktreeStatus.ABANDONED:
        return "Abandoned";
      case WorktreeStatus.MERGING:
        return "Merging";
      default:
        return "Unknown";
    }
  };

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

  const handleCreateWorktree = () => {
    if (!currentProject) {
      alert("Please select a project first");
      return;
    }
    setShowCreateModal(true);
  };

  const handleWorktreeCreated = (_worktreeId: string) => {
    setShowCreateModal(false);
    refreshWorktrees();
  };

  const handleDiscoverWorktrees = () => {
    if (!currentProject) {
      alert("Please select a project first");
      return;
    }
    setShowDiscoverModal(true);
  };

  const handleWorktreesImported = () => {
    refreshWorktrees();
  };

  const handleInitGitSuccess = async () => {
    await refreshCurrentProject();
    await refreshWorktrees();
  };

  if (!currentProject) {
    return (
      <div className="flex h-full flex-col items-center justify-center p-6 text-center">
        <FolderGit2 className="mb-3 h-10 w-10 text-muted-foreground/40" />
        <p className="text-sm font-medium text-foreground">No project selected</p>
        <p className="mt-1 text-xs text-muted-foreground">
          Select a project to view its workspaces.
        </p>
      </div>
    );
  }

  if (!currentProject.is_git_repo) {
    return (
      <div className={cn("flex h-full flex-col", paddingClass)}>
        <div className="border-b border-border/60 px-4 py-4">
          <p className="text-sm font-semibold text-foreground">Active workspaces</p>
          <p className="mt-1 text-xs text-muted-foreground">{currentProject.name}</p>
        </div>

        <div className="flex flex-1 flex-col items-center justify-center p-6 text-center">
          <div className="mb-4 rounded-full bg-warning/10 p-4 ring-1 ring-warning/20">
            <AlertCircle className="h-8 w-8 text-warning" />
          </div>
          <h3 className="mb-2 text-sm font-semibold text-foreground">
            Git repository required
          </h3>
          <p className="mb-6 max-w-sm text-sm text-muted-foreground">
            Workspaces require a git repository. Initialize git for this project to enable workspace management.
          </p>
          <Button
            onClick={() => setShowInitGitModal(true)}
            leftIcon={<FolderGit2 className="h-4 w-4" />}
            variant="primary"
            size="md"
            className={workspaceButton.primary}
          >
            Initialize Git Repository
          </Button>
        </div>

        <InitializeGitModal
          isOpen={showInitGitModal}
          onClose={() => setShowInitGitModal(false)}
          onSuccess={handleInitGitSuccess}
          projectId={currentProject.id}
          projectName={currentProject.name}
        />
      </div>
    );
  }

  return (
    <div className={cn("flex h-full flex-col", paddingClass)}>
      <div className="flex-shrink-0 border-b border-border/60 px-4 py-4">
        <div className="mb-3 flex items-center justify-between gap-3">
          <div className="min-w-0">
            <div className="flex items-center gap-2">
              <h2 className="text-sm font-semibold text-foreground">Active workspaces</h2>
              <span className="rounded-full bg-muted px-2 py-0.5 text-xs font-medium text-muted-foreground">
                {worktrees.length}
              </span>
            </div>
            <p className="mt-1 truncate text-xs text-muted-foreground">
              {currentProject.name}
            </p>
          </div>
          <Button
            onClick={refreshWorktrees}
            leftIcon={<RefreshCw className={cn("h-3 w-3", isLoading && "animate-spin")} />}
            variant="outline"
            size="xs"
            disabled={isLoading}
            className={workspaceButton.subtle}
          >
            Refresh
          </Button>
        </div>

        <div className="grid grid-cols-2 gap-2">
          <Button
            onClick={handleCreateWorktree}
            leftIcon={<Plus className="h-3 w-3" />}
            variant="secondary"
            size="sm"
            className={workspaceButton.secondary}
          >
            New
          </Button>
          <Button
            onClick={handleDiscoverWorktrees}
            leftIcon={<Search className="h-3 w-3" />}
            variant="secondary"
            size="sm"
            className={workspaceButton.secondary}
          >
            Import
          </Button>
          {daemonId && (
            <Button
              onClick={() => setShowAddRepoModal(true)}
              leftIcon={<Download className="h-3 w-3" />}
              variant="secondary"
              size="sm"
              className={cn("col-span-2", workspaceButton.secondary)}
            >
              Add Repository
            </Button>
          )}
        </div>
      </div>

      {error && (
        <div className="mx-4 mt-4 rounded-lg border border-destructive/20 bg-destructive/10 px-3 py-2 text-xs text-destructive">
          {error}
        </div>
      )}

      <div className="min-h-0 flex-1 overflow-y-auto p-3">
        {isLoading && worktrees.length === 0 ? (
          <div className="flex h-32 items-center justify-center">
            <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
          </div>
        ) : worktrees.length === 0 ? (
          <div className="flex h-full flex-col items-center justify-center rounded-xl border border-dashed border-border/70 p-6 text-center">
            <FolderGit2 className="mb-3 h-10 w-10 text-muted-foreground/35" />
            <p className="text-sm font-medium text-foreground">No workspaces yet</p>
            <p className="mt-1 max-w-48 text-xs text-muted-foreground">
              Create a workspace or import an existing git worktree.
            </p>
          </div>
        ) : (
          <div className="space-y-2">
            {worktrees.map((worktree) => {
              const isDeleting = deletingId === worktree.id;
              const isSelected = currentWorktree?.id === worktree.id;

              return (
                <button
                  key={worktree.id}
                  type="button"
                  className={cn(
                    "w-full rounded-xl border p-3 text-left transition-colors focus:outline-none focus:ring-2 focus:ring-primary/50",
                    isSelected
                      ? "border-primary/50 bg-primary/5 shadow-sm"
                      : "border-border/60 bg-background hover:border-primary/30 hover:bg-muted/40",
                    isDeleting && "cursor-not-allowed opacity-60"
                  )}
                  onClick={() => !isDeleting && switchWorktreeContext(currentProject.id, worktree)}
                  title={`${worktree.name} (${getStatusLabel(worktree.status)})`}
                  disabled={isDeleting}
                >
                  <div className="flex items-start gap-3">
                    <div className={cn("mt-1.5 h-2.5 w-2.5 flex-shrink-0 rounded-full", getStatusColor(worktree.status))} />
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2">
                        <span className="truncate text-sm font-medium text-foreground">
                          {worktree.name}
                        </span>
                        {worktree.is_main && (
                          <span className="rounded-full bg-muted px-1.5 py-0.5 text-2xs font-medium uppercase tracking-wide text-muted-foreground">
                            Main
                          </span>
                        )}
                      </div>
                      <p className="mt-1 truncate text-xs text-muted-foreground">
                        {worktree.branch}
                        {worktree.base_branch && ` → ${worktree.base_branch}`}
                      </p>
                    </div>
                    {isDeleting ? (
                      <Loader2 className="h-4 w-4 flex-shrink-0 animate-spin text-muted-foreground" />
                    ) : (
                      <span className="flex-shrink-0 rounded-full bg-muted px-2 py-0.5 text-xs text-muted-foreground">
                        {getStatusLabel(worktree.status)}
                      </span>
                    )}
                  </div>
                </button>
              );
            })}
          </div>
        )}
      </div>

      {currentProject && (
        <>
          <CreateWorktreeModal
            isOpen={showCreateModal}
            onClose={() => setShowCreateModal(false)}
            onWorktreeCreated={handleWorktreeCreated}
            projectId={currentProject.id}
          />
          <DiscoverWorktreesModal
            isOpen={showDiscoverModal}
            onClose={() => setShowDiscoverModal(false)}
            onWorktreesImported={handleWorktreesImported}
            projectId={currentProject.id}
          />
          {daemonId && (
            <AddRepoModal
              isOpen={showAddRepoModal}
              onClose={() => setShowAddRepoModal(false)}
              daemonId={daemonId}
            />
          )}
        </>
      )}
    </div>
  );
}