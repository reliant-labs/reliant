import { useState, useEffect, useRef, useMemo } from "react";
import { X, File, Plus, Minus, Loader2, Undo2, Check, GitPullRequest, ArrowUp, ArrowDown } from "lucide-react";
import { worktreeGrpc } from "../../api/worktree-grpc";
import { projectGrpc, type FileChange as GrpcFileChange } from "../../api/project-grpc";
import { FileIcon } from "../ui/FileIcon";
import { useViewerStore } from "../../store/viewerStore";
import { useProjectStore } from "../../store/projectStore";
import { cn } from "../../lib/utils";
import * as gitApi from "../../api/git";
import { PRDialog } from "../SourceControl/PRDialog";
import { GitNotInitialized } from "../Git/GitNotInitialized";
import { logger } from "../../lib/logger";
import { matchesRefetchScope, subscribeToRefetch } from "../../store/refetchStore";
import { Tooltip } from "../ui/Tooltip";
import {
  SidebarSection,
  SidebarEmptyState,
  SidebarInput,
} from "../RightSidebar/shared";
import { FileChangeStatus } from "../../gen/reliant/v1/common_pb";

export interface FileChange {
  path: string;
  status: FileChangeStatus;
  diff?: string;
  content?: string;
  original_content?: string;
  is_new: boolean;
}

interface RecentChangesData {
  branch: string;
  files: FileChange[];
  total_files: number;
  ahead: number;
  behind: number;
  default_branch: string;  // Repository's default branch (for PR targeting)
}

interface RecentChangesProps {
  worktreeId?: string;
  projectId: string;
  onClose: () => void;
  inline?: boolean;
  onFileSelect?: (file: FileChange | null) => void;
}

// Cache for git status data to prevent unnecessary reloads
const gitStatusCache = new Map<string, { data: RecentChangesData; timestamp: number }>();
const CACHE_TTL = 5000; // 5 second cache TTL

function getCacheKey(worktreeId: string | undefined, projectId: string): string {
  return worktreeId ? `worktree:${worktreeId}` : `project:${projectId}`;
}

function getCachedData(worktreeId: string | undefined, projectId: string): RecentChangesData | null {
  const key = getCacheKey(worktreeId, projectId);
  const cached = gitStatusCache.get(key);
  if (!cached) return null;
  
  // Check if cache is still valid
  const age = Date.now() - cached.timestamp;
  if (age > CACHE_TTL) {
    gitStatusCache.delete(key);
    return null;
  }
  
  return cached.data;
}

function setCachedData(worktreeId: string | undefined, projectId: string, data: RecentChangesData): void {
  const key = getCacheKey(worktreeId, projectId);
  gitStatusCache.set(key, { data, timestamp: Date.now() });
}

function invalidateCache(worktreeId: string | undefined, projectId: string): void {
  const key = getCacheKey(worktreeId, projectId);
  gitStatusCache.delete(key);
}

// Preload function that can be called from outside the component
export async function preloadGitStatus(worktreeId: string | undefined, projectId: string, isGitRepo: boolean): Promise<void> {
  if (!isGitRepo || (!worktreeId && !projectId)) {
    return;
  }

  // Check if we already have cached data
  const cached = getCachedData(worktreeId, projectId);
  if (cached) {
    return; // Already cached, no need to preload
  }

  try {
    let changesData: RecentChangesData;
    
    if (worktreeId) {
      const grpcData = await worktreeGrpc.getChanges(worktreeId);
      changesData = {
        branch: grpcData.branch,
        files: grpcData.files.map((f): FileChange => ({
          path: f.path,
          status: f.status,
          diff: f.diff,
          is_new: f.is_new,
        })),
        total_files: grpcData.total_files,
        ahead: grpcData.ahead,
        behind: grpcData.behind,
        default_branch: grpcData.default_branch,
      };
    } else {
      const grpcChangesData = await projectGrpc.getChanges(projectId);
      const currentProject = useProjectStore.getState().currentProject;
      changesData = {
        branch: grpcChangesData.branch,
        files: grpcChangesData.files.map((f: GrpcFileChange): FileChange => ({
          path: f.path,
          status: f.status,
          diff: f.diff,
          content: f.content,
          original_content: f.original_content,
          is_new: f.is_new,
        })),
        total_files: grpcChangesData.total_files,
        ahead: 0,
        behind: 0,
        default_branch: currentProject?.default_branch ?? "",
      };
    }
    
    // Store in cache
    setCachedData(worktreeId, projectId, changesData);
    logger.debug("[RecentChanges] Preloaded git status", { worktreeId, projectId, fileCount: changesData.total_files });
  } catch (err) {
    // Silently fail - preloading shouldn't break the app
    logger.debug("[RecentChanges] Failed to preload git status", err);
  }
}

