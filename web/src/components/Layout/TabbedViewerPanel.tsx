import { X, ArrowRightFromLine, ArrowLeftToLine, FolderOpen, Terminal, Globe, FolderGit2, Workflow, Lock } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import { cn } from "../../lib/utils";
import { isElectron } from "../../lib/constants";
import { ResizableDiffPanel } from "./ResizableDiffPanel";
import { useViewerStore, type Viewer, type CommandsViewer, type WorkflowViewer } from "../../store/viewerStore";
import { useProjectStore } from "../../store/projectStore";
import { useWorktreeStore } from "../../store/worktreeStore";
import { MonacoDiffViewer } from "../FileBrowser/MonacoDiffViewer";
import { FileViewerTab } from "../FileBrowser/FileViewerTab";

import { useDaemonStatus } from "../../hooks/useDaemonStatus";
import { WorktreesPanel } from "../Worktrees/WorktreesPanel";
import { ArchivedWorktreesPanel } from "../Worktrees/ArchivedWorktreesPanel";
import { WorktreeDetailView } from "../Worktrees/WorktreeDetailView";
import { ProjectPanel } from "../Projects/ProjectPanel";
import { CommandsViewerTab } from "../PackageCommands/CommandsViewerTab";
import { SingleBrowserView } from "../Browser/SingleBrowserView";
import { WorkflowViewerTab } from "../workflow/WorkflowViewerTab";
import { FileChangeStatus } from "../../gen/reliant/v1/common_pb";

interface TabbedViewerPanelProps {
  hasTerminal?: boolean;
}

