import { useState, useEffect, useMemo, useCallback, useRef } from "react";
import { useShallow } from "zustand/react/shallow";
import { Files, GitBranch, ListTodo, Check, Terminal, Globe } from "lucide-react";
import { FileTree, type FileTreeHandle } from "./FileTree";
import { FileTreeToolbar } from "./FileTreeToolbar";
import { useProjectStore } from "../../store/projectStore";
import { useViewerStore } from "../../store/viewerStore";
import { useChatStore } from "../../store/chatStore";
import { useWorktreeStore } from "../../store/worktreeStore";
import { useUIStore } from "../../store/uiStore";
import { useBrowserStore } from "../../store/browserStore";
import { useUnifiedProcessCounts } from "../../hooks/useUnifiedProcesses";
import { RecentChanges } from "../Chat/RecentChanges";
import { TasksPanel } from "../Chat/TasksPanel";
import { CommandsViewerTab } from "../PackageCommands/CommandsViewerTab";
import { BrowserSidebarContent } from "../Browser/BrowserSidebarContent";
import { useTasksStore } from "../../store/tasksStore";
import { useCurrentWorktreeState, useWorkspaceStateStore, type RightSidebarTab } from "../../store/workspaceStateStore";
import { useFileClipboardStore } from "../../store/fileClipboardStore";
import { useFileDeletionStore } from "../../store/fileDeletionStore";
import { copyFile, moveFile, copyDirectoryRecursive, createFolder, deleteFileOrFolder } from "../../api/fileSystem";
import { toast } from "../../lib/toast-manager";
import { Tooltip } from "../ui/Tooltip";
import { cn } from "../../lib/utils";
import { logger } from "../../lib/logger";
import { useGitStatusRefreshTrigger } from "../../store/gitStatusStore";
import type { FileNode } from "./index";

interface RightSidebarProps {
  /** When provided, clicking the active tab again will close the sidebar */
  onCloseSidebar?: () => void;
}

