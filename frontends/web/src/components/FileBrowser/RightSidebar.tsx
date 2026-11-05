import { useState, useEffect, useCallback, useRef, type KeyboardEvent, type ReactNode } from "react";
import { Files, GitBranch, ListTodo, Check, Terminal, Globe, PanelBottom } from "lucide-react";
import { FileTree, type FileTreeHandle } from "./FileTree";
import { FileTreeToolbar } from "./FileTreeToolbar";
import { useProjectStore } from "../../store/projectStore";
import { useShortcutLabel, withShortcut } from "../../store/shortcutsStore";
import { useViewerStore } from "../../store/viewerStore";
import { useUIStore } from "../../store/uiStore";
import { useBrowserStore } from "../../store/browserStore";
import { useTerminalStore } from "../../store/terminalStore";
import { isElectron } from "../../lib/constants";
import { useUnifiedProcessCounts } from "../../hooks/useUnifiedProcesses";
import { RecentChanges } from "../Chat/RecentChanges";
import { TasksPanel } from "../Chat/TasksPanel";
import { CommandsViewerTab } from "../PackageCommands/CommandsViewerTab";
import { BrowserSidebarContent } from "../Browser/BrowserSidebarContent";
import { useTaskStats } from "../../hooks/task-queries";
import { useActiveChatId } from "../../store/chatStoreHooks";
import { useCurrentWorktreeState, useWorkspaceStateStore, type RightSidebarTab } from "../../store/workspaceStateStore";
import { useFileClipboardStore } from "../../store/fileClipboardStore";
import { useFileDeletionStore } from "../../store/fileDeletionStore";
import { copyFile, moveFile, copyDirectoryRecursive, createFolder, deleteFileOrFolder } from "../../api/fileSystem";
import { toast } from "../../lib/toast-manager";
import Tooltip from "../forge-ui/tooltip";
import { cn } from "../../lib/utils";
import { logger } from "../../lib/logger";
import { useGitStatusRefreshTrigger } from "../../store/gitStatusStore";
import { useFileTreePushRefresh } from "./useFileTreePushRefresh";
import { withAncestorsExpanded } from "./treePaths";
import type { FileNode } from "./index";
import { useCurrentWorktree, useActiveWorktreeId } from "../../hooks/worktree-queries";

/**
 * The right sidebar's tabbed panel. It has no close control of its own:
 * clicking the open tab used to close it, a surprising thing for a tab to
 * do. The header's sidebar toggle (and its shortcut) shows and hides it.
 */