export function TabbedViewerPanel({
  hasTerminal = false,
}: TabbedViewerPanelProps = {}) {
  const allViewers = useViewerStore((state) => state.viewers);
  // Project context is now owned by projectStore (single source of truth).
  const currentProjectId = useProjectStore((state) => state.currentProject?.id ?? null);
  // Get current worktree from worktreeStore (single source of truth)
  const currentWorktreeId = useWorktreeStore((state) => state.currentWorktree?.id ?? null);
  const activeViewerId = useViewerStore((state) => state.activeViewerId);
  const setActiveViewer = useViewerStore((state) => state.setActiveViewer);
  const closeViewer = useViewerStore((state) => state.closeViewer);
  const closeAllViewers = useViewerStore((state) => state.closeAllViewers);
  const isFullscreen = useViewerStore((state) => state.isFullscreen);
  const toggleFullscreen = useViewerStore((state) => state.toggleFullscreen);
  const [isMounted, setIsMounted] = useState(false);
  
  // Right offset for fullscreen mode (so the viewer doesn't hide behind the file browser).
  // IMPORTANT: Measure the *actual* right sidebar width from the DOM so we don't leave
  // blank space when the sidebar is closed/hidden.
  const [rightOffset, setRightOffset] = useState(0);
  useEffect(() => {
    if (!isFullscreen) {
      setRightOffset(0);
      return;
    }

    const computeRightOffset = () => {
      const rightSidebar = document.getElementById("layout-right-sidebar");
      if (!rightSidebar) {
        setRightOffset(0);
        return;
      }
      // IMPORTANT:
      // The right sidebar can be "closed" by translating it off-screen (or otherwise hiding it)
      // while still having a non-zero element width. In fullscreen mode we must only reserve
      // space for the *visible* portion, otherwise we end up with a blank gutter.
      const rect = rightSidebar.getBoundingClientRect();
      const visibleWidth = Math.max(
        0,
        Math.min(rect.right, window.innerWidth) - Math.max(rect.left, 0)
      );
      const width = Math.round(visibleWidth || 0);
      setRightOffset(width > 0 ? width : 0);
    };

    computeRightOffset();

    // Keep updated on window resize + sidebar resize/unmount.
    window.addEventListener("resize", computeRightOffset);

    const sidebarEl = document.getElementById("layout-right-sidebar");
    const resizeObserver = new ResizeObserver(computeRightOffset);
    if (sidebarEl) {
      resizeObserver.observe(sidebarEl);
    }

    // Observe layout changes (mount/unmount of the right sidebar).
    const layoutRoot =
      document.getElementById("layout-right-panel")?.parentElement ?? document.body;
    const mutationObserver = new MutationObserver(computeRightOffset);
    mutationObserver.observe(layoutRoot, { childList: true, subtree: true });

    return () => {
      window.removeEventListener("resize", computeRightOffset);
      resizeObserver.disconnect();
      mutationObserver.disconnect();
    };
  }, [isFullscreen, currentProjectId, currentWorktreeId]);

  // Trigger mount animation
  useEffect(() => {
    setIsMounted(true);
  }, []);

  // Filter viewers by project, and for browser viewers also filter by worktree
  const viewers = useMemo(() => {
    if (!currentProjectId) return allViewers;
    return allViewers.filter(v => {
      if (v.projectId !== currentProjectId) return false;
      // Browser viewers should only show for the current worktree
      if (v.type === "browser") {
        return (v as any).worktreeId === currentWorktreeId;
      }
      return true;
    });
  }, [allViewers, currentProjectId, currentWorktreeId]);

  const hasViewers = viewers.length > 0;

  // Only show panel if there are viewers
  if (!hasViewers) {
    return null;
  }

  return (
    <ResizableDiffPanel
      defaultWidth={600}
      minWidth={350}
      maxWidth={1200}
      hasTerminal={hasTerminal}
      isFullscreen={isFullscreen}
      rightOffset={rightOffset}
      className={`transition-opacity duration-200 ${
        isMounted ? 'opacity-100' : 'opacity-0'
      }`}
    >
      {/* Tabs Header */}
      <div className="flex items-center border-b border-border bg-muted/20 flex-shrink-0">
        <div className="flex-1 flex items-center overflow-x-auto scrollbar-thin">
          {viewers.map((viewer) => (
            <ViewerTab
              key={viewer.id}
              viewer={viewer}
              isActive={viewer.id === activeViewerId}
              onSelect={() => {
                setActiveViewer(viewer.id);
              }}
              onClose={() => closeViewer(viewer.id)}
            />
          ))}
        </div>
        {/* Fullscreen Toggle Button */}
        <button
          onClick={toggleFullscreen}
          className="px-2 py-1.5 text-xs text-muted-foreground hover:text-foreground hover:bg-muted/50 transition-colors flex-shrink-0 border-l border-border"
          title={isFullscreen ? "Return to side panel" : "Expand to cover chat"}
        >
          {isFullscreen ? (
            <ArrowRightFromLine className="w-4 h-4" />
          ) : (
            <ArrowLeftToLine className="w-4 h-4" />
          )}
        </button>
        {/* Close All Button */}
        {viewers.length > 1 && (
          <button
            onClick={closeAllViewers}
            className="px-2 py-1.5 text-xs text-muted-foreground hover:text-foreground hover:bg-muted/50 transition-colors flex-shrink-0 border-l border-border"
            title="Close all tabs"
          >
            Close All
          </button>
        )}
      </div>

      {/* Content Area - scrollable */}
      <div className="flex-1 overflow-auto min-h-0">
        {viewers.map((viewer, index) => {
          // Show viewer if it's active, OR if no viewer is active show the first one
          const isActive =
            viewer.id === activeViewerId || (!activeViewerId && index === 0);
          return (
            <div
              key={viewer.id}
              className="h-full"
              style={{ display: isActive ? "block" : "none" }}
            >
              <ViewerContent viewer={viewer} isActive={isActive} />
            </div>
          );
        })}
      </div>
    </ResizableDiffPanel>
  );
}

interface ViewerTabProps {
  viewer: Viewer;
  isActive: boolean;
  onSelect: () => void;
  onClose: () => void;
}