export function RightSidebar({ onCloseSidebar }: RightSidebarProps = {}) {
  const currentProject = useProjectStore((state) => state.currentProject);
  const currentWorktree = useWorktreeStore((state) => state.currentWorktree);
  
  // Get the persisted sidebar tab from workspace state store
  const worktreeState = useCurrentWorktreeState(
    currentProject?.id ?? null,
    currentWorktree?.id ?? null
  );
  const activeSidebarTab = worktreeState.rightSidebarTab;
  const setRightSidebarTab = useWorkspaceStateStore((state) => state.setRightSidebarTab);
  
  // Memoized callback to update sidebar tab
  const setActiveSidebarTab = useCallback(
    (tab: RightSidebarTab) => {
      if (currentProject?.id) {
        setRightSidebarTab(currentProject.id, currentWorktree?.id ?? null, tab);
      }
    },
    [currentProject?.id, currentWorktree?.id, setRightSidebarTab]
  );
  const [treeKey, setTreeKey] = useState<number>(0);
  const [collapseKey, setCollapseKey] = useState<number>(0);
  const [creatingType, setCreatingType] = useState<"file" | "folder" | null>(null);

  // Lifted state for file tree persistence across tab switches
  const [focusedPath, setFocusedPath] = useState<string | null>(null);
  const [expandedPaths, setExpandedPaths] = useState<Set<string>>(new Set());
  const fileTreeRef = useRef<FileTreeHandle>(null);
  
  // Track if we should auto-focus the tree (when Files tab becomes active)
  const [shouldAutoFocus, setShouldAutoFocus] = useState(false);
  const prevSidebarTabRef = useRef(activeSidebarTab);

  // Get showHidden from UI store (persistent across sessions)
  const showHidden = useUIStore((state) => state.showHiddenFiles);

  // Subscribe to git status refresh trigger to update file tree when files change
  const gitRefreshTrigger = useGitStatusRefreshTrigger();

  const openFileViewer = useViewerStore((state) => state.openFileViewer);
  const setCurrentProject = useViewerStore((state) => state.setCurrentProject);
  const activeViewer = useViewerStore((state) => state.getActiveViewer());

  // Get active chat's worktree, with fallback to pendingNewChatWorktreeId, then currentWorktree for new chat view
  // Use useShallow to prevent infinite re-renders from object selector
  const { activeChatId, pendingNewChatWorktreeId, chats } = useChatStore(useShallow((state) => ({
    activeChatId: state.activeChatId,
    pendingNewChatWorktreeId: state.pendingNewChatWorktreeId,
    chats: state.chats,
  })));
  const worktrees = useWorktreeStore((state) => state.worktrees);
  const mainWorktree = worktrees.find(w => w.is_main);
  const activeChat = activeChatId ? chats.get(activeChatId) ?? null : null;
  // Use chat's worktree if available, then pendingNewChatWorktreeId (for new chat in specific workspace),
  // otherwise fall back to currentWorktree, then main worktree
  const activeWorktreeId = activeChat?.worktreeId || pendingNewChatWorktreeId || currentWorktree?.id || mainWorktree?.id;
  
  // Unified running count from deduplicated process stores
  const { currentWorkspaceRunning: totalRunningCount } = useUnifiedProcessCounts(activeWorktreeId);

  // Browser tabs count for the current worktree
  const browserTabs = useBrowserStore((state) => state.tabs);
  const worktreeBrowserTabs = activeWorktreeId 
    ? browserTabs.filter(t => t.worktreeId === activeWorktreeId)
    : [];

  // Get task stats for the current chat - use activeChatId directly from chatStore for consistency
  // Subscribe to the entire tasksByChat object to ensure re-renders on any task updates
  // Then derive tasks for the specific chatId (same pattern as TasksPanel)
  const tasksByChat = useTasksStore((state) => state.tasksByChat);
  const taskStats = useMemo(() => {
    const tasks = activeChatId && tasksByChat[activeChatId] 
      ? Object.values(tasksByChat[activeChatId]) 
      : [];
    return {
      total: tasks.length,
      completed: tasks.filter((t) => t.status === "completed").length,
      inProgress: tasks.filter((t) => t.status === "in_progress").length,
      pending: tasks.filter((t) => t.status === "pending").length,
    };
  }, [activeChatId, tasksByChat]);

  // Detect when Files tab becomes active to auto-focus
  useEffect(() => {
    if (activeSidebarTab === "files" && prevSidebarTabRef.current !== "files") {
      // Files tab just became active - trigger auto-focus
      setShouldAutoFocus(true);
    }
    prevSidebarTabRef.current = activeSidebarTab;
  }, [activeSidebarTab]);

  // Sync file tree focus when active viewer changes to a file
  useEffect(() => {
    if (activeViewer?.type === "file" && activeSidebarTab === "files") {
      const filePath = activeViewer.file.path;
      // Expand parents to make file visible
      setExpandedPaths((prev) => {
        const newExpanded = new Set(prev);
        let parentPath = filePath.substring(0, filePath.lastIndexOf('/'));
        let changed = false;
        while (parentPath) {
          if (!newExpanded.has(parentPath)) {
            newExpanded.add(parentPath);
            changed = true;
          }
          const lastSlash = parentPath.lastIndexOf('/');
          parentPath = lastSlash > 0 ? parentPath.substring(0, lastSlash) : '';
        }
        return changed ? newExpanded : prev;
      });
      setFocusedPath(filePath);
      
      // Scroll to the file after React renders (instant, no animation)
      setTimeout(() => {
        const element = document.querySelector(`[data-path="${CSS.escape(filePath)}"]`);
        element?.scrollIntoView({ block: "nearest" });
      }, 0);
    }
  }, [activeViewer?.id, activeViewer?.type, activeViewer?.file?.path, activeSidebarTab]);

  // Handle arrow key navigation from file viewer
  useEffect(() => {
    if (activeSidebarTab !== "files") return;

    const handleFileTreeNavigate = (event: CustomEvent<{ direction: 'up' | 'down'; filePath: string }>) => {
      const { direction, filePath } = event.detail;
      
      // Get the file tree ref to access its methods
      if (!fileTreeRef.current) return;

      // Dispatch a custom event that FileTree can listen to
      // We'll need to pass the current focused path and direction
      window.dispatchEvent(new CustomEvent('file-tree-keyboard-navigate', {
        detail: { direction, currentPath: focusedPath || filePath }
      }));
    };

    window.addEventListener('file-tree-navigate', handleFileTreeNavigate as EventListener);
    return () => {
      window.removeEventListener('file-tree-navigate', handleFileTreeNavigate as EventListener);
    };
  }, [activeSidebarTab, focusedPath]);

  // Clear auto-focus flag after the tree has had a chance to use it
  useEffect(() => {
    if (shouldAutoFocus) {
      const timer = setTimeout(() => setShouldAutoFocus(false), 100);
      return () => clearTimeout(timer);
    }
  }, [shouldAutoFocus]);

  // Reset state when project or worktree changes
  useEffect(() => {
    if (currentProject?.id) {
      setCurrentProject(currentProject.id);
    }
    // Reset file tree state on project/worktree change
    setFocusedPath(null);
    setExpandedPaths(new Set());
    // Note: showHidden is now persisted in UI store and won't reset on project change
    setTreeKey((prev) => prev + 1); // Force FileTree to remount
  }, [currentProject?.id, activeWorktreeId, setCurrentProject]);

  // Listen for file operation undo events to refresh the tree and expand paths
  useEffect(() => {
    const handleFileOperationUndone = (e: CustomEvent<{ path: string }>) => {
      if (activeSidebarTab !== "files") {
        return;
      }

      const restoredPath = e.detail.path;
      
      // Expand parent directories to make restored file/folder visible
      setExpandedPaths((prev) => {
        const newExpanded = new Set(prev);
        let parentPath = restoredPath.substring(0, restoredPath.lastIndexOf("/"));
        let changed = false;
        while (parentPath) {
          if (!newExpanded.has(parentPath)) {
            newExpanded.add(parentPath);
            changed = true;
          }
          const lastSlash = parentPath.lastIndexOf("/");
          parentPath = lastSlash > 0 ? parentPath.substring(0, lastSlash) : "";
        }
        return changed ? newExpanded : prev;
      });

      // Set focus to the restored file/folder
      setFocusedPath(restoredPath);

      // Refresh the file tree silently
      if (fileTreeRef.current) {
        fileTreeRef.current.refresh();
      }
    };

    window.addEventListener("file-deletion-undone", handleFileOperationUndone as EventListener);
    window.addEventListener("file-move-undone", handleFileOperationUndone as EventListener);
    window.addEventListener("file-copy-undone", handleFileOperationUndone as EventListener);
    return () => {
      window.removeEventListener("file-deletion-undone", handleFileOperationUndone as EventListener);
      window.removeEventListener("file-move-undone", handleFileOperationUndone as EventListener);
      window.removeEventListener("file-copy-undone", handleFileOperationUndone as EventListener);
    };
  }, [activeSidebarTab]);


  // Refresh file tree when git status changes (file operations trigger this)
  useEffect(() => {
    if (gitRefreshTrigger > 0 && fileTreeRef.current) {
      logger.debug("[RightSidebar] Git status changed, refreshing file tree", { gitRefreshTrigger });
      fileTreeRef.current.refresh();
    }
  }, [gitRefreshTrigger]);

  const handleFileSelect = (file: FileNode) => {
    if (file.type === "file" && currentProject?.id) {
      openFileViewer(file, currentProject.id, undefined, false); // Click - don't auto-focus
    }
  };

  // Handle cut/copy/paste keyboard shortcuts at the file tree level
  useEffect(() => {
    if (activeSidebarTab !== "files") return;

    const handleCut = () => {
      const { setClipboard } = useFileClipboardStore.getState();
      // Prioritize focused path (what user is navigating to) over active viewer (open file)
      if (focusedPath) {
        // Check if focused path is in expanded paths (indicates it's a directory)
        const isDir = expandedPaths.has(focusedPath);
        const fileName = focusedPath.split("/").pop() || focusedPath;
        setClipboard("cut", focusedPath, fileName, isDir, activeWorktreeId, currentProject?.id);
        toast.notify(`${isDir ? "Directory" : "File"} cut`, { description: fileName });
      } else {
        // Fall back to active viewer if no focused path
        const activeViewer = useViewerStore.getState().getActiveViewer();
        if (activeViewer?.type === "file") {
          setClipboard("cut", activeViewer.file.path, activeViewer.file.name, false, activeViewer.worktreeId, activeViewer.projectId);
          toast.notify("File cut", { description: activeViewer.file.name });
        }
      }
    };

    const handleCopy = () => {
      const { setClipboard } = useFileClipboardStore.getState();
      // Prioritize focused path (what user is navigating to) over active viewer (open file)
      if (focusedPath) {
        // Check if focused path is in expanded paths (indicates it's a directory)
        const isDir = expandedPaths.has(focusedPath);
        const fileName = focusedPath.split("/").pop() || focusedPath;
        setClipboard("copy", focusedPath, fileName, isDir, activeWorktreeId, currentProject?.id);
        toast.notify(`${isDir ? "Directory" : "File"} copied`, { description: fileName });
      } else {
        // Fall back to active viewer if no focused path
        const activeViewer = useViewerStore.getState().getActiveViewer();
        if (activeViewer?.type === "file") {
          setClipboard("copy", activeViewer.file.path, activeViewer.file.name, false, activeViewer.worktreeId, activeViewer.projectId);
          toast.notify("File copied", { description: activeViewer.file.name });
        }
      }
    };

    const handlePaste = async () => {
      const clipboardState = useFileClipboardStore.getState();
      const { operation, filePath, fileName, isDirectory: clipboardIsDirectory, worktreeId: clipboardWorktreeId, projectId: clipboardProjectId, clearClipboard, isPasting, setPasting } = clipboardState;
      
      // Prevent multiple simultaneous pastes
      if (isPasting) {
        return;
      }
      
      if (!operation || !filePath || !fileName) {
        toast.notify("Nothing to paste", { description: "Cut or copy a file or directory first" });
        return;
      }

      // Set paste flag immediately to prevent duplicates
      setPasting(true);

      // Use focused path as destination, or find parent of focused file
      let destinationDir = "";
      if (focusedPath) {
        // Check if focused path is a directory (in expanded paths or no extension)
        const isFocusedDir = expandedPaths.has(focusedPath);
        const lastSlash = focusedPath.lastIndexOf("/");
        const afterLastSlash = focusedPath.substring(lastSlash + 1);
        const hasExtension = afterLastSlash.includes(".");
        
        if (isFocusedDir || (!hasExtension && afterLastSlash)) {
          // It's a directory
          destinationDir = focusedPath;
        } else {
          // It's a file, use its parent directory
          destinationDir = focusedPath.substring(0, lastSlash) || "/";
        }
      } else {
        // No focused path, use root
        destinationDir = "/";
      }

      if (clipboardProjectId !== currentProject?.id) {
        toast.notify("Cannot paste", { description: "Item is from a different project" });
        setPasting(false);
        return;
      }

      const destinationPath = destinationDir === "/" ? `/${fileName}` : `${destinationDir}/${fileName}`;
      
      if (filePath === destinationPath) {
        toast.notify("Cannot paste", { description: "Item is already in this location" });
        setPasting(false);
        return;
      }

      try {
        const { addMovedFile, addCopiedFile } = useFileDeletionStore.getState();
        
        if (operation === "cut") {
          // For directories, we need to recursively move all files
          // The moveFile function should handle this, but let's check if it's a directory
          if (clipboardIsDirectory) {
            // For directories, we need to use a recursive approach
            // First create the destination directory, then move all contents
            await createFolder(destinationPath, clipboardWorktreeId || activeWorktreeId);
            // Then recursively copy all files from source to destination
            await copyDirectoryRecursive(filePath, destinationPath, clipboardWorktreeId || activeWorktreeId);
            // Then delete the source directory
            await deleteFileOrFolder(filePath, clipboardWorktreeId || activeWorktreeId);
          } else {
            await moveFile(filePath, destinationPath, clipboardWorktreeId || activeWorktreeId);
          }
          // Track move operation for undo
          if (currentProject?.id) {
            addMovedFile({
              sourcePath: filePath,
              destinationPath: destinationPath,
              fileName: fileName,
              worktreeId: clipboardWorktreeId || activeWorktreeId,
              projectId: currentProject.id,
              movedAt: Date.now(),
            });
          }
          toast.notify(`${clipboardIsDirectory ? "Directory" : "File"} moved`, { description: fileName });
        } else {
          // For directories, we need to recursively copy all files
          if (clipboardIsDirectory) {
            // Create the destination directory first
            await createFolder(destinationPath, clipboardWorktreeId || activeWorktreeId);
            // Then recursively copy all files from source to destination
            await copyDirectoryRecursive(filePath, destinationPath, clipboardWorktreeId || activeWorktreeId);
          } else {
            await copyFile(filePath, destinationPath, clipboardWorktreeId || activeWorktreeId);
          }
          // Track copy operation for undo
          if (currentProject?.id) {
            addCopiedFile({
              sourcePath: filePath,
              destinationPath: destinationPath,
              fileName: fileName,
              worktreeId: clipboardWorktreeId || activeWorktreeId,
              projectId: currentProject.id,
              copiedAt: Date.now(),
            });
          }
          toast.notify(`${clipboardIsDirectory ? "Directory" : "File"} copied`, { description: fileName });
        }
        // Clear clipboard after successful paste
        clearClipboard();
        // Refresh tree without remounting (no loading spinner)
        if (fileTreeRef.current) {
          await fileTreeRef.current.refresh();
        }
        setPasting(false); // Reset paste flag after refresh
      } catch (error) {
        logger.error("[RightSidebar] Failed to paste:", error);
        toast.notify("Failed to paste", { 
          description: error instanceof Error ? error.message : "Unknown error" 
        });
        setPasting(false); // Reset paste flag on error
      }
    };

    window.addEventListener("file-tree-cut", handleCut);
    window.addEventListener("file-tree-copy", handleCopy);
    window.addEventListener("file-tree-paste", handlePaste);

    return () => {
      window.removeEventListener("file-tree-cut", handleCut);
      window.removeEventListener("file-tree-copy", handleCopy);
      window.removeEventListener("file-tree-paste", handlePaste);
    };
  }, [activeSidebarTab, focusedPath, currentProject?.id, activeWorktreeId, setTreeKey, expandedPaths]);


  const handleRefresh = () => {
    setTreeKey((prev) => prev + 1);
  };

  const handleCollapseAll = () => {
    setCollapseKey((prev) => prev + 1);
    setExpandedPaths(new Set());
  };

  const handleToolbarNewFile = () => {
    setCreatingType("file");
  };

  const handleToolbarNewFolder = () => {
    setCreatingType("folder");
  };

  const handleCreatingComplete = () => {
    setCreatingType(null);
  };

  // Handle Files tab click - if already on files tab, close sidebar (or sync to active file if no close handler)
  const handleFilesTabClick = () => {
    if (activeSidebarTab === "files") {
      if (onCloseSidebar) {
        onCloseSidebar();
      } else {
        fileTreeRef.current?.syncToActiveFile();
        fileTreeRef.current?.focus();
      }
    } else {
      setActiveSidebarTab("files");
    }
  };

  const handleTabClick = (tab: RightSidebarTab) => {
    if (activeSidebarTab === tab) {
      onCloseSidebar?.();
    } else {
      setActiveSidebarTab(tab);
    }
  };

  return (
    <div className="flex flex-col h-full" data-onboarding="right-sidebar">
      {/* Tabs */}
      <div className="flex justify-center bg-accent border-b border-border h-10 overflow-visible pt-1">
        <Tooltip content="Files" placement="bottom">
          <button
            onClick={handleFilesTabClick}
            tabIndex={-1}
            className={cn(
              "px-3 h-full transition-colors border-b-2 flex items-center justify-center",
              activeSidebarTab === "files"
                ? "text-foreground"
                : "text-muted-foreground bg-accent header-icon-btn"
            )}
            style={activeSidebarTab === "files" ? {
              backgroundColor: 'hsl(var(--tab-active) / 0.15)',
              borderBottomColor: 'hsl(var(--tab-active))'
            } : undefined}
          >
            <Files className="w-4 h-4" />
          </button>
        </Tooltip>
        <Tooltip content="Changes" placement="bottom">
          <button
            onClick={() => handleTabClick("changes")}
            tabIndex={-1}
            className={cn(
              "px-3 h-full transition-colors border-b-2 flex items-center justify-center",
              activeSidebarTab === "changes"
                ? "text-foreground"
                : "text-muted-foreground bg-accent header-icon-btn"
            )}
            style={activeSidebarTab === "changes" ? {
              backgroundColor: 'hsl(var(--tab-active) / 0.15)',
              borderBottomColor: 'hsl(var(--tab-active))'
            } : undefined}
          >
            <GitBranch className="w-4 h-4" />
          </button>
        </Tooltip>
        <Tooltip content="Monitor running processes" placement="bottom">
          <button
            onClick={() => handleTabClick("processes")}
            tabIndex={-1}
            className={cn(
              "px-3 h-full transition-colors border-b-2 flex items-center justify-center",
              activeSidebarTab === "processes"
                ? "text-foreground"
                : "text-muted-foreground bg-accent header-icon-btn"
            )}
            style={activeSidebarTab === "processes" ? {
              backgroundColor: 'hsl(var(--tab-active) / 0.15)',
              borderBottomColor: 'hsl(var(--tab-active))'
            } : undefined}
          >
            <span className="relative">
              <Terminal className="w-4 h-4" />
              {totalRunningCount > 0 && (
                <span className="absolute -top-0.5 -right-0.5 w-1.5 h-1.5 rounded-full bg-green-500 animate-pulse" />
              )}
            </span>
          </button>
        </Tooltip>
        <Tooltip content="Tasks" placement="bottom">
          <button
            onClick={() => handleTabClick("tasks")}
            tabIndex={-1}
            className={cn(
              "px-3 h-full transition-colors border-b-2 flex items-center justify-center",
              activeSidebarTab === "tasks"
                ? "text-foreground"
                : "text-muted-foreground bg-accent header-icon-btn"
            )}
            style={activeSidebarTab === "tasks" ? {
              backgroundColor: 'hsl(var(--tab-active) / 0.15)',
              borderBottomColor: 'hsl(var(--tab-active))'
            } : undefined}
          >
            <span className="relative">
              <ListTodo className="w-4 h-4" />
              {/* Yellow dot for pending tasks (not yet started) */}
              {taskStats.pending > 0 && taskStats.inProgress === 0 && (
                <span className="absolute -top-0.5 -right-0.5 w-1.5 h-1.5 rounded-full bg-yellow-500" />
              )}
              {/* Green pulsing dot for in-progress tasks */}
              {taskStats.inProgress > 0 && (
                <span className="absolute -top-0.5 -right-0.5 w-1.5 h-1.5 rounded-full bg-green-500 animate-pulse" />
              )}
              {/* Checkmark for all tasks completed */}
              {taskStats.total > 0 && taskStats.completed === taskStats.total && (
                <span className="absolute -top-0.5 -right-0.5 flex items-center justify-center">
                  <Check className="w-2.5 h-2.5 text-green-500" />
                </span>
              )}
            </span>
          </button>
        </Tooltip>
        <Tooltip content="Browser" placement="bottom">
          <button
            onClick={() => handleTabClick("browser")}
            tabIndex={-1}
            className={cn(
              "px-3 h-full transition-colors border-b-2 flex items-center justify-center",
              activeSidebarTab === "browser"
                ? "text-foreground"
                : "text-muted-foreground bg-accent header-icon-btn"
            )}
            style={activeSidebarTab === "browser" ? {
              backgroundColor: 'hsl(var(--tab-active) / 0.15)',
              borderBottomColor: 'hsl(var(--tab-active))'
            } : undefined}
          >
            <span className="relative">
              <Globe className="w-4 h-4" />
              {worktreeBrowserTabs.length > 0 && (
                <span className="absolute -top-0.5 -right-0.5 w-1.5 h-1.5 rounded-full bg-blue-500" />
              )}
            </span>
          </button>
        </Tooltip>
      </div>

      {/* Header and search bar - only show for files tab */}
      {activeSidebarTab === "files" && (
        <FileTreeToolbar
          projectName={currentProject?.name}
          onNewFile={handleToolbarNewFile}
          onNewFolder={handleToolbarNewFolder}
          onRefresh={handleRefresh}
          onCollapseAll={handleCollapseAll}
        />
      )}

      {/* Content */}
      <div className="flex-1 overflow-hidden flex flex-col min-h-0">
        {/* Keep FileTree mounted but hidden to preserve state and scroll position */}
        <div className={cn("flex-1 overflow-y-auto", activeSidebarTab !== "files" && "hidden")}>
          <FileTree
            ref={fileTreeRef}
            key={treeKey}
            searchQuery=""
            onFileSelect={handleFileSelect}
            onPathChange={() => {}} // Not needed in sidebar mode
            selectedFile={activeViewer?.type === "file" ? activeViewer.file : null}
            showHidden={showHidden}
            onRefresh={handleRefresh}
            collapseKey={collapseKey}
            creatingType={creatingType}
            onCreatingComplete={handleCreatingComplete}
            worktreeId={activeWorktreeId}
            focusedPath={focusedPath}
            onFocusedPathChange={setFocusedPath}
            expandedPaths={expandedPaths}
            onExpandedPathsChange={setExpandedPaths}
            autoFocus={shouldAutoFocus}
          />
        </div>
        {activeSidebarTab === "changes" && (
          <div className="flex-1 overflow-y-auto">
            {currentProject ? (
              <RecentChanges 
                projectId={currentProject.id}
                worktreeId={activeWorktreeId}
                onClose={() => {}} // No close button needed in sidebar
                inline={true}
              />
            ) : (
              <div className="flex items-center justify-center h-full text-muted-foreground text-sm">
                No project selected
              </div>
            )}
          </div>
        )}
        {activeSidebarTab === "tasks" && (
          <TasksPanel chatId={activeChatId || undefined} />
        )}
        {activeSidebarTab === "processes" && (
          <CommandsViewerTab worktreeId={activeWorktreeId} />
        )}
        {activeSidebarTab === "browser" && (
          <BrowserSidebarContent worktreeId={activeWorktreeId} />
        )}
      </div>
    </div>
  );
}