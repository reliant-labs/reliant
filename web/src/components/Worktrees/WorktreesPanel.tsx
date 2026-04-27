import { useState, useEffect } from "react";
import { RefreshCw, Loader2, Search, AlertCircle, FolderGit2, Download } from "lucide-react";
import { cn } from "../../lib/utils";
import { useWorktreeStore } from "../../store/worktreeStore";
import { useProjectStore } from "../../store/projectStore";
import { CreateWorktreeModal } from "./CreateWorktreeModal";
import { DiscoverWorktreesModal } from "./DiscoverWorktreesModal";
import { AddRepoModal } from "./AddRepoModal";
import { InitializeGitModal } from "../Git/InitializeGitModal";
import { Button } from "../ui/Button";
import { WorktreeStatus } from "../../gen/reliant/v1/worktree_pb";

interface WorktreesPanelProps {
  paddingClass?: string;
  daemonName?: string;
}

export function WorktreesPanel({ paddingClass = "", daemonName }: WorktreesPanelProps) {
  const allWorktrees = useWorktreeStore((state) => state.worktrees);
  const currentWorktree = useWorktreeStore((state) => state.currentWorktree);
  const loadWorktrees = useWorktreeStore((state) => state.loadWorktrees);
  const switchWorktreeContext = useWorktreeStore((state) => state.switchWorktreeContext);
  const isLoading = useWorktreeStore((state) => state.isLoading);
  const deletingId = useWorktreeStore((state) => state.deletingId);
  const error = useWorktreeStore((state) => state.error);

  // Filter out archived worktrees (only show active ones)
  const worktrees = allWorktrees.filter(w => !w.deleted_at);

  const currentProject = useProjectStore((state) => state.currentProject);
  const refreshCurrentProject = useProjectStore((state) => state.refreshCurrentProject);
  const [showCreateModal, setShowCreateModal] = useState(false);
  const [showDiscoverModal, setShowDiscoverModal] = useState(false);
  const [showAddRepoModal, setShowAddRepoModal] = useState(false);
  const [showInitGitModal, setShowInitGitModal] = useState(false);

  const getStatusColor = (status: WorktreeStatus) => {
    switch (status) {
      case WorktreeStatus.ACTIVE:
        return 'bg-status-active';
      case WorktreeStatus.COMPLETED:
        return 'bg-status-completed';
      case WorktreeStatus.ABANDONED:
        return 'bg-status-abandoned';
      case WorktreeStatus.MERGING:
        return 'bg-status-merging';
      default:
        return 'bg-muted-foreground';
    }
  };

  useEffect(() => {
    if (currentProject) {
      loadWorktrees(currentProject.id);
    }
  }, [currentProject, loadWorktrees]);

  const handleCreateWorktree = () => {
    if (!currentProject) {
      alert("Please select a project first");
      return;
    }
    setShowCreateModal(true);
  };

  const handleWorktreeCreated = (_worktreeId: string) => {
    setShowCreateModal(false);
    if (currentProject) {
      loadWorktrees(currentProject.id);
    }
  };

  const handleDiscoverWorktrees = () => {
    if (!currentProject) {
      alert("Please select a project first");
      return;
    }
    setShowDiscoverModal(true);
  };

  const handleWorktreesImported = () => {
    if (currentProject) {
      loadWorktrees(currentProject.id);
    }
  };

  const handleInitGitSuccess = async () => {
    await refreshCurrentProject();
    if (currentProject) {
      loadWorktrees(currentProject.id);
    }
  };

  if (!currentProject) {
    return (
      <div className="flex flex-col h-full items-center justify-center p-4 text-center">
        <FolderGit2 className="w-8 h-8 text-muted-foreground opacity-50 mb-2" />
        <p className="text-sm font-mono text-muted-foreground">
          Select a project to view worktrees
        </p>
      </div>
    );
  }

  // Show git initialization prompt if project is not a git repo
  if (!currentProject.is_git_repo) {
    return (
      <div className="flex flex-col h-full">
        {paddingClass && (
          <div className="h-12 border-b border-border bg-card"></div>
        )}
        
        <div className="p-3 border-b border-border">
          <h2 className="text-sm font-mono font-semibold">// workspaces</h2>
          <p className="text-xs font-mono text-muted-foreground mt-1">
            {currentProject.name}
          </p>
        </div>

        <div className="flex-1 flex flex-col items-center justify-center p-6 text-center">
          <div className="p-4 rounded-full bg-warning/10 ring-1 ring-warning/20 mb-4">
            <AlertCircle className="w-8 h-8 text-warning" />
          </div>
          <h3 className="text-sm font-semibold text-foreground mb-2">
            Git Repository Required
          </h3>
          <p className="text-sm text-muted-foreground mb-6 max-w-sm">
            Workspaces require a git repository. Initialize git for this project to enable workspace management.
          </p>
          <Button
            onClick={() => setShowInitGitModal(true)}
            leftIcon={<FolderGit2 className="w-4 h-4" />}
            variant="primary"
            size="md"
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
    <div className="flex flex-col h-full">
      {/* Header section - only when not fullscreen */}
      {paddingClass && (
        <div className="h-12 border-b border-border bg-card"></div>
      )}

      {/* Header */}
      <div className="p-3 border-b border-border">
        <div className="flex items-center justify-between mb-3">
          <div>
            <h2 className="text-sm font-mono font-semibold">// workspaces</h2>
            <p className="text-xs font-mono text-muted-foreground mt-1">
              {currentProject.name}
            </p>
          </div>
          <button
            onClick={() => loadWorktrees(currentProject.id)}
            className="p-1 hover:bg-accent rounded transition-colors"
            aria-label="Refresh workspaces"
          >
            <RefreshCw
              className={cn(
                "w-3 h-3 text-muted-foreground",
                isLoading && "animate-spin"
              )}
            />
          </button>
        </div>

        <div className="flex gap-2">
          <Button
            onClick={handleDiscoverWorktrees}
            leftIcon={<Search className="w-3 h-3" />}
            variant="secondary"
            size="sm"
            className="flex-1"
          >
            Discover
          </Button>
          <Button
            onClick={handleCreateWorktree}
            leftIcon={<FolderGit2 className="w-3 h-3" />}
            variant="primary"
            size="sm"
            className="flex-1"
          >
            Create
          </Button>
          {daemonName && (
            <Button
              onClick={() => setShowAddRepoModal(true)}
              leftIcon={<Download className="w-3 h-3" />}
              variant="secondary"
              size="sm"
              className="flex-1"
            >
              Add Repo
            </Button>
          )}
        </div>
      </div>

      {/* Error Message */}
      {error && (
        <div className="p-3 m-3 bg-destructive/10 text-destructive rounded text-xs font-mono">
          {error}
        </div>
      )}

      {/* Worktrees List */}
      <div className="flex-1 overflow-y-auto">
        {worktrees.length === 0 ? (
          <div className="p-4 text-center text-muted-foreground text-xs font-mono">
            <FolderGit2 className="w-8 h-8 mx-auto mb-2 opacity-50" />
            <p>No workspaces yet</p>
            <p className="mt-1 text-xs">Create a workspace to start working</p>
          </div>
        ) : (
          <div className="p-3 space-y-0.5">
            {worktrees.map((worktree) => {
              const isDeleting = deletingId === worktree.id;
              return (
                <button
                  key={worktree.id}
                  className={cn(
                    "flex items-center gap-2 px-2.5 py-1.5 rounded-lg text-xs cursor-pointer hover:bg-[var(--transparent-button-hover)] transition-colors font-mono w-full text-left",
                    currentWorktree?.id === worktree.id &&
                      "bg-[var(--transparent-button-hover)] shadow-sm",
                    isDeleting && "opacity-60"
                  )}
                  onClick={() => !isDeleting && currentProject && switchWorktreeContext(currentProject.id, worktree)}
                  title={`${worktree.name} (${WorktreeStatus[worktree.status]?.toLowerCase() ?? "unknown"})`}
                  disabled={isDeleting}
                >
                  <div className={cn("w-2 h-2 rounded-full flex-shrink-0", getStatusColor(worktree.status))} />
                  <FolderGit2 className="w-3 h-3 text-muted-foreground flex-shrink-0" />
                  <span className="flex-1 truncate">
                    {worktree.name.toLowerCase().replace(/\s+/g, "_")}
                  </span>
                  {isDeleting && (
                    <Loader2 className="w-3 h-3 text-muted-foreground animate-spin flex-shrink-0" />
                  )}
                </button>
              );
            })}
          </div>
        )}
      </div>

      {/* Create Workspace Modal */}
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
          {daemonName && (
            <AddRepoModal
              isOpen={showAddRepoModal}
              onClose={() => setShowAddRepoModal(false)}
              daemonName={daemonName}
            />
          )}
        </>
      )}
    </div>
  );
}