function ViewerTab({ viewer, isActive, onSelect, onClose }: ViewerTabProps) {
  const tabRef = useRef<HTMLDivElement>(null);

  const getDiffStatusTitle = () => {
    if (viewer.type !== "diff") return "file";

    switch (viewer.file.status) {
      case FileChangeStatus.STAGED:
        return "staged";
      case FileChangeStatus.MODIFIED:
        return "modified";
      case FileChangeStatus.UNTRACKED:
        return "untracked";
      default:
        return "unknown";
    }
  };

  // Auto-scroll to active tab when it becomes active
  useEffect(() => {
    if (isActive && tabRef.current) {
      tabRef.current.scrollIntoView({
        behavior: "smooth",
        block: "nearest",
        inline: "center",
      });
    }
  }, [isActive]);

  const getStatusColor = () => {
    if (viewer.type === "diff") {
      const status = viewer.file.status;
      if (status === FileChangeStatus.STAGED) return "text-blue-500";
      if (status === FileChangeStatus.MODIFIED) return "text-amber-500";
      return "text-green-500";
    }
    return "text-primary";
  };

  const getStatusBadge = () => {
    if (viewer.type === "diff") {
      const status = viewer.file.status;
      if (status === FileChangeStatus.STAGED) return "S";
      if (status === FileChangeStatus.MODIFIED) return "M";
      return "N";
    }
    if (viewer.type === "worktrees") return <FolderGit2 className="w-3 h-3" />;
    if (viewer.type === "projects") return <FolderOpen className="w-3 h-3" />;
    if (viewer.type === "commands") return <Terminal className="w-3 h-3" />;
    if (viewer.type === "browser") return <Globe className="w-3 h-3" />;
    if (viewer.type === "workflow") return <Workflow className="w-3 h-3" />;
    return "F";
  };

  const isDiff = viewer.type === "diff";

  return (
    <div
      ref={tabRef}
      className={cn(
        "group flex items-center gap-2 px-3 py-2 border-r border-border cursor-pointer transition-colors",
        isActive
          ? isDiff
            ? "bg-amber-500/10 text-amber-600 dark:text-amber-400 border-b-2 border-b-amber-500"
            : "text-foreground border-b-2"
          : isDiff
          ? "bg-amber-500/5 text-amber-600/70 dark:text-amber-400/70 hover:bg-amber-500/10"
          : "bg-muted/30 text-muted-foreground hover:bg-muted/50"
      )}
      style={
        isActive && !isDiff
          ? {
              backgroundColor: `hsl(var(--tab-active) / 0.15)`,
              borderBottomColor: `hsl(var(--tab-active))`,
              color: `hsl(var(--foreground))`,
            }
          : undefined
      }
      onClick={onSelect}
      title={isActive ? "Active - Press Cmd+W to close" : viewer.title}
    >
      {/* Status Badge */}
      <span
        className={cn(
          "text-xs font-mono font-semibold w-4 h-4 flex items-center justify-center rounded",
          getStatusColor()
        )}
        title={getDiffStatusTitle()}
      >
        {getStatusBadge()}
      </span>

      {/* Title */}
      <span
        className={cn(
          "text-sm font-mono truncate min-w-[80px]",
          isDiff && isActive && "font-semibold"
        )}
      >
        {viewer.title}
      </span>

      {/* Lock icon for staged/index files */}
      {isDiff && viewer.file.status === FileChangeStatus.STAGED && (
        <Lock className="w-3 h-3 text-muted-foreground flex-shrink-0" aria-label="Read-only (Index)" />
      )}

      {/* Close Button */}
      <button
        onMouseDown={(e) => {
          e.stopPropagation();
          e.preventDefault();
        }}
        onClick={(e) => {
          e.stopPropagation();
          e.preventDefault();
          onClose();
        }}
        className={cn(
          "p-0.5 rounded hover:bg-destructive/20 transition-all relative z-10 pointer-events-auto",
          isActive ? "opacity-100" : "opacity-0 group-hover:opacity-100"
        )}
        aria-label="Close tab (Cmd+W)"
        title="Close (Cmd+W)"
      >
        <X className="w-3 h-3 pointer-events-none" />
      </button>
    </div>
  );
}

interface ViewerContentProps {
  viewer: Viewer;
  isActive?: boolean;
}