export function RightSidebar() {
  const shortcuts = {
    files: useShortcutLabel("sidebarFiles"),
    changes: useShortcutLabel("sidebarChanges"),
    processes: useShortcutLabel("sidebarProcesses"),
    tasks: useShortcutLabel("sidebarTasks"),
    browser: useShortcutLabel("sidebarBrowser"),
    terminal: useShortcutLabel("toggleTerminal"),
  };
  const currentProject = useProjectStore((state) => state.currentProject);
  const currentWorktree = useCurrentWorktree();
  
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
  const sidebarRef = useRef<HTMLDivElement>(null);

  // Track if we should auto-focus the tree (when Files tab becomes active)
  const [shouldAutoFocus, setShouldAutoFocus] = useState(false);
  const prevSidebarTabRef = useRef(activeSidebarTab);

  // Get showHidden from UI store (persistent across sessions)
  const showHidden = useUIStore((state) => state.showHiddenFiles);

  // Subscribe to git status refresh trigger to update file tree when files change
  const gitRefreshTrigger = useGitStatusRefreshTrigger();

  const openFileViewer = useViewerStore((state) => state.openFileViewer);
  const activeViewer = useViewerStore((state) => state.getActiveViewer());
  const activeFilePath = activeViewer?.type === "file" ? activeViewer.file.path : null;

  // Use the global active worktree (from worktreeStore) as the single source of truth
  const activeWorktreeId = useActiveWorktreeId();
  
  // Unified running count from deduplicated process stores
  const { currentWorkspaceRunning: totalRunningCount } = useUnifiedProcessCounts(activeWorktreeId);

  // Browser tabs count for the current worktree
  const browserTabs = useBrowserStore((state) => state.tabs);
  const worktreeBrowserTabs = activeWorktreeId 
    ? browserTabs.filter(t => t.worktreeId === activeWorktreeId)
    : [];

  const activeChatId = useActiveChatId();

  // Task stats from React Query
  const taskStats = useTaskStats(activeChatId);

  // Terminal panel visibility. This is a panel toggle, not a sidebar tab — the
  // terminal renders below the viewer, outside this sidebar.
  const isTerminalOpen = useTerminalStore((state) => state.isOpen);
  const toggleTerminal = useTerminalStore((state) => state.toggleTerminal);

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
    if (!activeFilePath || activeSidebarTab !== "files") {
      return;
    }

    // Expand parents to make file visible. A file outside the tree (an
    // absolute or "~" path) expands nothing: see treePaths.ts.
    setExpandedPaths((prev) => withAncestorsExpanded(prev, activeFilePath));
    setFocusedPath(activeFilePath);

    // Scroll to the file after React renders (instant, no animation)
    setTimeout(() => {
      const element = document.querySelector(`[data-path="${CSS.escape(activeFilePath)}"]`);
      element?.scrollIntoView({ block: "nearest" });
    }, 0);
  }, [activeFilePath, activeSidebarTab]);

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

  // Cmd+Shift+B focuses the sidebar. Each tab owns its own arrow/Enter keys —
  // FileTree and RecentChanges both have richer navigation than anything a
  // sidebar-level handler could provide — so this only hands over focus and
  // lets the active tab take it from there.
  useEffect(() => {
    const handleFocusSidebar = () => {
      if (activeSidebarTab === "files") {
        fileTreeRef.current?.focus();
        return;
      }
      // Other tabs have no imperative focus handle; focusing the container puts
      // the user in the right place for tab-order and scrolling.
      sidebarRef.current?.focus();
    };

    window.addEventListener("focus-right-sidebar", handleFocusSidebar);
    return () =>
      window.removeEventListener("focus-right-sidebar", handleFocusSidebar);
  }, [activeSidebarTab]);

  // Reset state when project or worktree changes. Project context is owned by
  // projectStore now — viewerStore reads it via getCurrentProjectId() — so we
  // no longer need to call setCurrentProject here.
  useEffect(() => {
    // Reset file tree state on project/worktree change
    setFocusedPath(null);
    setExpandedPaths(new Set());
    // Note: showHidden is now persisted in UI store and won't reset on project change
    // Don't remount via treeKey — FileTree's own useEffect on [worktreeId] handles re-fetch.
    // Remounting caused 2-3 redundant GetFileTree calls during initial hydration.
  }, [currentProject?.id, activeWorktreeId]);

  // Listen for file operation undo events to refresh the tree and expand paths
  useEffect(() => {
    const handleFileOperationUndone = (e: CustomEvent<{ path: string }>) => {
      if (activeSidebarTab !== "files") {
        return;
      }

      const restoredPath = e.detail.path;
      
      // Expand parent directories to make restored file/folder visible
      setExpandedPaths((prev) => withAncestorsExpanded(prev, restoredPath));

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

  // Refresh the tree when its files change on disk (agent tool calls, the
  // daemon's filesystem watcher) — only for this worktree/project, and only
  // while the Files tab is showing; see useFileTreePushRefresh.
  useFileTreePushRefresh({
    projectId: currentProject?.id,
    worktreeId: activeWorktreeId,
    visible: activeSidebarTab === "files",
    refresh: () => fileTreeRef.current?.refresh(),
  });

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

  // Clicking the tab that is already open does not close the sidebar — a tab
  // never hides its own panel (the header's toggle does that). On Files it
  // brings the tree to the open file.
  const handleTabClick = (tab: RightSidebarTab) => {
    if (activeSidebarTab !== tab) {
      setActiveSidebarTab(tab);
      return;
    }
    if (tab === "files") {
      fileTreeRef.current?.syncToActiveFile();
      fileTreeRef.current?.focus();
    }
  };

  // The tabs, as a WAI-ARIA tablist: the open tab is the one tab stop, the
  // arrow keys move along the strip (and open what they land on), and each
  // tab's status dot is in its accessible name, not only its colour.
  const tabRefs = useRef<Partial<Record<RightSidebarTab, HTMLButtonElement | null>>>({});
  const tabs: Array<{
    id: RightSidebarTab;
    label: string;
    tooltip: string;
    icon: ReactNode;
    status?: { dotClass?: string; icon?: ReactNode; label: string };
  }> = [
    { id: "files", label: "Files", tooltip: withShortcut("Files", shortcuts.files), icon: <Files className="w-4 h-4" /> },
    {
      id: "changes",
      label: "Changes",
      tooltip: withShortcut("Changes", shortcuts.changes),
      icon: <GitBranch className="w-4 h-4" />,
    },
    {
      id: "processes",
      label: "Processes",
      tooltip: withShortcut("Processes: monitor running commands", shortcuts.processes),
      icon: <Terminal className="w-4 h-4" />,
      status:
        totalRunningCount > 0
          ? { dotClass: "bg-primary animate-pulse motion-reduce:animate-none", label: `${totalRunningCount} running` }
          : undefined,
    },
    {
      id: "tasks",
      label: "Tasks",
      tooltip: withShortcut("Tasks", shortcuts.tasks),
      icon: <ListTodo className="w-4 h-4" />,
      status:
        taskStats.inProgress > 0
          ? { dotClass: "bg-primary animate-pulse motion-reduce:animate-none", label: `${taskStats.inProgress} in progress` }
          : taskStats.pending > 0
            ? { dotClass: "bg-warning", label: `${taskStats.pending} not started` }
            : taskStats.total > 0 && taskStats.completed === taskStats.total
              ? { icon: <Check className="w-2.5 h-2.5 text-success-ink" />, label: "all done" }
              : undefined,
    },
    // The in-app browser is an Electron <webview>: no tab in the web build,
    // where it cannot be mounted.
    ...(isElectron()
      ? [
          {
            id: "browser" as const,
            label: "Browser",
            tooltip: withShortcut("Browser", shortcuts.browser),
            icon: <Globe className="w-4 h-4" />,
            status:
              worktreeBrowserTabs.length > 0
                ? { dotClass: "bg-muted-foreground", label: `${worktreeBrowserTabs.length} open` }
                : undefined,
          },
        ]
      : []),
  ];

  const onTabKeyDown = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    const last = tabs.length - 1;
    let next: number;
    switch (event.key) {
      case "ArrowRight":
        next = index === last ? 0 : index + 1;
        break;
      case "ArrowLeft":
        next = index === 0 ? last : index - 1;
        break;
      case "Home":
        next = 0;
        break;
      case "End":
        next = last;
        break;
      default:
        return;
    }
    event.preventDefault();
    const tab = tabs[next];
    setActiveSidebarTab(tab.id);
    tabRefs.current[tab.id]?.focus();
  };

  return (
    <div
      ref={sidebarRef}
      className="flex flex-col h-full"
      data-onboarding="right-sidebar"
      // Marks the focus context for the keyboard dispatcher, so shortcuts can
      // be scoped to "the right sidebar has focus".
      data-context="right-sidebar"
      tabIndex={-1}
    >
      {/* Tabs */}
      <div className="flex justify-center bg-accent border-b border-border h-10 overflow-visible pt-1">
        <div role="tablist" aria-label="Sidebar" className="flex h-full">
          {tabs.map((tab, index) => {
            const selected = activeSidebarTab === tab.id;
            return (
              <Tooltip key={tab.id} content={tab.tooltip} side="bottom" delay={300}>
                <button
                  ref={(el) => {
                    tabRefs.current[tab.id] = el;
                  }}
                  type="button"
                  role="tab"
                  id={`right-sidebar-tab-${tab.id}`}
                  aria-selected={selected}
                  aria-controls="right-sidebar-panel"
                  aria-label={tab.status ? `${tab.label}, ${tab.status.label}` : tab.label}
                  tabIndex={selected ? 0 : -1}
                  onClick={() => handleTabClick(tab.id)}
                  onKeyDown={(event) => onTabKeyDown(event, index)}
                  className={cn(
                    "px-3 h-full transition-colors border-b-2 flex items-center justify-center focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
                    selected ? "text-foreground" : "border-transparent text-muted-foreground bg-accent header-icon-btn",
                  )}
                  style={selected ? {
                    backgroundColor: 'hsl(var(--tab-active) / 0.15)',
                    borderBottomColor: 'hsl(var(--tab-active))'
                  } : undefined}
                >
                  <span className="relative">
                    {tab.icon}
                    {tab.status?.dotClass && (
                      <span className={cn("absolute -top-0.5 -right-0.5 w-1.5 h-1.5 rounded-full", tab.status.dotClass)} />
                    )}
                    {tab.status?.icon && (
                      <span className="absolute -top-0.5 -right-0.5 flex items-center justify-center">{tab.status.icon}</span>
                    )}
                  </span>
                </button>
              </Tooltip>
            );
          })}
        </div>

        {/* Terminal toggle. Not a tab — the terminal panel lives below the
            viewer, so this shows/hides it rather than switching sidebar
            content. Separated by a divider to make that distinction visible. */}
        {currentProject && (
          <>
            <span className="self-center h-4 w-px bg-border mx-1" />
            <Tooltip content={withShortcut(isTerminalOpen ? "Hide terminal" : "Show terminal", shortcuts.terminal)} side="bottom" delay={300}>
              <button
                type="button"
                onClick={toggleTerminal}
                aria-pressed={isTerminalOpen}
                aria-label={isTerminalOpen ? "Hide Terminal" : "Show Terminal"}
                className={cn(
                  "px-3 h-full transition-colors border-b-2 border-transparent flex items-center justify-center focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
                  isTerminalOpen
                    ? "text-foreground"
                    : "text-muted-foreground bg-accent header-icon-btn"
                )}
                style={isTerminalOpen ? {
                  backgroundColor: 'hsl(var(--tab-active) / 0.15)',
                  borderBottomColor: 'hsl(var(--tab-active))'
                } : undefined}
              >
                <PanelBottom className="w-4 h-4" />
              </button>
            </Tooltip>
          </>
        )}
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
      <div
        className="flex-1 overflow-hidden flex flex-col min-h-0"
        role="tabpanel"
        id="right-sidebar-panel"
        aria-labelledby={`right-sidebar-tab-${activeSidebarTab}`}
      >
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
        {isElectron() && activeSidebarTab === "browser" && (
          <BrowserSidebarContent worktreeId={activeWorktreeId} />
        )}
      </div>
    </div>
  );
}