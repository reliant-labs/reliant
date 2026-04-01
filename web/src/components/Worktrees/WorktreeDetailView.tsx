import { Archive, ExternalLink, Plus, Clock, FolderOpen, GitMerge, GitPullRequest, Copy, Check, ChevronDown, Loader2, FolderGit2 } from "lucide-react";
import { cn } from "../../lib/utils";
import { useWorktreeStore, type Worktree } from "../../store/worktreeStore";
import { WorktreeStatus } from "../../gen/reliant/v1/worktree_pb";
import { useProjectStore } from "../../store/projectStore";
import { useChatStore } from "../../store/chatStore";
import { useSettingsStore } from "../../store/settingsStore";
import { useWindowContext } from "../../hooks/useWindowContext";
import { GitStatus } from "../Git/GitStatus";
import { GitOperations } from "../Git/GitOperations";
import { CommitHistory } from "../Git/CommitHistory";
import { Button } from "../ui/Button";
import { DeleteWorktreeModal } from "./DeleteWorktreeModal";
import { useState, useRef, useEffect, useMemo } from "react";

export function WorktreeDetailView() {
  const currentWorktree = useWorktreeStore((state) => state.currentWorktree);
  const deleteWorktree = useWorktreeStore((state) => state.deleteWorktree);
  const deletingId = useWorktreeStore((state) => state.deletingId);
  const updateWorktreeStatus = useWorktreeStore((state) => state.updateWorktreeStatus);
  const loadWorktrees = useWorktreeStore((state) => state.loadWorktrees);
  const currentProject = useProjectStore((state) => state.currentProject);
  const chatsMap = useChatStore((state) => state.chats);
  const chats = useMemo(() => Array.from(chatsMap.values()), [chatsMap]);
  const preferences = useSettingsStore((state) => state.preferences);
  const loadPreferences = useSettingsStore((state) => state.loadPreferences);
  const { isElectron, openInNewWindow } = useWindowContext();
  const [copiedPath, setCopiedPath] = useState(false);
  const [statusDropdownOpen, setStatusDropdownOpen] = useState(false);
  const [deleteModalOpen, setDeleteModalOpen] = useState(false);
  const dropdownRef = useRef<HTMLDivElement>(null);

  // Load preferences on mount
  useEffect(() => {
    loadPreferences();
  }, [loadPreferences]);

  // Close dropdown when clicking outside
  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (dropdownRef.current && !dropdownRef.current.contains(event.target as Node)) {
        setStatusDropdownOpen(false);
      }
    };

    if (statusDropdownOpen) {
      document.addEventListener('mousedown', handleClickOutside);
    }

    return () => {
      document.removeEventListener('mousedown', handleClickOutside);
    };
  }, [statusDropdownOpen]);

  // Count chats associated with this worktree
  const associatedChatCount = useMemo(() => {
    if (!currentWorktree) return 0;
    return chats.filter(chat => chat.worktreeId === currentWorktree.id).length;
  }, [chats, currentWorktree]);

  const handleStatusChange = async (newStatus: Worktree['status']) => {
    if (currentWorktree && newStatus !== currentWorktree.status) {
      await updateWorktreeStatus(currentWorktree.id, newStatus);
      setStatusDropdownOpen(false);
    }
  };

  const statusOptions: Array<{ value: Worktree['status']; label: string; description: string }> = [
    { value: WorktreeStatus.ACTIVE, label: 'Active', description: 'Currently being worked on' },
    { value: WorktreeStatus.MERGING, label: 'Merging', description: 'PR in review or merging' },
    { value: WorktreeStatus.COMPLETED, label: 'Completed', description: 'Work finished and merged' },
    { value: WorktreeStatus.ABANDONED, label: 'Abandoned', description: 'No longer working on this' },
  ];

  if (!currentWorktree) {
    return (
      <div className="flex items-center justify-center h-full text-center p-8">
        <div className="max-w-md">
          <FolderGit2 className="w-16 h-16 mx-auto text-muted-foreground/50 mb-4" />
          <h3 className="text-lg font-mono font-semibold mb-2">No workspace selected</h3>
          <p className="text-sm text-muted-foreground font-mono">
            Select a workspace from the sidebar to view details and manage git operations
          </p>
        </div>
      </div>
    );
  }

  const isDeleting = deletingId === currentWorktree.id;

  const getStatusColor = (status: WorktreeStatus) => {
    switch (status) {
      case WorktreeStatus.ACTIVE:
        return 'text-status-active';
      case WorktreeStatus.COMPLETED:
        return 'text-status-completed';
      case WorktreeStatus.ABANDONED:
        return 'text-warning';
      case WorktreeStatus.MERGING:
        return 'text-status-merging';
      default:
        return 'text-muted-foreground';
    }
  };

  const getStatusIcon = (status: WorktreeStatus) => {
    switch (status) {
      case WorktreeStatus.MERGING:
        return <GitMerge className="w-4 h-4" />;
      case WorktreeStatus.COMPLETED:
        return <GitPullRequest className="w-4 h-4" />;
      default:
        return <FolderGit2 className="w-4 h-4" />;
    }
  };

  const getStatusLabel = (status: WorktreeStatus) => {
    switch (status) {
      case WorktreeStatus.ACTIVE:
        return 'active';
      case WorktreeStatus.COMPLETED:
        return 'completed';
      case WorktreeStatus.ABANDONED:
        return 'abandoned';
      case WorktreeStatus.MERGING:
        return 'merging';
      default:
        return 'unknown';
    }
  };

  const formatDate = (date: string) => {
    if (!date) return null;
    try {
      const d = new Date(date);
      if (isNaN(d.getTime())) return null;
      return d.toLocaleDateString('en-US', {
        weekday: 'short',
        year: 'numeric',
        month: 'short',
        day: 'numeric',
        hour: '2-digit',
        minute: '2-digit'
      });
    } catch (error) {
      return null;
    }
  };

  return (
    <div className="flex flex-col h-full">
      {/* Header */}
      <div className="border-b border-border p-6">
        <div className="flex items-start justify-between mb-4">
          <div>
            <div className="flex items-center gap-3 mb-2">
              <div className={cn("flex items-center gap-2", getStatusColor(currentWorktree.status))}>
                {getStatusIcon(currentWorktree.status)}
                <h1 className="text-xl font-mono font-bold">{currentWorktree.name}</h1>
              </div>
              <span className={cn(
                "px-2 py-0.5 text-xs font-mono rounded-full border",
                getStatusColor(currentWorktree.status),
                "bg-current/10 border-current/20"
              )}>
                {getStatusLabel(currentWorktree.status)}
              </span>
            </div>
            <div className="flex items-center gap-4 text-sm text-muted-foreground font-mono">
              <div className="flex items-center gap-1">
                <FolderGit2 className="w-3 h-3" />
                <span>{currentWorktree.branch}</span>
              </div>
              <div>→ {currentWorktree.base_branch}</div>
              {formatDate(currentWorktree.last_active) && (
                <div className="flex items-center gap-1">
                  <Clock className="w-3 h-3" />
                  <span>{formatDate(currentWorktree.last_active)}</span>
                </div>
              )}
            </div>
          </div>

          <div className="flex items-center gap-2">
            {/* Status Change Dropdown */}
            <div className="relative" ref={dropdownRef}>
              <Button
                onClick={() => setStatusDropdownOpen(!statusDropdownOpen)}
                rightIcon={<ChevronDown className="w-3 h-3" />}
                variant="outline"
                size="xs"
                className="gap-1"
              >
                Change Status
              </Button>

              {statusDropdownOpen && (
                <div className="absolute top-full right-0 mt-1 w-64 elevation-4 border-2 border-border rounded-lg z-50 overflow-hidden bg-background">
                  {statusOptions.map((option) => (
                    <button
                      key={option.value}
                      onClick={() => handleStatusChange(option.value)}
                      className={cn(
                        "w-full px-3 py-2.5 text-left transition-colors border-b border-border/50 last:border-b-0 text-foreground font-mono",
                        currentWorktree.status === option.value
                          ? "bg-primary/15 font-semibold"
                          : "hover:bg-muted/50"
                      )}
                    >
                      <div className="flex items-center justify-between mb-1">
                        <span className="text-sm font-semibold">{option.label}</span>
                        {currentWorktree.status === option.value && (
                          <Check className="w-3 h-3 text-primary" />
                        )}
                      </div>
                      <div className="text-xs text-muted-foreground">{option.description}</div>
                    </button>
                  ))}
                </div>
              )}
            </div>

            {isElectron && (
              <Button
                onClick={async () => {
                  const result = await openInNewWindow(currentWorktree);
                  if (!result) {
                    alert('Failed to open worktree in new window. Please try again.');
                  }
                }}
                leftIcon={<Plus className="w-3 h-3" />}
                variant="accent"
                size="xs"
                title="Open in new window"
              >
                New Window
              </Button>
            )}

            {currentWorktree.session_id && (
              <Button
                onClick={() => {
                  // Navigate to session
                }}
                leftIcon={<ExternalLink className="w-3 h-3" />}
                variant="ghost"
                size="xs"
              >
                Open Session
              </Button>
            )}

            <Button
              onClick={() => {
                // Check preferences to determine behavior
                const mode = preferences.worktree.archiveMode;
                if (mode === 'ask_me') {
                  // Show modal to ask user
                  setDeleteModalOpen(true);
                } else if (mode === 'always_cleanup' || mode === 'always_keep') {
                  // Execute directly with preference defaults
                  const options = mode === 'always_cleanup' ? {
                    deleteGitBranch: preferences.worktree.defaultDeleteBranch,
                    deleteLocalDirectory: preferences.worktree.defaultDeleteDirectory,
                  } : {
                    deleteGitBranch: false,
                    deleteLocalDirectory: false,
                  };
                  if (currentWorktree) {
                    deleteWorktree(currentWorktree.id, options);
                  }
                }
              }}
              leftIcon={isDeleting ? <Loader2 className="w-3 h-3 animate-spin" /> : <Archive className="w-3 h-3" />}
              variant="default"
              size="xs"
              disabled={isDeleting || currentWorktree?.is_main}
              className="bg-amber-500/90 hover:bg-amber-500 text-white border-amber-600/50 disabled:opacity-50 disabled:cursor-not-allowed"
              title={currentWorktree?.is_main ? "Cannot delete main worktree" : "Archive worktree"}
            >
              {isDeleting ? 'Archiving...' : 'Archive'}
            </Button>
          </div>
        </div>

        {/* Path Information */}
        <div className="space-y-2 text-xs font-mono">
          <div className="flex items-center gap-2 text-muted-foreground">
            <FolderOpen className="w-3 h-3" />
            <span className="font-medium">Working Directory:</span>
            <span className="bg-muted px-2 py-0.5 rounded font-mono">
              {currentWorktree.path}
            </span>
            <button
              onClick={async () => {
                try {
                  await navigator.clipboard.writeText(currentWorktree.path);
                  setCopiedPath(true);
                  setTimeout(() => setCopiedPath(false), 2000);
                } catch (error) {
                  console.error('Failed to copy path:', error);
                }
              }}
              className="p-1 hover:bg-muted rounded transition-colors"
              title="Copy path to clipboard"
            >
              {copiedPath ? (
                <Check className="w-3 h-3 text-success" />
              ) : (
                <Copy className="w-3 h-3" />
              )}
            </button>
          </div>
        </div>
      </div>

      {/* Content */}
      <div className="flex-1 overflow-y-auto p-6">
        <div className="max-w-4xl mx-auto space-y-8">
          {/* Git Status */}
          <section>
            <h2 className="text-sm font-mono font-semibold mb-4">// git_status</h2>
            <div className="elevation-1 hover:elevation-2 border border-border/40 rounded-lg p-4">
              <GitStatus worktreeId={currentWorktree.id} />
            </div>
          </section>

          {/* Git Operations */}
          <section>
            <h2 className="text-sm font-mono font-semibold mb-4">// operations</h2>
            <div className="elevation-1 hover:elevation-2 border border-border/40 rounded-lg p-4">
              <GitOperations
                worktreeId={currentWorktree.id}
                branch={currentWorktree.branch}
                defaultBranch={currentProject?.default_branch}
                onOperationComplete={() => {
                  if (currentProject) {
                    loadWorktrees(currentProject.id);
                  }
                }}
              />
            </div>
          </section>

          {/* Commit History */}
          <section>
            <h2 className="text-sm font-mono font-semibold mb-4">// commit_history</h2>
            <div className="elevation-1 hover:elevation-2 border border-border/40 rounded-lg p-4">
              <CommitHistory worktreeId={currentWorktree.id} limit={20} />
            </div>
          </section>

          {/* Metadata */}
          <section>
            <h2 className="text-sm font-mono font-semibold mb-4">// metadata</h2>
            <div className="elevation-1 hover:elevation-2 border border-border/40 rounded-lg p-4">
              <dl className="grid grid-cols-2 gap-4 text-xs font-mono">
                <div>
                  <dt className="text-muted-foreground mb-1">ID</dt>
                  <dd className="bg-muted px-2 py-1 rounded">{currentWorktree.id}</dd>
                </div>
                {formatDate(currentWorktree.created_at) && (
                  <div>
                    <dt className="text-muted-foreground mb-1">Created</dt>
                    <dd>{formatDate(currentWorktree.created_at)}</dd>
                  </div>
                )}
                {currentWorktree.session_id && (
                  <div>
                    <dt className="text-muted-foreground mb-1">Session ID</dt>
                    <dd className="bg-muted px-2 py-1 rounded">{currentWorktree.session_id}</dd>
                  </div>
                )}
              </dl>
            </div>
          </section>
        </div>
      </div>

      {/* Delete Worktree Modal */}
      <DeleteWorktreeModal
        isOpen={deleteModalOpen}
        onClose={() => setDeleteModalOpen(false)}
        worktree={currentWorktree}
        chatCount={associatedChatCount}
        onConfirmDelete={(options) => {
          if (currentWorktree) {
            deleteWorktree(currentWorktree.id, options);
          }
        }}
      />
    </div>
  );
}