function WorktreesViewerContent() {
  const [worktreeView, setWorktreeView] = useState<"active" | "archived">(
    "active"
  );
  const { activeDaemon } = useDaemonStatus();

  return (
    <div className="flex h-full">
      {/* Left Sidebar - Worktree List */}
      <div className="w-64 flex-shrink-0 flex flex-col border-r border-border">
        {/* Tabs for Active/Archived */}
        <div className="flex bg-accent border-b border-border h-10 flex-shrink-0">
          <button
            onClick={() => setWorktreeView("active")}
            className={cn(
              "flex-1 px-3 h-full text-xs font-mono transition-colors border-b-2",
              worktreeView === "active"
                ? "text-foreground"
                : "text-muted-foreground bg-accent hover:bg-muted"
            )}
            style={
              worktreeView === "active"
                ? {
                    backgroundColor: "hsl(var(--tab-active) / 0.15)",
                    borderBottomColor: "hsl(var(--tab-active))",
                  }
                : undefined
            }
          >
            Active
          </button>
          <button
            onClick={() => setWorktreeView("archived")}
            className={cn(
              "flex-1 px-3 h-full text-xs font-mono transition-colors border-b-2",
              worktreeView === "archived"
                ? "text-foreground"
                : "text-muted-foreground bg-accent hover:bg-muted"
            )}
            style={
              worktreeView === "archived"
                ? {
                    backgroundColor: "hsl(var(--tab-active) / 0.15)",
                    borderBottomColor: "hsl(var(--tab-active))",
                  }
                : undefined
            }
          >
            Archived
          </button>
        </div>
        {/* List Content */}
        <div className="flex-1 overflow-auto">
          {worktreeView === "active" ? (
            <WorktreesPanel daemonId={activeDaemon?.daemonId} />
          ) : (
            <ArchivedWorktreesPanel />
          )}
        </div>
      </div>

      {/* Right Side - Detail View */}
      <div className="flex-1 overflow-auto">
        <WorktreeDetailView />
      </div>
    </div>
  );
}

function ViewerContent({ viewer, isActive }: ViewerContentProps) {
  if (viewer.type === "diff") {
    return <MonacoDiffViewer file={viewer.file} />;
  }

  if (viewer.type === "file") {
    return <FileViewerTab file={viewer.file} worktreeId={viewer.worktreeId} isActive={isActive} viewerId={viewer.id} />;
  }

  if (viewer.type === "worktrees") {
    return <WorktreesViewerContent />;
  }

  if (viewer.type === "projects") {
    return (
      <div className="h-full overflow-auto">
        <ProjectPanel
          onNavigateToProjectPicker={() => {
            // Close the projects viewer when navigating to project picker
            const { closeViewer } = useViewerStore.getState();
            closeViewer(viewer.id);
            // Navigate to project picker (clear current project)
            useProjectStore.setState({ currentProject: null });
          }}
        />
      </div>
    );
  }

  // Workflows are now full-screen mode, not tabs

  if (viewer.type === "commands") {
    const commandsViewer = viewer as CommandsViewer;
    return (
      <div className="h-full">
        <CommandsViewerTab
          worktreeId={commandsViewer.worktreeId}
          processId={commandsViewer.processId}
        />
      </div>
    );
  }

  if (viewer.type === "browser") {
    // The in-app browser is an Electron <webview> — it does not exist in the web
    // build. Never mount it there; show a desktop-only note instead of a dead panel.
    if (!isElectron()) {
      return (
        <div className="flex-1 flex items-center justify-center text-muted-foreground p-8 text-center">
          <div className="max-w-xs">
            <Globe className="w-8 h-8 mx-auto mb-2 opacity-50" />
            <p className="text-sm font-medium">Browser preview is desktop-only</p>
            <p className="text-xs mt-1">
              The in-app browser is available in the Reliant desktop app. Here, links
              open in a new browser tab.
            </p>
          </div>
        </div>
      );
    }
    return <SingleBrowserView tabId={viewer.browserTabId} viewerId={viewer.id} />;
  }

  if (viewer.type === "workflow") {
    const workflowViewer = viewer as WorkflowViewer;
    return (
      <div className="h-full">
        <WorkflowViewerTab
          projectId={workflowViewer.projectId}
          chatId={workflowViewer.chatId}
          workflowName={workflowViewer.workflowName}
        />
      </div>
    );
  }

  return null;
}