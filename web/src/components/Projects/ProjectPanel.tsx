import { useEffect, useMemo, useState } from "react";
import { FolderOpen, GitBranch, Terminal, Folder, ArrowLeft, Clock, MessageSquare, AlertCircle, RefreshCw, ExternalLink, Inbox, Trash2 } from "lucide-react";
import { useProjectStore } from "../../store/projectStore";
import { useWorktreeStore } from "../../store/worktreeStore";
import { useChatStore } from "../../store/chatStore";
import { useChatNavigationStore } from "../../store/chatNavigationStore";
import { useChatList } from "../../hooks/chat-queries";
import { cn } from "../../lib/utils";
import { logger } from "../../lib/logger";
import { projectGrpc, type GitInfo as GrpcGitInfo } from "../../api/project-grpc";
import type { Chat } from "../../types/chat";
import { RemoveProjectModal } from "./RemoveProjectModal";
import { WorktreeStatus } from "../../gen/reliant/v1/worktree_pb";

// Re-export the GitInfo type from project-grpc for consistency
type GitInfo = GrpcGitInfo;

interface GitStatus {
  uncommitted_changes: number;
  unpushed_commits: number;
  remote_url?: string;
}

interface ProjectPanelProps {
  onNavigateToProjectPicker?: () => void;
  onNavigateToChats?: () => void;
}