export function RecentChanges({ worktreeId, projectId, onClose, inline = false, onFileSelect }: RecentChangesProps) {
  const [data, setData] = useState<RecentChangesData | null>(null);
  const [loading, setLoading] = useState(true);
  
  // Track previous props to detect workspace changes
  const prevWorktreeIdRef = useRef<string | undefined>(worktreeId);
  const prevProjectIdRef = useRef<string>(projectId);
  const [error, setError] = useState<string | null>(null);
  const openDiffViewer = useViewerStore((state) => state.openDiffViewer);
  const currentProject = useProjectStore((state) => state.currentProject);
  const [expandedSections, setExpandedSections] = useState({
    staged: true,
    modified: true,
    untracked: true,
  });
  const [stagingFiles, setStagingFiles] = useState<Set<string>>(new Set());
  const [revertingFiles, setRevertingFiles] = useState<Set<string>>(new Set());
  const [isPRDialogOpen, setIsPRDialogOpen] = useState(false);
  const [prRefreshTrigger, setPrRefreshTrigger] = useState(0);
  const [selectedFile, setSelectedFile] = useState<{ path: string; status: string } | null>(null);
  const [selectedIndex, setSelectedIndex] = useState<number>(-1);
  const [selectedIndices, setSelectedIndices] = useState<Set<number>>(new Set());
  const [anchorIndex, setAnchorIndex] = useState<number>(-1);
  const fileListRef = useRef<HTMLDivElement>(null);
  const selectedFileRef = useRef<HTMLDivElement>(null);
  const loadRequestIdRef = useRef(0);
  const prRequestIdRef = useRef(0);
  const scopeKeyRef = useRef(`${worktreeId ?? "project"}:${projectId}`);

  // Commit-related state
  const [commitMessage, setCommitMessage] = useState("");
  const [isCommitting, setIsCommitting] = useState(false);
  const [existingPR, setExistingPR] = useState<gitApi.ExistingPRResponse | null>(null);
  const [_isCheckingPR, setIsCheckingPR] = useState(false);
  const [ghCliMissing, setGhCliMissing] = useState(false);
  const [isSyncing, setIsSyncing] = useState(false);

  const isDefaultBranch = !!data?.default_branch && data?.branch === data.default_branch;
  const prDisabled = isSyncing || isCommitting || ghCliMissing || isDefaultBranch;
  const prTooltip = ghCliMissing
    ? "GitHub CLI (gh) not installed. Install from cli.github.com"
    : isDefaultBranch
      ? "Cannot create PR from the default branch"
      : "Create pull request";

  // Check if project is a git repo
  const isGitRepo = currentProject?.is_git_repo ?? true; // Default to true to avoid flicker



  // Check for existing PR when worktree changes
  useEffect(() => {
    if (!worktreeId) {
      prRequestIdRef.current += 1;
      setExistingPR(null);
      setGhCliMissing(false);
      setIsCheckingPR(false);
      return;
    }

    const requestId = ++prRequestIdRef.current;
    const currentWorktreeId = worktreeId;

    const checkExistingPR = async () => {
      setIsCheckingPR(true);
      setGhCliMissing(false);
      try {
        const prInfo = await gitApi.getExistingPR(currentWorktreeId);
        if (prRequestIdRef.current !== requestId || worktreeId !== currentWorktreeId) {
          return;
        }
        setExistingPR(prInfo);
        if (prInfo.exists) {
          logger.info("Found existing PR for worktree", { url: prInfo.url, state: prInfo.state });
        }
      } catch (err) {
        if (prRequestIdRef.current !== requestId || worktreeId !== currentWorktreeId) {
          return;
        }
        const errorMessage = err instanceof Error ? err.message : String(err);
        if (errorMessage.includes("GitHub CLI (gh) is not installed")) {
          setGhCliMissing(true);
          logger.info("GitHub CLI not installed - PR features disabled");
        } else {
          logger.debug("Could not check for existing PR", err);
        }
        setExistingPR(null);
      } finally {
        if (prRequestIdRef.current === requestId && worktreeId === currentWorktreeId) {
          setIsCheckingPR(false);
        }
      }
    };

    void checkExistingPR();
  }, [worktreeId, prRefreshTrigger]);

  // Handle workspace changes - immediately reset state when worktreeId or projectId changes
  useEffect(() => {
    const worktreeChanged = prevWorktreeIdRef.current !== worktreeId;
    const projectChanged = prevProjectIdRef.current !== projectId;
    const nextScopeKey = `${worktreeId ?? "project"}:${projectId}`;

    if (worktreeChanged || projectChanged) {
      logger.debug("[RecentChanges] Workspace changed", {
        prevWorktree: prevWorktreeIdRef.current,
        newWorktree: worktreeId,
        prevProject: prevProjectIdRef.current,
        newProject: projectId,
      });

      loadRequestIdRef.current += 1;
      prRequestIdRef.current += 1;
      scopeKeyRef.current = nextScopeKey;

      // Immediately clear stale data and show loading
      setData(null);
      setLoading(true);
      setError(null);
      setExistingPR(null);
      setGhCliMissing(false);
      setIsCheckingPR(false);

      // Reset selection state
      setSelectedFile(null);
      setSelectedIndex(-1);
      setSelectedIndices(new Set());
      setAnchorIndex(-1);

      // Update refs
      prevWorktreeIdRef.current = worktreeId;
      prevProjectIdRef.current = projectId;
    }
  }, [worktreeId, projectId]);

  useEffect(() => {
    // Don't load changes if not a git repo
    if (!isGitRepo) {
      setLoading(false);
      return;
    }

    // Check if we have cached data first (for same workspace)
    const cached = getCachedData(worktreeId, projectId);
    if (cached) {
      // Use cached data immediately - no loading state
      setData(cached);
      setLoading(false);
      setError(null);
    } else {
      // No cache - load it
      setLoading(true);
      setError(null);
      loadChanges(true);
    }

    // Subscribe to refetch events (fast path: backend signals after tool calls)
    // Debounced so rapid-fire tool completions collapse into a single call
    let debounceTimer: ReturnType<typeof setTimeout> | null = null;
    const unsubscribe = subscribeToRefetch("worktree_changes", (event) => {
      if (!matchesRefetchScope(event, { worktreeId, projectId })) {
        return;
      }
      if (debounceTimer) clearTimeout(debounceTimer);
      debounceTimer = setTimeout(() => {
        debounceTimer = null;
        void loadChanges(false);
      }, 500);
    });

    // Slow fallback poll to catch external filesystem changes
    // (user editing in another editor, git pull, etc.)
    const fallbackInterval = setInterval(() => {
      loadChanges(false);
    }, 30_000);

    return () => {
      unsubscribe();
      clearInterval(fallbackInterval);
      if (debounceTimer) clearTimeout(debounceTimer);
    };
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [worktreeId, projectId, isGitRepo]);

  const loadChanges = async (isInitial: boolean = false) => {
    const requestId = ++loadRequestIdRef.current;
    const currentWorktreeId = worktreeId;
    const currentProjectId = projectId;
    const currentScopeKey = `${currentWorktreeId ?? "project"}:${currentProjectId}`;
    const projectDefaultBranch = currentProject?.default_branch ?? "";

    try {
      setError(null);
      // Only show loading spinner on initial load when we don't have data
      if (isInitial) {
        setLoading(true);
      }

      // If worktreeId is provided, use gRPC for worktree changes
      // Otherwise use gRPC for project changes
      let changesData: RecentChangesData;

      if (currentWorktreeId) {
        const grpcData = await worktreeGrpc.getChanges(currentWorktreeId);
        // Convert gRPC response to RecentChangesData format
        changesData = {
          branch: grpcData.branch,
          files: grpcData.files.map((f): FileChange => ({
            path: f.path,
            status: f.status,
            diff: f.diff,
            is_new: f.is_new,
          })),
          total_files: grpcData.total_files,
          ahead: grpcData.ahead,
          behind: grpcData.behind,
          default_branch: grpcData.default_branch,
        };
      } else {
        const grpcChangesData = await projectGrpc.getChanges(currentProjectId);
        // Convert gRPC response to RecentChangesData format
        changesData = {
          branch: grpcChangesData.branch,
          files: grpcChangesData.files.map((f: GrpcFileChange): FileChange => ({
            path: f.path,
            status: f.status,
            diff: f.diff,
            content: f.content,
            original_content: f.original_content,
            is_new: f.is_new,
          })),
          total_files: grpcChangesData.total_files,
          ahead: 0,  // Not included in gRPC response for project changes
          behind: 0, // Not included in gRPC response for project changes
          default_branch: projectDefaultBranch, // Use project's default branch
        };
      }

      if (
        loadRequestIdRef.current !== requestId ||
        scopeKeyRef.current !== currentScopeKey ||
        worktreeId !== currentWorktreeId ||
        projectId !== currentProjectId
      ) {
        logger.debug("[RecentChanges] Ignoring stale changes response", {
          requestId,
          currentRequestId: loadRequestIdRef.current,
          currentScopeKey,
          latestScopeKey: scopeKeyRef.current,
        });
        return;
      }

      // Update state and cache
      setData(changesData);
      setCachedData(currentWorktreeId, currentProjectId, changesData);
    } catch (err) {
      if (
        loadRequestIdRef.current !== requestId ||
        scopeKeyRef.current !== currentScopeKey ||
        worktreeId !== currentWorktreeId ||
        projectId !== currentProjectId
      ) {
        return;
      }
      const errorMessage = err instanceof Error ? err.message : "Failed to load changes";
      console.error("[RecentChanges] Failed to load recent changes:", {
        error: err,
        message: errorMessage,
        projectId: currentProjectId,
        worktreeId: currentWorktreeId,
      });
      setError(errorMessage);
    } finally {
      if (
        loadRequestIdRef.current === requestId &&
        scopeKeyRef.current === currentScopeKey &&
        worktreeId === currentWorktreeId &&
        projectId === currentProjectId
      ) {
        // Always clear loading state for the active request
        setLoading(false);
      }
    }
  };

  const toggleSection = (section: keyof typeof expandedSections) => {
    setExpandedSections((prev) => ({ ...prev, [section]: !prev[section] }));
  };

  // Commit handler
  const handleCommit = async () => {
    if (!worktreeId || !commitMessage.trim()) return;

    const stagedFilesCount =
      data?.files.filter((f) => f.status === FileChangeStatus.STAGED).length || 0;
    if (stagedFilesCount === 0) {
      setError("No files staged for commit");
      return;
    }

    setIsCommitting(true);
    setError(null);

    try {
      await gitApi.commitChanges(worktreeId, commitMessage.trim());
      setCommitMessage("");
      invalidateCache(worktreeId, projectId);
      await loadChanges(false);
      logger.info("Changes committed successfully");
    } catch (err) {
      const errorMessage = err instanceof Error ? err.message : "Failed to commit changes";
      setError(errorMessage);
      logger.error("Failed to commit changes", err);
    } finally {
      setIsCommitting(false);
    }
  };

  const handlePush = async () => {
    if (!worktreeId) return;

    setIsSyncing(true);
    setError(null);

    try {
      await gitApi.pushChanges(worktreeId);
      invalidateCache(worktreeId, projectId);
      await loadChanges(false);
      logger.info("Changes pushed successfully");
    } catch (err) {
      const errorMessage = err instanceof Error ? err.message : "Failed to push changes";
      setError(errorMessage);
      logger.error("Failed to push changes", err);
    } finally {
      setIsSyncing(false);
    }
  };

  const handlePull = async () => {
    if (!worktreeId) return;

    setIsSyncing(true);
    setError(null);

    try {
      await gitApi.pullChanges(worktreeId);
      invalidateCache(worktreeId, projectId);
      await loadChanges(false);
      logger.info("Changes pulled successfully");
    } catch (err) {
      const errorMessage = err instanceof Error ? err.message : "Failed to pull changes";
      setError(errorMessage);
      logger.error("Failed to pull changes", err);
    } finally {
      setIsSyncing(false);
    }
  };

  const handleStageFile = async (filePath: string, e: React.MouseEvent) => {
    e.stopPropagation();
    if (!worktreeId) return;

    setStagingFiles((prev) => new Set(prev).add(filePath));
    try {
      await gitApi.stageFiles(worktreeId, [filePath]);
      invalidateCache(worktreeId, projectId);
      await loadChanges(false);
      logger.info("File staged successfully:", filePath);
    } catch (err) {
      logger.error("Failed to stage file:", err);
    } finally {
      setStagingFiles((prev) => {
        const next = new Set(prev);
        next.delete(filePath);
        return next;
      });
    }
  };

  const handleUnstageFile = async (filePath: string, e: React.MouseEvent) => {
    e.stopPropagation();
    if (!worktreeId) return;

    setStagingFiles((prev) => new Set(prev).add(filePath));
    try {
      await gitApi.unstageFiles(worktreeId, [filePath]);
      invalidateCache(worktreeId, projectId);
      await loadChanges(false);
      logger.info("File unstaged successfully:", filePath);
    } catch (err) {
      logger.error("Failed to unstage file:", err);
    } finally {
      setStagingFiles((prev) => {
        const next = new Set(prev);
        next.delete(filePath);
        return next;
      });
    }
  };

  const handleRevertFile = async (
    filePath: string,
    status: "staged" | "modified" | "untracked",
    e: React.MouseEvent,
  ) => {
    e.stopPropagation();
    if (!worktreeId) return;

    const confirmMessage =
      status === "untracked"
        ? `Delete file "${filePath}"?\n\nThis will permanently delete this untracked file. This cannot be undone.`
        : `Discard changes for "${filePath}"?\n\nThis will revert the file to its last committed state. This cannot be undone.`;

    if (!window.confirm(confirmMessage)) return;

    setError(null);
    setRevertingFiles((prev) => new Set(prev).add(filePath));
    try {
      const result = await gitApi.revertFiles(worktreeId, [filePath]);
      if (result.message?.includes("error(s):")) {
        setError(result.message);
        logger.warn("File revert completed with errors:", { filePath, message: result.message });
      } else {
        setError(null);
        logger.info("File reverted successfully:", filePath);
      }
      invalidateCache(worktreeId, projectId);
      await loadChanges(false);
    } catch (err) {
      const errorMessage = err instanceof Error ? err.message : "Failed to revert file";
      setError(errorMessage);
      logger.error("Failed to revert file:", err);
    } finally {
      setRevertingFiles((prev) => {
        const next = new Set(prev);
        next.delete(filePath);
        return next;
      });
    }
  };

  const handleStageAll = async (files: FileChange[], e: React.MouseEvent) => {
    e.stopPropagation();
    if (!worktreeId || files.length === 0) return;

    const filePaths = files.map((f) => f.path);
    filePaths.forEach((path) => setStagingFiles((prev) => new Set(prev).add(path)));

    try {
      await gitApi.stageFiles(worktreeId, filePaths);
      invalidateCache(worktreeId, projectId);
      await loadChanges(false);
      logger.info("All files staged successfully");
    } catch (err) {
      logger.error("Failed to stage all files:", err);
    } finally {
      filePaths.forEach((path) =>
        setStagingFiles((prev) => {
          const next = new Set(prev);
          next.delete(path);
          return next;
        })
      );
    }
  };

  const handleUnstageAll = async (files: FileChange[], e: React.MouseEvent) => {
    e.stopPropagation();
    if (!worktreeId || files.length === 0) return;

    const filePaths = files.map((f) => f.path);
    filePaths.forEach((path) => setStagingFiles((prev) => new Set(prev).add(path)));

    try {
      await gitApi.unstageFiles(worktreeId, filePaths);
      invalidateCache(worktreeId, projectId);
      await loadChanges(false);
      logger.info("All files unstaged successfully");
    } catch (err) {
      logger.error("Failed to unstage all files:", err);
    } finally {
      filePaths.forEach((path) =>
        setStagingFiles((prev) => {
          const next = new Set(prev);
          next.delete(path);
          return next;
        })
      );
    }
  };

  const handleDiscardAllChanges = async (
    files: FileChange[],
    status: "modified" | "untracked",
    e: React.MouseEvent,
  ) => {
    e.stopPropagation();
    if (!worktreeId || files.length === 0) return;

    const fileCountLabel = files.length === 1 ? "1 file" : `${files.length} files`;
    const confirmMessage =
      status === "untracked"
        ? `Delete all untracked files?\n\nThis will permanently delete ${fileCountLabel}. This cannot be undone.`
        : `Discard all changes?\n\nThis will revert ${fileCountLabel} to their last committed state. This cannot be undone.`;

    if (!window.confirm(confirmMessage)) return;

    const filePaths = files.map((f) => f.path);
    setError(null);
    filePaths.forEach((path) => setRevertingFiles((prev) => new Set(prev).add(path)));

    try {
      const result = await gitApi.revertFiles(worktreeId, filePaths);
      if (result.message?.includes("error(s):")) {
        setError(result.message);
        logger.warn("Discard all completed with errors", { message: result.message, files: filePaths });
      } else {
        setError(null);
        logger.info("All changes discarded successfully");
      }
      invalidateCache(worktreeId, projectId);
      await loadChanges(false);
    } catch (err) {
      const errorMessage = err instanceof Error ? err.message : "Failed to discard all changes";
      setError(errorMessage);
      logger.error("Failed to discard all changes:", err);
    } finally {
      filePaths.forEach((path) =>
        setRevertingFiles((prev) => {
          const next = new Set(prev);
          next.delete(path);
          return next;
        })
      );
    }
  };

  // Bulk action handlers for selected files
  const handleStageSelected = async (e?: React.MouseEvent) => {
    e?.stopPropagation();
    if (!worktreeId || selectedIndices.size === 0) return;

    const filePaths = Array.from(selectedIndices)
      .map((idx) => {
        const fileData = visibleFiles[idx];
        if (!fileData) return null;
        if (
          fileData.status === "modified" ||
          fileData.status === "untracked"
        ) {
          return fileData.file.path;
        }
        return null;
      })
      .filter((path): path is string => path !== null);

    if (filePaths.length === 0) return;

    filePaths.forEach((path) => setStagingFiles((prev) => new Set(prev).add(path)));
    try {
      await gitApi.stageFiles(worktreeId, filePaths);
      invalidateCache(worktreeId, projectId);
      await loadChanges(false);
      setSelectedIndices(new Set());
      logger.info("Selected files staged successfully");
    } catch (err) {
      logger.error("Failed to stage selected files:", err);
    } finally {
      filePaths.forEach((path) =>
        setStagingFiles((prev) => {
          const next = new Set(prev);
          next.delete(path);
          return next;
        })
      );
    }
  };

  const handleUnstageSelected = async (e?: React.MouseEvent) => {
    e?.stopPropagation();
    if (!worktreeId || selectedIndices.size === 0) return;

    const filePaths = Array.from(selectedIndices)
      .map((idx) => {
        const fileData = visibleFiles[idx];
        if (!fileData || fileData.status !== "staged") return null;
        return fileData.file.path;
      })
      .filter((path): path is string => path !== null);

    if (filePaths.length === 0) return;

    filePaths.forEach((path) => setStagingFiles((prev) => new Set(prev).add(path)));
    try {
      await gitApi.unstageFiles(worktreeId, filePaths);
      invalidateCache(worktreeId, projectId);
      await loadChanges(false);
      setSelectedIndices(new Set());
      logger.info("Selected files unstaged successfully");
    } catch (err) {
      logger.error("Failed to unstage selected files:", err);
    } finally {
      filePaths.forEach((path) =>
        setStagingFiles((prev) => {
          const next = new Set(prev);
          next.delete(path);
          return next;
        })
      );
    }
  };

  const handleDiscardSelected = async (e?: React.MouseEvent) => {
    e?.stopPropagation();
    if (!worktreeId || selectedIndices.size === 0) return;

    const selectedFiles = Array.from(selectedIndices)
      .map((idx) => visibleFiles[idx])
      .filter((item): item is { file: FileChange; status: string } => item !== undefined);

    const filePaths = selectedFiles.map((item) => item.file.path);
    if (filePaths.length === 0) return;

    const untrackedCount = selectedFiles.filter((item) => item.status === "untracked").length;
    const trackedCount = filePaths.length - untrackedCount;
    const fileCountLabel = filePaths.length === 1 ? "1 file" : `${filePaths.length} files`;

    let confirmMessage = `Discard selected changes for ${fileCountLabel}?\n\nThis cannot be undone.`;
    if (untrackedCount > 0 && trackedCount > 0) {
      confirmMessage = `Discard selected changes for ${fileCountLabel}?\n\nThis will revert ${trackedCount} tracked ${trackedCount === 1 ? "file" : "files"} and permanently delete ${untrackedCount} untracked ${untrackedCount === 1 ? "file" : "files"}. This cannot be undone.`;
    } else if (untrackedCount > 0) {
      confirmMessage = `Delete ${fileCountLabel}?\n\nThis will permanently delete ${fileCountLabel}. This cannot be undone.`;
    } else {
      confirmMessage = `Discard selected changes for ${fileCountLabel}?\n\nThis will revert ${fileCountLabel} to their last committed state. This cannot be undone.`;
    }

    if (!window.confirm(confirmMessage)) return;

    setError(null);
    filePaths.forEach((path) => setRevertingFiles((prev) => new Set(prev).add(path)));
    try {
      const result = await gitApi.revertFiles(worktreeId, filePaths);
      if (result.message?.includes("error(s):")) {
        setError(result.message);
        logger.warn("Discard selected completed with errors", { message: result.message, files: filePaths });
      } else {
        setError(null);
        logger.info("Selected files discarded successfully");
      }
      invalidateCache(worktreeId, projectId);
      await loadChanges(false);
      setSelectedIndices(new Set());
    } catch (err) {
      const errorMessage = err instanceof Error ? err.message : "Failed to discard selected files";
      setError(errorMessage);
      logger.error("Failed to discard selected files:", err);
    } finally {
      filePaths.forEach((path) =>
        setRevertingFiles((prev) => {
          const next = new Set(prev);
          next.delete(path);
          return next;
        })
      );
    }
  };

  // Calculate file lists and visible files for keyboard navigation
  const stagedFiles = useMemo(
    () => data?.files.filter((f) => f.status === FileChangeStatus.STAGED) || [],
    [data?.files],
  );
  const modifiedFiles = useMemo(
    () => data?.files.filter((f) => f.status === FileChangeStatus.MODIFIED) || [],
    [data?.files],
  );
  const untrackedFiles = useMemo(
    () => data?.files.filter((f) => f.status === FileChangeStatus.UNTRACKED) || [],
    [data?.files],
  );

  // Create a flat list of all visible files for keyboard navigation
  const visibleFiles = useMemo(() => {
    const files: Array<{ file: FileChange; status: string }> = [];
    if (expandedSections.staged) {
      stagedFiles.forEach(file => files.push({ file, status: "staged" }));
    }
    if (expandedSections.modified) {
      modifiedFiles.forEach(file => files.push({ file, status: "modified" }));
    }
    if (expandedSections.untracked) {
      untrackedFiles.forEach(file => files.push({ file, status: "untracked" }));
    }
    return files;
  }, [expandedSections.staged, expandedSections.modified, expandedSections.untracked, stagedFiles, modifiedFiles, untrackedFiles]);

  // Helper function to select a range of indices
  const selectRange = (start: number, end: number) => {
    const min = Math.min(start, end);
    const max = Math.max(start, end);
    const newIndices = new Set<number>();
    for (let i = min; i <= max; i++) {
      if (i >= 0 && i < visibleFiles.length) {
        newIndices.add(i);
      }
    }
    setSelectedIndices(newIndices);
  };

  // Scroll selected file into view
  useEffect(() => {
    if (selectedFileRef.current) {
      selectedFileRef.current.scrollIntoView({
        behavior: "smooth",
        block: "nearest",
      });
    }
  }, [selectedIndex]);

  // Reset selected index when files change
  useEffect(() => {
    if (selectedIndex >= visibleFiles.length) {
      setSelectedIndex(Math.max(0, visibleFiles.length - 1));
    }
    // Sync selectedFile with selectedIndex if they're out of sync
    if (selectedIndex >= 0 && selectedIndex < visibleFiles.length) {
      const { file, status } = visibleFiles[selectedIndex];
      if (selectedFile?.path !== file.path || selectedFile?.status !== status) {
        setSelectedFile({ path: file.path, status });
      }
    }
    // Clean up selected indices that are out of bounds
    if (visibleFiles.length > 0) {
      setSelectedIndices((prev) => {
        const next = new Set(prev);
        let changed = false;
        prev.forEach((idx) => {
          if (idx >= visibleFiles.length) {
            next.delete(idx);
            changed = true;
          }
        });
        return changed ? next : prev;
      });
    } else {
      setSelectedIndices(new Set());
    }
  }, [visibleFiles, selectedIndex, selectedFile?.path, selectedFile?.status]);

  // Keyboard navigation handler
  const handleKeyDown = (e: React.KeyboardEvent) => {
    // Don't handle keyboard navigation if user is typing in an input
    const target = e.target as HTMLElement;
    if (target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.isContentEditable) {
      return;
    }

    if (visibleFiles.length === 0) return;

    const isShiftPressed = e.shiftKey;

    if (e.key === "ArrowDown") {
      e.preventDefault();
      setSelectedIndex((prev) => {
        const next = prev < visibleFiles.length - 1 ? prev + 1 : prev;
        if (next !== prev) {
          const { file, status } = visibleFiles[next];
          setSelectedFile({ path: file.path, status });
          
          if (isShiftPressed) {
            // Extend selection
            const anchor = anchorIndex >= 0 ? anchorIndex : prev;
            selectRange(anchor, next);
            setAnchorIndex(anchor);
          } else {
            // Single selection
            setSelectedIndices(new Set([next]));
            setAnchorIndex(next);
          }
        }
        return next;
      });
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setSelectedIndex((prev) => {
        const next = prev > 0 ? prev - 1 : 0;
        if (next !== prev) {
          const { file, status } = visibleFiles[next];
          setSelectedFile({ path: file.path, status });
          
          if (isShiftPressed) {
            // Extend selection
            const anchor = anchorIndex >= 0 ? anchorIndex : prev;
            selectRange(anchor, next);
            setAnchorIndex(anchor);
          } else {
            // Single selection
            setSelectedIndices(new Set([next]));
            setAnchorIndex(next);
          }
        }
        return next;
      });
    } else if (e.key === "Enter") {
      // Open all selected files (or just the current one if none selected)
      const indicesToOpen = selectedIndices.size > 0 ? Array.from(selectedIndices) : 
                           (selectedIndex >= 0 ? [selectedIndex] : []);
      
      if (indicesToOpen.length > 0) {
        e.preventDefault();
        indicesToOpen.forEach((idx) => {
          if (idx >= 0 && idx < visibleFiles.length) {
            const { file } = visibleFiles[idx];
            openDiffViewer(file, projectId);
            if (inline && onFileSelect) {
              onFileSelect(file);
            }
          }
        });
      }
    } else if (e.key === "Escape") {
      // Clear selection
      setSelectedIndices(new Set());
      setAnchorIndex(-1);
    } else if ((e.metaKey || e.ctrlKey) && selectedIndices.size > 0 && worktreeId) {
      // Bulk actions with Cmd/Ctrl
      if (e.key === "s" || e.key === "S") {
        e.preventDefault();
        handleStageSelected();
      } else if (e.key === "u" || e.key === "U") {
        e.preventDefault();
        handleUnstageSelected();
      } else if (e.key === "d" || e.key === "D") {
        e.preventDefault();
        handleDiscardSelected();
      }
    }
  };

  const renderFileList = (files: FileChange[], status: string) => {
    if (files.length === 0) return null;

    const sectionKey = status as keyof typeof expandedSections;
    const isExpanded = expandedSections[sectionKey];
    const isStaged = status === "staged";
    const canStage = status === "modified" || status === "untracked";

    // Display title: "modified" -> "changes", others keep as-is
    const displayTitle = status === "modified" ? "changes" : status;

    return (
      <SidebarSection
        title={displayTitle}
        isExpanded={isExpanded}
        onToggle={() => toggleSection(sectionKey)}
        enableHeaderHoverBg
        actions={
          <div className="flex items-center gap-1">
            {worktreeId && (
              <>
                {(status === "modified" || status === "untracked") && (
                  <Tooltip content={status === "untracked" ? "Delete all untracked files" : "Discard all changes"}>
                    <button
                      onClick={(e) =>
                        handleDiscardAllChanges(
                          files,
                          status === "untracked" ? "untracked" : "modified",
                          e,
                        )
                      }
                      className="px-2.5 py-1.5 rounded-md transition-all text-muted-foreground hover:text-foreground flex items-center justify-center hover:bg-accent"
                    >
                      <Undo2 className="w-4 h-4" />
                    </button>
                  </Tooltip>
                )}
                {canStage && (
                  <Tooltip content="Stage all">
                    <button
                      onClick={(e) => handleStageAll(files, e)}
                      className="p-1 hover:bg-accent rounded transition-all text-muted-foreground hover:text-foreground"
                    >
                      <Plus className="w-3.5 h-3.5" />
                    </button>
                  </Tooltip>
                )}
                {isStaged && (
                  <Tooltip content="Unstage all">
                    <button
                      onClick={(e) => handleUnstageAll(files, e)}
                      className="p-1 hover:bg-accent rounded transition-all text-muted-foreground hover:text-foreground"
                    >
                      <Minus className="w-3.5 h-3.5" />
                    </button>
                  </Tooltip>
                )}
              </>
            )}
            <span className="text-xs text-muted-foreground px-2">
              {files.length}
            </span>
          </div>
        }
        className="border-b-0 mb-1"
        headerClassName="bg-muted/30"
      >
        <div className="space-y-0.5 px-1">
          {files.map((file, index) => {
            const isProcessing = stagingFiles.has(file.path) || revertingFiles.has(file.path);

            // Allow independent selection in each section (staged vs modified)
            const isSelected = selectedFile?.path === file.path && selectedFile?.status === status;

            // Check if this file matches the selected index
            const fileIndex = visibleFiles.findIndex(
              (f) => f.file.path === file.path && f.status === status
            );
            const isKeyboardSelected = fileIndex === selectedIndex;
            const isMultiSelected = selectedIndices.has(fileIndex);
            const isAnySelected = isSelected || isKeyboardSelected || isMultiSelected;
            const shouldKeepActionsVisible = isProcessing || isMultiSelected || selectedIndices.size > 1;

            return (
              <div
                key={`${status}-${file.path}-${index}`}
                ref={isKeyboardSelected ? selectedFileRef : undefined}
                className={cn(
                  "relative flex items-center gap-2 w-full px-2 py-1.5 text-sm rounded-md transition-all duration-150 group/item cursor-pointer overflow-hidden",
                  "text-foreground/70 hover:text-foreground",
                  isProcessing && "opacity-50"
                )}
                style={{
                  backgroundColor: isAnySelected
                    ? "hsl(var(--tab-active) / 0.2)"
                    : undefined,
                }}
                onMouseEnter={(e) => {
                  if (!isAnySelected) {
                    const element = e.currentTarget;
                    const computedStyle = getComputedStyle(document.documentElement);
                    const mutedColor = computedStyle.getPropertyValue('--muted').trim();
                    element.style.backgroundColor = mutedColor ? `hsl(${mutedColor})` : '';
                  }
                }}
                onMouseLeave={(e) => {
                  if (!isAnySelected) {
                    e.currentTarget.style.backgroundColor = '';
                  }
                }}
                onClick={(e) => {
                  if (fileIndex < 0) return;

                  if (e.shiftKey && anchorIndex >= 0) {
                    // Shift+click: extend selection range
                    selectRange(anchorIndex, fileIndex);
                    setSelectedIndex(fileIndex);
                    setSelectedFile({ path: file.path, status });
                  } else if (e.metaKey || e.ctrlKey) {
                    // Cmd/Ctrl+click: toggle selection
                    setSelectedIndices((prev) => {
                      const next = new Set(prev);
                      if (next.has(fileIndex)) {
                        next.delete(fileIndex);
                      } else {
                        next.add(fileIndex);
                      }
                      return next;
                    });
                    setSelectedIndex(fileIndex);
                    setSelectedFile({ path: file.path, status });
                    setAnchorIndex(fileIndex);
                  } else {
                    // Regular click: single selection
                    setSelectedIndices(new Set([fileIndex]));
                    setSelectedIndex(fileIndex);
                    setSelectedFile({ path: file.path, status });
                    setAnchorIndex(fileIndex);

                    // Also open the diff on single click anywhere on the row.
                    // (Previously only the filename button opened, which felt like
                    // needing to click twice depending on where you clicked.)
                    openDiffViewer(file, projectId);
                    if (inline && onFileSelect) {
                      onFileSelect(file);
                    }
                  }
                }}
              >
                <button
                  onClick={(e) => {
                    e.stopPropagation();
                    // Keep selection state consistent with what we're opening.
                    // (Otherwise the selectedIndex-sync effect will "snap" highlight back.)
                    setSelectedIndices(new Set(fileIndex >= 0 ? [fileIndex] : []));
                    setSelectedIndex(fileIndex);
                    setAnchorIndex(fileIndex);
                    setSelectedFile({ path: file.path, status });

                    // If multiple files are selected, open all of them, but ensure
                    // the file the user clicked becomes the active/focused tab by
                    // opening it last.
                    if (selectedIndices.size > 1 && fileIndex >= 0) {
                      const ordered = Array.from(selectedIndices)
                        .filter((idx) => idx >= 0 && idx < visibleFiles.length && idx !== fileIndex);
                      ordered.push(fileIndex);

                      ordered.forEach((idx) => {
                        const { file: f } = visibleFiles[idx];
                        openDiffViewer(f, projectId);
                        if (inline && onFileSelect) {
                          onFileSelect(f);
                        }
                      });
                      return;
                    }

                    // Default: open just the clicked file
                    {
                      // Keep selection state consistent with what we're opening.
                      // (Otherwise the selectedIndex-sync effect will "snap" highlight back.)
                      openDiffViewer(file, projectId);
                      if (inline && onFileSelect) {
                        onFileSelect(file);
                      }
                    }
                  }}
                  className={cn(
                    "flex items-center gap-2 w-full min-w-0 text-left hover:bg-transparent transition-[padding] duration-150",
                    worktreeId && (shouldKeepActionsVisible ? "pr-20" : "group-hover/item:pr-20")
                  )}
                  disabled={isProcessing}
                >
                  <FileIcon fileName={file.path} className="w-4 h-4 flex-shrink-0" />
                  <span className="font-mono text-xs truncate">
                    {file.path.split('/').pop()}
                  </span>
                  {file.path.includes('/') && (
                    <span className="text-[10px] text-muted-foreground truncate flex-1 min-w-0">
                      {file.path.substring(0, file.path.lastIndexOf('/'))}
                    </span>
                  )}
                </button>

                {worktreeId && (
                  <div
                    className={cn(
                      "absolute right-2 top-1/2 -translate-y-1/2 flex items-center gap-0.5 transition-opacity duration-150",
                      shouldKeepActionsVisible
                        ? "opacity-100 pointer-events-auto"
                        : "opacity-0 pointer-events-none group-hover/item:opacity-100 group-hover/item:pointer-events-auto"
                    )}
                  >
                    {isProcessing ? (
                      <Loader2 className="w-3.5 h-3.5 animate-spin text-muted-foreground" />
                    ) : (
                      <>
                        {canStage && (
                          <Tooltip content={selectedIndices.size > 1 ? `Stage ${selectedIndices.size} files` : "Stage file"}>
                            <button
                              onClick={(e) => {
                                if (selectedIndices.size > 1) {
                                  handleStageSelected(e);
                                } else {
                                  handleStageFile(file.path, e);
                                }
                              }}
                              className="p-1.5 rounded transition-all text-muted-foreground hover:text-foreground hover:bg-accent"
                            >
                              <Plus className="w-3.5 h-3.5" />
                            </button>
                          </Tooltip>
                        )}
                        {isStaged && (
                          <Tooltip content={selectedIndices.size > 1 ? `Unstage ${selectedIndices.size} files` : "Unstage file"}>
                            <button
                              onClick={(e) => {
                                if (selectedIndices.size > 1) {
                                  handleUnstageSelected(e);
                                } else {
                                  handleUnstageFile(file.path, e);
                                }
                              }}
                              className="p-1.5 rounded transition-all text-muted-foreground hover:text-foreground hover:bg-accent"
                            >
                              <Minus className="w-3.5 h-3.5" />
                            </button>
                          </Tooltip>
                        )}
                        <Tooltip content={
                          selectedIndices.size > 1
                            ? `Discard changes for ${selectedIndices.size} files`
                            : isStaged
                              ? "Unstage changes"
                              : status === "untracked"
                                ? "Delete file"
                                : "Discard changes"
                        }>
                          <button
                            onClick={(e) => {
                              if (selectedIndices.size > 1) {
                                handleDiscardSelected(e);
                              } else {
                                handleRevertFile(
                                  file.path,
                                  status === "untracked"
                                    ? "untracked"
                                    : isStaged
                                      ? "staged"
                                      : "modified",
                                  e,
                                );
                              }
                            }}
                            className="p-1.5 rounded transition-all text-muted-foreground hover:text-foreground hover:bg-accent"
                          >
                            <Undo2 className="w-3.5 h-3.5" />
                          </button>
                        </Tooltip>
                      </>
                    )}
                  </div>
                )}
              </div>
            );
          })}
        </div>
      </SidebarSection>
    );
  };

  // Note: Inline file viewer removed - files now open in tabbed viewer panel

  // Show git initialization prompt if project is not a git repo
  if (!isGitRepo && currentProject) {
    return (
      <div className={cn(
        "flex flex-col",
        inline ? "h-96" : "h-full bg-card border-l border-border"
      )}>
        {!inline && (
          <div className="flex items-center justify-between p-4 border-b border-border">
            <div>
              <h3 className="text-sm font-semibold">Changes</h3>
              <p className="text-xs text-muted-foreground mt-0.5">
                Source control
              </p>
            </div>
            <button
              onClick={onClose}
              className="p-1 hover:bg-muted rounded transition-colors"
            >
              <X className="w-4 h-4" />
            </button>
          </div>
        )}
        <GitNotInitialized
          projectId={projectId}
          projectName={currentProject.name}
          className="flex-1"
        />
      </div>
    );
  }


  if (error) {
    return (
      <div className={cn(
        "flex flex-col",
        inline ? "h-96" : "h-full bg-card border-l border-border"
      )}>
        {!inline && (
          <div className="flex items-center justify-between p-4 border-b border-border">
            <h3 className="text-sm font-semibold">Recent Changes</h3>
            <button
              onClick={onClose}
              className="p-1 hover:bg-muted rounded transition-colors"
            >
              <X className="w-4 h-4" />
            </button>
          </div>
        )}
        <div className="flex items-center justify-center flex-1">
          <div className="text-sm text-destructive">{error}</div>
        </div>
      </div>
    );
  }

  return (
    <div className={cn(
      "flex flex-col h-full",
      !inline && "bg-card border-l border-border"
    )}>
      {/* Header */}
      {!inline && (
        <div className="flex items-center justify-between p-4 border-b border-border">
          <div>
            <div className="flex items-center gap-2">
              <h3 className="text-sm font-semibold">Recent Changes</h3>
              {selectedIndices.size > 0 && (
                <span className="text-xs bg-primary/20 text-primary px-2 py-0.5 rounded-full">
                  {selectedIndices.size} selected
                </span>
              )}
            </div>
            <p className="text-xs text-muted-foreground mt-0.5">
              {data?.branch} • {data?.total_files || 0} files changed
            </p>
          </div>
          <button
            onClick={onClose}
            className="p-1 hover:bg-muted rounded transition-colors"
          >
            <X className="w-4 h-4" />
          </button>
        </div>
      )}

      {/* Inline header with commit input and actions - full width design */}
      {inline && worktreeId && (
        <div className="flex flex-col gap-2 p-2 bg-background/95 border-b border-border">
          {/* Error Display */}
          {error && (
            <div className="text-xs text-destructive bg-destructive/10 px-2 py-1 rounded border border-destructive/20">
              {error}
            </div>
          )}

          {/* Commit Input - full width */}
          <SidebarInput
            placeholder={`Message (⌘⏎ to commit on "${data?.branch || 'main'}")`}
            value={commitMessage}
            onChange={(value) => {
              setCommitMessage(value);
              setError(null);
            }}
            onKeyDown={(e) => {
              if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
                e.preventDefault();
                if (stagedFiles.length > 0) {
                  handleCommit();
                }
              }
            }}
            showClear={false}
            rightContent={
              commitMessage ? (
                <span className="text-xs text-muted-foreground">
                  {commitMessage.length}
                </span>
              ) : undefined
            }
            disabled={isCommitting || isSyncing}
            wrapperClassName="w-full"
          />

          {/* Determine button state */}
          {(() => {
            const hasAnyChanges = stagedFiles.length > 0 || modifiedFiles.length > 0 || untrackedFiles.length > 0;
            const showPushMode = !hasAnyChanges && (data?.ahead ?? 0) > 0;
            const showPullMode = !hasAnyChanges && (data?.behind ?? 0) > 0 && (data?.ahead ?? 0) === 0;

            let buttonState;
            if (showPullMode) {
              buttonState = {
                label: `Pull (${data?.behind ?? 0})`,
                icon: ArrowDown,
                onClick: handlePull,
                disabled: isSyncing || (data?.behind ?? 0) === 0,
                loading: isSyncing,
                title: `Pull ${data?.behind ?? 0} commit${(data?.behind ?? 0) === 1 ? '' : 's'}`,
              };
            } else if (showPushMode) {
              buttonState = {
                label: "Push",
                icon: ArrowUp,
                onClick: handlePush,
                disabled: isSyncing || (data?.ahead ?? 0) === 0,
                loading: isSyncing,
                title: `Push ${data?.ahead ?? 0} commit${(data?.ahead ?? 0) === 1 ? '' : 's'}`,
              };
            } else {
              buttonState = {
                label: "Commit",
                icon: Check,
                onClick: handleCommit,
                disabled: !commitMessage.trim() || stagedFiles.length === 0 || isCommitting,
                loading: isCommitting,
                title: `Commit ${stagedFiles.length} ${stagedFiles.length === 1 ? 'file' : 'files'} (⌘+Enter)`,
              };
            }

            const ButtonIcon = buttonState.icon;

            return (
              <div className="flex flex-col gap-2 w-full">
                {/* Main Action Button - Commit/Push/Pull */}
                <Tooltip content={buttonState.title}>
                  <button
                    onClick={buttonState.onClick}
                    disabled={buttonState.disabled}
                    style={{
                      backgroundColor: 'hsl(var(--primary))',
                      color: 'hsl(var(--primary-foreground))',
                      borderColor: 'hsl(var(--primary) / 0.2)',
                    }}
                    className={cn(
                      "w-full h-9 px-3 rounded-md text-sm font-medium transition-all flex items-center justify-center gap-1.5",
                      "border hover:opacity-90 hover:brightness-95",
                      "disabled:opacity-50 disabled:cursor-not-allowed",
                      buttonState.loading && "opacity-70"
                    )}
                  >
                    {buttonState.loading ? (
                      <>
                        <Loader2 className="w-4 h-4 animate-spin" />
                        <span>{buttonState.label === "Commit" ? "Committing" : buttonState.label === "Push" ? "Pushing" : "Pulling"}...</span>
                      </>
                    ) : (
                      <>
                        <ButtonIcon className="w-4 h-4" />
                        <span>{buttonState.label}</span>
                      </>
                    )}
                  </button>
                </Tooltip>

                {/* PR button - full width below */}
                {existingPR?.exists && existingPR.url ? (
                  <Tooltip content={`View PR #${existingPR.number}: ${existingPR.title}`}>
                    <a
                      href={existingPR.url}
                      target="_blank"
                      rel="noopener noreferrer"
                      className={cn(
                        "w-full h-9 rounded-md text-sm font-medium transition-all flex items-center justify-center gap-1.5",
                        "border border-primary/40 bg-primary/10 hover:bg-primary/20 text-primary"
                      )}
                    >
                      <GitPullRequest className="w-3.5 h-3.5" />
                      <span className="truncate">PR #{existingPR.number}</span>
                    </a>
                  </Tooltip>
                ) : (
                  <Tooltip content={prTooltip}>
                    <button
                      onClick={prDisabled ? undefined : () => setIsPRDialogOpen(true)}
                      disabled={prDisabled}
                      className={cn(
                        "w-full h-9 rounded-md text-sm font-medium transition-all flex items-center justify-center gap-1.5",
                        "border border-border bg-background btn-hover-bg-muted hover:border-primary/40 text-foreground",
                        prDisabled && "opacity-50 cursor-not-allowed"
                      )}
                    >
                      <GitPullRequest className="w-3.5 h-3.5" />
                      <span>Create PR</span>
                    </button>
                  </Tooltip>
                )}
              </div>
            );
          })()}
        </div>
      )}

      {/* Error display for inline mode */}
      {inline && error && (
        <div className="px-3 py-1.5 text-xs text-destructive bg-destructive/10 border-b border-destructive/20">
          {error}
        </div>
      )}

      {/* Content - File list only, files open in tabbed viewer */}
      <div 
        className="flex-1 overflow-y-auto"
        ref={fileListRef}
        onKeyDown={handleKeyDown}
        tabIndex={0}
      >
        <div className="p-2">
          {loading && !data ? (
            <div className="flex items-center justify-center py-8">
              <Loader2 className="w-5 h-5 animate-spin text-muted-foreground" />
            </div>
          ) : (
            <>
              {renderFileList(stagedFiles, "staged")}
              {renderFileList(modifiedFiles, "modified")}
              {renderFileList(untrackedFiles, "untracked")}

              {data?.total_files === 0 && (
                <SidebarEmptyState
                  icon={File}
                  title="No changes"
                  description="Working tree is clean"
                />
              )}
            </>
          )}
        </div>
      </div>

      {/* PR Dialog */}
      {worktreeId && (
        <PRDialog
          isOpen={isPRDialogOpen}
          onClose={() => setIsPRDialogOpen(false)}
          onPRCreated={() => setPrRefreshTrigger(prev => prev + 1)}
          worktreeId={worktreeId}
          defaultBranch={data?.default_branch}
          currentBranch={data?.branch}
        />
      )}
    </div>
  );
}