export function ProjectPanel({ onNavigateToProjectPicker, onNavigateToChats }: ProjectPanelProps) {
  const currentProject = useProjectStore((state) => state.currentProject);
  const refreshCurrentProject = useProjectStore((state) => state.refreshCurrentProject);
  const deleteProject = useProjectStore((state) => state.deleteProject);
  const worktrees = useWorktreeStore((state) => state.worktrees);
  const loadWorktrees = useWorktreeStore((state) => state.loadWorktrees);
  const currentWorktree = useWorktreeStore((state) => state.currentWorktree);
  const { data: chats = [] } = useChatList(currentProject?.id);
  const selectChat = useChatStore((state) => state.selectChat);
  const navigateToChat = useChatNavigationStore((state) => state.navigateToChat);
  const switchWorktreeContext = useWorktreeStore((state) => state.switchWorktreeContext);
  const [gitInfo, setGitInfo] = useState<GitInfo | null>(null);
  const [gitStatus, setGitStatus] = useState<GitStatus | null>(null);
  const [isRefreshing, setIsRefreshing] = useState(false);
  const [showRemoveModal, setShowRemoveModal] = useState(false);

  useEffect(() => {
    if (currentProject) {
      logger.info("[ProjectPanel] Current project data:", currentProject);
      refreshCurrentProject();
      loadWorktrees(currentProject.id);
      fetchGitData();
    }
     
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [currentProject?.id]);

  // Calculate worktree statistics
  const worktreeStats = useMemo(() => {
    const active = worktrees.filter((w) => w.status === WorktreeStatus.ACTIVE).length;
    const completed = worktrees.filter((w) => w.status === WorktreeStatus.COMPLETED).length;
    const merging = worktrees.filter((w) => w.status === WorktreeStatus.MERGING).length;
    const abandoned = worktrees.filter((w) => w.status === WorktreeStatus.ABANDONED).length;
    return { active, completed, merging, abandoned, total: worktrees.length };
  }, [worktrees]);

  // Get recent worktrees (last 3 active ones)
  const recentWorktrees = useMemo(() => {
    return [...worktrees]
      .filter((w) => w.status === WorktreeStatus.ACTIVE)
      .sort((a, b) => new Date(b.last_active).getTime() - new Date(a.last_active).getTime())
      .slice(0, 3);
  }, [worktrees]);

  // Get recent chats for this project (last 5)
  const recentChats = useMemo(() => {
    return chats
      .filter(c => c.projectId === currentProject?.id)
      .sort((a, b) => new Date(b.lastActive).getTime() - new Date(a.lastActive).getTime())
      .slice(0, 5);
  }, [chats, currentProject?.id]);

  const fetchGitData = async () => {
    if (!currentProject) return;

    try {
      const data = await projectGrpc.getGitInfo(currentProject.id);
      setGitInfo(data);

      // Extract git status from the response
      if (data.is_git_repo) {
        const totalChanges = 
          (data.staged_files?.length || 0) + 
          (data.unstaged_files?.length || 0) + 
          (data.untracked_files?.length || 0);
        
        setGitStatus({
          uncommitted_changes: totalChanges,
          unpushed_commits: data.ahead || 0,
          remote_url: data.remote_url,
        });
      } else {
        setGitStatus(null);
      }
    } catch (error) {
      logger.error("[ProjectPanel] Failed to fetch git info:", error);
      setGitInfo(null);
      setGitStatus(null);
    }
  };

  const handleRefreshGit = async () => {
    setIsRefreshing(true);
    await fetchGitData();
    setTimeout(() => setIsRefreshing(false), 500);
  };

  const handleWorktreeClick = async (worktree: typeof worktrees[0]) => {
    if (currentProject) {
      await switchWorktreeContext(currentProject.id, worktree);
    }
    // TODO: Navigate to worktrees view if needed
  };

  const handleChatClick = async (chat: Chat) => {
    if (currentProject?.id) {
      if (chat.worktreeId) {
        const targetWorktree = worktrees.find((worktree) => worktree.id === chat.worktreeId) ?? null;
        if (targetWorktree) {
          await switchWorktreeContext(currentProject.id, targetWorktree);
        }
      } else {
        await switchWorktreeContext(currentProject.id, null);
      }
    }

    navigateToChat(chat.id);
    selectChat(chat);
    // Navigate to chats tab
    onNavigateToChats?.();
  };

  const handleOpenTerminal = async () => {
    if (!currentProject?.path) return;

    try {
      if (window.electronAPI?.openTerminal) {
        const result = await window.electronAPI.openTerminal(currentProject.path);
        if (!result.success) {
          logger.error("Failed to open terminal:", result.error);
        }
      } else {
        logger.warn("Terminal opening not available in web environment");
      }
    } catch (error) {
      logger.error("Error opening terminal:", error);
    }
  };

  const handleOpenInFinder = async () => {
    if (!currentProject?.path) return;

    try {
      if (window.electronAPI?.openProjectDirectory) {
        const result = await window.electronAPI.openProjectDirectory(currentProject.path);
        if (!result.success) {
          logger.error("Failed to open project directory:", result.error);
        }
      } else {
        logger.warn("Opening in finder not available in web environment");
      }
    } catch (error) {
      logger.error("Error opening project directory:", error);
    }
  };

  const handleRemoveProject = async () => {
    if (!currentProject) return;

    try {
      await deleteProject(currentProject.id);
      setShowRemoveModal(false);
      onNavigateToProjectPicker?.();
    } catch (error) {
      logger.error("Failed to remove project:", error);
    }
  };

  if (!currentProject) {
    return (
      <div className="flex flex-col items-center justify-center h-full p-6">
        <div className="w-16 h-16 mb-4 rounded-full elevation-1 flex items-center justify-center">
          <FolderOpen className="w-8 h-8 text-muted-foreground" />
        </div>
        <div className="text-sm text-muted-foreground text-center">
          No project selected
        </div>
        {onNavigateToProjectPicker && (
          <button
            onClick={onNavigateToProjectPicker}
            className="mt-4 px-4 py-2 bg-primary text-primary-foreground hover:bg-primary/90 rounded text-sm font-mono transition-colors"
          >
            Select Project
          </button>
        )}
      </div>
    );
  }

  return (
    <div className="h-full overflow-y-auto">
      <div className="max-w-5xl mx-auto p-6 space-y-6">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-3">
            <div className="w-12 h-12 rounded-lg bg-primary/10 flex items-center justify-center">
              <FolderOpen className="w-6 h-6 text-primary" />
            </div>
            <div>
              <h1 className="text-2xl font-mono font-semibold">{currentProject.name}</h1>
              <p className="text-sm text-muted-foreground font-mono">{currentProject.path}</p>
            </div>
          </div>
          {onNavigateToProjectPicker && (
            <button
              onClick={onNavigateToProjectPicker}
              className="px-4 py-2 bg-primary text-primary-foreground hover:bg-primary/90 rounded-lg text-sm font-mono font-semibold transition-all hover:scale-105 active:scale-95 flex items-center gap-2 shadow-md border-2 border-primary-foreground/20"
            >
              <ArrowLeft className="w-4 h-4" />
              Change Project
            </button>
          )}
        </div>

        <div className="flex gap-2">
          <button
            onClick={handleOpenTerminal}
            className="px-3 py-2 elevation-1 hover:elevation-2 border border-border/40 rounded-lg hover:bg-accent hover:border-primary/30 transition-all group flex items-center gap-2"
          >
            <Terminal className="w-4 h-4 text-muted-foreground group-hover:text-primary transition-colors" />
            <span className="text-sm font-mono">Open Terminal</span>
          </button>

          <button
            onClick={handleOpenInFinder}
            className="px-3 py-2 elevation-1 hover:elevation-2 border border-border/40 rounded-lg hover:bg-accent hover:border-primary/30 transition-all group flex items-center gap-2"
          >
            <Folder className="w-4 h-4 text-muted-foreground group-hover:text-primary transition-colors" />
            <span className="text-sm font-mono">Open in Finder</span>
          </button>
        </div>

        <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
          {/* Repository Information */}
          <div className="elevation-1 hover:elevation-2 border border-border/40 rounded-lg p-6 space-y-4 hover:border-primary/20  transition-all">
            <h2 className="text-sm font-mono font-semibold uppercase tracking-wider text-muted-foreground">
              Repository Information
            </h2>
            <div className="space-y-3">
              <div className="flex items-start gap-3">
                <GitBranch className={cn("w-4 h-4 mt-0.5", currentProject.is_git_repo ? "text-success" : "text-muted-foreground")} />
                <div className="flex-1">
                  <div className="text-sm font-mono">
                    {currentProject.is_git_repo ? "Git Repository" : "Not a Git Repository"}
                  </div>
                  {currentProject.is_git_repo && currentProject.default_branch && (
                    <div className="text-xs text-muted-foreground mt-1">
                      Default branch: <span className="font-mono">{currentProject.default_branch}</span>
                    </div>
                  )}
                </div>
              </div>
              {gitStatus?.remote_url && (
                <div className="flex items-start gap-3">
                  <ExternalLink className="w-4 h-4 mt-0.5 text-primary" />
                  <div className="flex-1">
                    <div className="text-sm font-mono">Remote URL</div>
                    <a
                      href={gitStatus.remote_url.replace('git@github.com:', 'https://github.com/').replace('.git', '')}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="text-xs text-primary hover:underline mt-1 font-mono break-all"
                    >
                      {gitStatus.remote_url}
                    </a>
                  </div>
                </div>
              )}
            </div>
          </div>

          {/* Git Status - only show for git repos */}
          {currentProject.is_git_repo && (
            <div className="elevation-1 hover:elevation-2 border border-border/40 rounded-lg p-6 space-y-4 hover:border-primary/20  transition-all">
              <div className="flex items-center justify-between">
                <h2 className="text-sm font-mono font-semibold uppercase tracking-wider text-muted-foreground">
                  Git Status
                </h2>
                <button
                  onClick={handleRefreshGit}
                  disabled={isRefreshing}
                  className="p-1 rounded hover:bg-accent transition-colors disabled:opacity-50"
                  title="Refresh git status"
                >
                  <RefreshCw className={cn("w-3.5 h-3.5 text-muted-foreground", isRefreshing && "animate-spin")} />
                </button>
              </div>
              {gitInfo && gitInfo.is_git_repo ? (
              <div className="space-y-3">
                <div className="flex items-start gap-3">
                  <GitBranch className="w-4 h-4 mt-0.5 text-primary" />
                  <div className="flex-1">
                    <div className="text-sm font-mono">Current Branch</div>
                    <div className="text-xs text-muted-foreground mt-1 font-mono">
                      {gitInfo.current_branch || 'Not on any branch'}
                    </div>
                  </div>
                </div>
                {gitStatus && gitStatus.uncommitted_changes > 0 && (
                  <div className="flex items-start gap-3">
                    <AlertCircle className="w-4 h-4 mt-0.5 text-warning" />
                    <div className="flex-1">
                      <div className="text-sm font-mono">Uncommitted Changes</div>
                      <div className="text-xs text-warning mt-1 font-mono">
                        {gitStatus.uncommitted_changes} file{gitStatus.uncommitted_changes !== 1 ? 's' : ''} modified
                      </div>
                    </div>
                  </div>
                )}
                {gitStatus && gitStatus.unpushed_commits > 0 && (
                  <div className="flex items-start gap-3">
                    <AlertCircle className="w-4 h-4 mt-0.5 text-primary" />
                    <div className="flex-1">
                      <div className="text-sm font-mono">Unpushed Commits</div>
                      <div className="text-xs text-primary mt-1 font-mono">
                        {gitStatus.unpushed_commits} commit{gitStatus.unpushed_commits !== 1 ? 's' : ''} ahead
                      </div>
                    </div>
                  </div>
                )}
              </div>
              ) : (
                <div className="text-xs text-muted-foreground font-mono">Loading git status...</div>
              )}
            </div>
          )}

          {/* Active Worktrees Summary */}
          <div className="elevation-1 hover:elevation-2 border border-border/40 rounded-lg p-6 space-y-4 hover:border-primary/20  transition-all">
            <div className="flex items-center justify-between">
              <h2 className="text-sm font-mono font-semibold uppercase tracking-wider text-muted-foreground">
                Active Worktrees
              </h2>
              <span className="text-lg font-mono font-semibold text-primary">{worktreeStats.total}</span>
            </div>
            <div className="space-y-2">
              <div className="flex items-center justify-between text-xs font-mono">
                <div className="flex items-center gap-2">
                  <div className="w-2 h-2 rounded-full bg-status-active"></div>
                  <span className="text-muted-foreground">Active</span>
                </div>
                <span className="text-success font-semibold">{worktreeStats.active}</span>
              </div>
              <div className="flex items-center justify-between text-xs font-mono">
                <div className="flex items-center gap-2">
                  <div className="w-2 h-2 rounded-full bg-status-merging"></div>
                  <span className="text-muted-foreground">Merging</span>
                </div>
                <span className="text-muted-foreground font-semibold">{worktreeStats.merging}</span>
              </div>
              <div className="flex items-center justify-between text-xs font-mono">
                <div className="flex items-center gap-2">
                  <div className="w-2 h-2 rounded-full bg-status-completed"></div>
                  <span className="text-muted-foreground">Completed</span>
                </div>
                <span className="text-muted-foreground font-semibold">{worktreeStats.completed}</span>
              </div>
              <div className="flex items-center justify-between text-xs font-mono">
                <div className="flex items-center gap-2">
                  <div className="w-2 h-2 rounded-full bg-status-abandoned"></div>
                  <span className="text-muted-foreground">Abandoned</span>
                </div>
                <span className="text-muted-foreground font-semibold">{worktreeStats.abandoned}</span>
              </div>
            </div>
            {recentWorktrees.length > 0 ? (
              <div className="pt-4 border-t border-border space-y-3">
                <div className="text-xs font-mono font-semibold text-muted-foreground uppercase tracking-wider">Recently Active</div>
                {recentWorktrees.map((wt) => {
                  const isSelected = currentWorktree?.id === wt.id;
                  return (
                    <button
                      key={wt.id}
                      onClick={() => handleWorktreeClick(wt)}
                      style={isSelected ? {
                        backgroundColor: 'hsl(var(--primary) / 0.1)',
                        borderColor: 'hsl(var(--primary))',
                        borderWidth: '2px'
                      } : undefined}
                      className={cn(
                        "w-full p-3 rounded-lg transition-all group border-2",
                        isSelected
                          ? ""
                          : "bg-card border-transparent hover:bg-accent"
                      )}
                    >
                      <div className="flex items-center gap-2.5">
                        <GitBranch className={cn(
                          "w-4 h-4 shrink-0 transition-colors",
                          isSelected ? "text-primary" : "text-muted-foreground group-hover:text-primary"
                        )} />
                        <div className="flex-1 min-w-0 text-left">
                          <div className={cn(
                            "text-sm font-mono font-semibold truncate transition-colors",
                            isSelected ? "text-primary" : "text-foreground group-hover:text-primary"
                          )}>
                            {wt.branch}
                          </div>
                          <div className="flex items-center gap-1.5 mt-1">
                            <span className="text-xs text-muted-foreground">from</span>
                            <span className={cn(
                              "text-xs font-mono px-1.5 py-0.5 rounded",
                              isSelected
                                ? "bg-primary/30 text-primary font-semibold"
                                : "bg-muted text-muted-foreground"
                            )}>
                              {wt.base_branch}
                            </span>
                          </div>
                        </div>
                        {isSelected && (
                          <div className="w-2 h-2 rounded-full bg-primary animate-pulse shrink-0"></div>
                        )}
                      </div>
                    </button>
                  );
                })}
              </div>
            ) : worktreeStats.total === 0 ? (
              <div className="pt-3 border-t border-border flex flex-col items-center justify-center py-4 text-center">
                <Inbox className="w-8 h-8 text-muted-foreground/30 mb-2" />
                <p className="text-xs text-muted-foreground">No workspaces yet</p>
                <p className="text-xs text-muted-foreground/60 mt-1">Create a workspace to start working on a new feature</p>
              </div>
            ) : null}
          </div>

          {/* Recent Activity */}
          <div className="elevation-1 hover:elevation-2 border border-border/40 rounded-lg p-6 space-y-4 hover:border-primary/20  transition-all">
            <h2 className="text-sm font-mono font-semibold uppercase tracking-wider text-muted-foreground">
              Recent Activity
            </h2>
            <div className="space-y-3">
              <div className="flex items-start gap-3">
                <MessageSquare className="w-4 h-4 mt-0.5 text-primary" />
                <div className="flex-1">
                  <div className="text-sm font-mono">Chat Sessions</div>
                  <div className="text-xs text-muted-foreground mt-1">
                    {recentChats.length} active conversation{recentChats.length !== 1 ? 's' : ''}
                  </div>
                </div>
              </div>
              {recentChats.length > 0 ? (
                <div className="pl-7 space-y-1.5">
                  {recentChats.map((chat) => (
                    <button
                      key={chat.id}
                      onClick={() => handleChatClick(chat)}
                      className="w-full text-left text-xs font-mono p-1.5 rounded hover:bg-accent/50 transition-colors group"
                    >
                      <div className="flex items-center gap-2">
                        <Clock className="w-3 h-3 text-muted-foreground group-hover:text-primary transition-colors" />
                        <span className="flex-1 truncate group-hover:text-primary transition-colors">{chat.title}</span>
                      </div>
                      <div className="text-muted-foreground/60 text-xs ml-5">
                        {new Date(chat.lastActive).toLocaleDateString()}
                      </div>
                    </button>
                  ))}
                </div>
              ) : (
                <div className="pl-7 flex flex-col items-center justify-center py-4 text-center">
                  <MessageSquare className="w-8 h-8 text-muted-foreground/30 mb-2" />
                  <p className="text-xs text-muted-foreground">No recent chats</p>
                  <p className="text-xs text-muted-foreground/60 mt-1">Start a conversation to see it here</p>
                </div>
              )}
            </div>
          </div>
        </div>

        <div className="border-t border-border pt-6 mt-8 flex justify-end">
          <button
            onClick={() => setShowRemoveModal(true)}
            className="px-3 py-2 elevation-1 hover:elevation-2 border border-border/40 rounded-lg hover:bg-destructive/10 hover:border-destructive/30 transition-all group flex items-center gap-2"
          >
            <Trash2 className="w-4 h-4 text-muted-foreground group-hover:text-destructive transition-colors" />
            <span className="text-sm font-mono group-hover:text-destructive transition-colors">Remove Project</span>
          </button>
        </div>
      </div>

      <RemoveProjectModal
        isOpen={showRemoveModal}
        onClose={() => setShowRemoveModal(false)}
        onConfirm={handleRemoveProject}
        project={currentProject}
      />
    </div>
  );
}