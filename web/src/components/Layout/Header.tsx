import { RiFeedbackLine } from "react-icons/ri";
import {
  Minus,
  Square,
  X,
  Activity,
  Settings,
  PanelBottom,
  PanelRight,
  PanelLeft,
  Workflow,
  FolderOpen,
  FolderGit2,
  ChevronDown,
  Check,
  Search,
} from "lucide-react";
import {
  useState,
  forwardRef,
  useImperativeHandle,
  useEffect,
  useRef,
} from "react";
import { LuFolderPlus } from "react-icons/lu";
import { Tooltip } from "../ui/Tooltip";

import { useProjectStore, type Project } from "../../store/projectStore";
import { useShortcutsStore } from "../../store/shortcutsStore";
import { useWorktreeStore } from "../../store/worktreeStore";
import { CreateWorktreeModal } from "../Worktrees/CreateWorktreeModal";
import { DiscoverWorktreesModal } from "../Worktrees/DiscoverWorktreesModal";
import { ConfigHealthIndicator } from "./ConfigHealthIndicator";
import { isDev } from "../../lib/constants";
import { openExternalLink } from "../../lib/open-link";

interface HeaderProps {
  // Controls logo size in the app bar for tasteful variations
  logoSize?: "sm" | "md" | "lg" | "xl"; // sm:16px, md:20px, lg:24px, xl:28px
  // Whether this header is aligned to the window's left edge (traffic lights on macOS)
  // If false, we assume it's aligned to the content area (e.g., to the right of a vertical nav)
  windowAligned?: boolean;
  onNavigateToSettings?: () => void;
  onNavigateToWorktrees?: () => void;
  onNavigateToSettingsSection?: (section: string) => void;
  onNavigateToChats?: () => void;
  onNavigateToProjects?: () => void;
  projectPickerMode?: boolean;
  onProjectSelect?: (project: Project) => void;
  onNavigateToProjectPicker?: () => void;
  onToggleTerminal?: () => void;
  onToggleFileBrowser?: () => void;
  onToggleChatSidebar?: () => void;
  onOpenPageOverview?: () => void;
  onOpenWorkflows?: () => void;
  onOpenFeedback?: () => void;
}

export interface HeaderRef {
  // Legacy interface kept for compatibility
  // Search functionality now handled by dedicated modals in ModernApp
}

export const Header = forwardRef<HeaderRef, HeaderProps>(
  (
    {
      windowAligned = true,
      onNavigateToSettings,
      onNavigateToWorktrees,
      onNavigateToSettingsSection: _onNavigateToSettingsSection,
      onNavigateToChats: _onNavigateToChats,
      onNavigateToProjects: _onNavigateToProjects,
      projectPickerMode = false,
      onProjectSelect: _onProjectSelect,
      onNavigateToProjectPicker,
      onToggleTerminal,
      onToggleFileBrowser,
      onToggleChatSidebar,
      onOpenPageOverview: _onOpenPageOverview,
      onOpenWorkflows,
      onOpenFeedback,
    },
    ref
  ) => {
    const [isMaximized, setIsMaximized] = useState(false);
    const [isFullscreen, setIsFullscreen] = useState(false);
    const [showWorkspaceDropdown, setShowWorkspaceDropdown] = useState(false);
    const [showCreateWorktreeModal, setShowCreateWorktreeModal] = useState(false);
    const [showDiscoverWorktreeModal, setShowDiscoverWorktreeModal] =
      useState(false);
    const workspaceDropdownRef = useRef<HTMLDivElement>(null);
    const isMac = window.electronAPI?.platform === "darwin";

    // Track fullscreen state - use both resize listener AND Electron API
    // Query Electron API on every resize to catch Fill modes immediately
    useEffect(() => {
      const checkFullscreenStatus = async () => {
        if (window.electronAPI?.getFullscreenStatus) {
          const isFS = await window.electronAPI.getFullscreenStatus();
          setIsFullscreen(isFS);
        }
      };

      // Get initial fullscreen status
      checkFullscreenStatus();

      // Listen for resize events (catches Fill modes, manual resizes, etc.)
      window.addEventListener('resize', checkFullscreenStatus);

      // Also listen to Electron fullscreen events for instant true fullscreen detection
      let unsubscribe: (() => void) | undefined;
      if (window.electronAPI?.onFullscreenChanged) {
        unsubscribe = window.electronAPI.onFullscreenChanged((fs: boolean) => {
          setIsFullscreen(fs);
        });
      }

      return () => {
        window.removeEventListener('resize', checkFullscreenStatus);
        if (unsubscribe) unsubscribe();
      };
    }, []);
    const currentProject = useProjectStore((state) => state.currentProject);
    const currentWorktree = useWorktreeStore((state) => state.currentWorktree);
    const worktrees = useWorktreeStore((state) => state.worktrees);
    const switchWorktreeContext = useWorktreeStore(
      (state) => state.switchWorktreeContext
    );
    const shortcuts = useShortcutsStore((state) => state.shortcuts);

    const activeWorktrees = worktrees.filter((worktree) => !worktree.deleted_at);
    const mainWorktree =
      activeWorktrees.find((worktree) => worktree.is_main) ?? null;
    const selectableWorkspaces = activeWorktrees.filter(
      (worktree) => !worktree.is_main
    );



    // Helper to format shortcut display
    const formatShortcut = (handler: string): string => {
      const shortcut = Object.values(shortcuts).find(
        (s) => s.handler === handler
      );
      if (!shortcut) return "";

      const binding = shortcut.currentBinding;
      const parts: string[] = [];

      if (binding.shift) parts.push("⇧");
      if (binding.meta) parts.push("⌘");
      if (binding.ctrl) parts.push("Ctrl");
      if (binding.alt) parts.push("⌥");

      // Format the key
      let key = binding.key;
      if (key === "\\") key = "\\";
      else key = key.toUpperCase();

      parts.push(key);
      return parts.join("");
    };

    // Track theme state
    useEffect(() => {
      const compute = () => {
        const mode = document.documentElement.dataset.mode;
        if (mode === "dark") return true;
        if (mode === "light") return false;
        return window.matchMedia("(prefers-color-scheme: dark)").matches;
      };
      const update = () => {
        compute();
      };
      update();
      const media = window.matchMedia("(prefers-color-scheme: dark)");
      media.addEventListener("change", update);
      // Update icon on theme changes
      window.addEventListener("appearance-updated", update as EventListener);
      window.addEventListener("theme-toggle", update as EventListener);
      window.addEventListener("theme-applied", update as EventListener);
      return () => {
        media.removeEventListener("change", update);
        window.removeEventListener(
          "appearance-updated",
          update as EventListener
        );
        window.removeEventListener("theme-toggle", update as EventListener);
        window.removeEventListener("theme-applied", update as EventListener);
      };
    }, []);

    // Expose ref (legacy, search now handled by dedicated modals)
    useImperativeHandle(ref, () => ({}));

    useEffect(() => {
      const handleClickOutside = (event: MouseEvent) => {
        if (
          workspaceDropdownRef.current &&
          !workspaceDropdownRef.current.contains(event.target as Node)
        ) {
          setShowWorkspaceDropdown(false);
        }
      };

      if (showWorkspaceDropdown) {
        document.addEventListener("mousedown", handleClickOutside);
        return () =>
          document.removeEventListener("mousedown", handleClickOutside);
      }
    }, [showWorkspaceDropdown]);

    const handleWorkspaceSelect = async (worktreeId: string | null) => {
      if (!currentProject) return;

      const targetWorktree =
        worktreeId === null
          ? mainWorktree
          : activeWorktrees.find((worktree) => worktree.id === worktreeId) ?? null;

      await switchWorktreeContext(currentProject.id, targetWorktree, {
        openFreshNewChat: true,
      });
      setShowWorkspaceDropdown(false);
    };

    const handleWorktreeCreated = async (worktreeId: string) => {
      if (!currentProject) return;

      const refreshedWorktrees = useWorktreeStore.getState().worktrees;
      const createdWorktree =
        refreshedWorktrees.find((worktree) => worktree.id === worktreeId) ?? null;

      await switchWorktreeContext(currentProject.id, createdWorktree, {
        openFreshNewChat: true,
      });
      setShowCreateWorktreeModal(false);
      setShowWorkspaceDropdown(false);
    };

    const handleWorktreesImported = async (importedWorktreeIds?: string[]) => {
      if (!currentProject) return;

      await useWorktreeStore.getState().loadWorktrees(currentProject.id);
      const refreshedWorktrees = useWorktreeStore.getState().worktrees;
      const importedWorktree = importedWorktreeIds?.length
        ? refreshedWorktrees.find(
            (worktree) => worktree.id === importedWorktreeIds[0]
          ) ?? null
        : null;
      const refreshedCurrentWorktree = useWorktreeStore.getState().currentWorktree;
      const fallbackWorktree =
        importedWorktree ??
        refreshedCurrentWorktree ??
        refreshedWorktrees.find((worktree) => worktree.is_main) ??
        null;

      await switchWorktreeContext(currentProject.id, fallbackWorktree, {
        openFreshNewChat: true,
      });
      setShowDiscoverWorktreeModal(false);
      setShowWorkspaceDropdown(false);
    };

    const handleMinimize = async () => {
      if (window.electronAPI) {
        await window.electronAPI.minimizeWindow();
      }
    };

    const handleMaximize = async () => {
      if (window.electronAPI) {
        await window.electronAPI.maximizeWindow();
        setIsMaximized(!isMaximized);
      }
    };

    const handleClose = async () => {
      if (window.electronAPI) {
        await window.electronAPI.closeWindow();
      }
    };

    return (
      <header
        className="h-12 border-b border-border flex items-center bg-background dense-ui select-none cursor-move relative z-[100]"
        style={
          {
            WebkitAppRegion: "drag",
            WebkitUserSelect: "none",
            userSelect: "none",
          } as React.CSSProperties
        }
      >
        {/* Left side */}
        <div className="flex items-center flex-1 transition-[padding] duration-200 ease-in-out gap-1" style={{ paddingLeft: !isFullscreen && isMac && windowAligned ? '80px' : '12px' }}>

          {/* Toggle Chat Sidebar button */}
          {!projectPickerMode && onToggleChatSidebar && (
            <Tooltip
              content={`Toggle Chat Sidebar (${formatShortcut(
                "onToggleSidebar"
              )})`}
              placement="bottom"
              delay={300}
            >
              <button
                onClick={onToggleChatSidebar}
                className="header-icon-btn p-1.5 rounded text-xs transition-colors"
                aria-label="Toggle Chat Sidebar"
                style={{ WebkitAppRegion: "no-drag" } as React.CSSProperties}
              >
                <PanelLeft className="w-4 h-4" />
              </button>
            </Tooltip>
          )}

          {/* Project Picker button */}
          {!projectPickerMode && onNavigateToProjectPicker && (
            <Tooltip
              content="Switch Project"
              placement="bottom"
              delay={300}
            >
              <button
                onClick={onNavigateToProjectPicker}
                className="header-icon-btn p-1.5 rounded text-xs transition-colors"
                aria-label="Switch Project"
                style={{ WebkitAppRegion: "no-drag" } as React.CSSProperties}
              >
                <FolderOpen className="w-4 h-4" />
              </button>
            </Tooltip>
          )}

          {/* Workflows button */}
          {!projectPickerMode && onOpenWorkflows && (
            <Tooltip
              content={`Workflows (${formatShortcut("onOpenWorkflows")})`}
              placement="bottom"
              delay={300}
            >
              <button
                onClick={onOpenWorkflows}
                className="header-icon-btn p-1.5 rounded text-xs transition-colors"
                aria-label="Open Workflows"
                style={{ WebkitAppRegion: "no-drag" } as React.CSSProperties}
                data-onboarding="workflow-button"
              >
                <Workflow className="w-4 h-4" />
              </button>
            </Tooltip>
          )}

        </div>

        {/* Center - Project name and workspace selector */}
        <div className="absolute left-1/2 -translate-x-1/2 flex items-center justify-center">
          <div
            className="flex items-center gap-2 text-sm font-medium text-foreground/80 px-2 py-1 rounded-md bg-accent/20"
            style={{ WebkitAppRegion: "no-drag" } as React.CSSProperties}
            data-onboarding="workspace-indicator"
          >
            <button
              type="button"
              className="rounded px-2 py-0.5 transition-colors hover:bg-accent/60"
              onClick={() => onNavigateToProjectPicker?.()}
            >
              {currentProject?.name || (projectPickerMode ? "Select Project" : "")}
            </button>

            {!projectPickerMode && currentProject && (
              <>
                <span className="text-muted-foreground/50">/</span>
                <div className="relative" ref={workspaceDropdownRef}>
                  <button
                    type="button"
                    className="flex items-center gap-1.5 rounded px-2 py-0.5 text-muted-foreground transition-colors hover:bg-accent/60"
                    onClick={() => setShowWorkspaceDropdown((open) => !open)}
                    aria-haspopup="menu"
                    aria-expanded={showWorkspaceDropdown}
                  >
                    <FolderGit2 className="w-3.5 h-3.5 translate-y-[1px]" />
                    <span>{currentWorktree?.branch || currentWorktree?.name || mainWorktree?.branch || mainWorktree?.name || currentProject.default_branch || "main"}</span>
                    <ChevronDown className="w-3.5 h-3.5 opacity-60" />
                  </button>

                  {showWorkspaceDropdown && (
                    <div className="absolute left-0 top-full mt-2 min-w-64 overflow-hidden rounded-md border border-border/50 bg-background shadow-2xl z-[1000]">
                      <div className="max-h-80 overflow-y-auto py-1">
                        <button
                          type="button"
                          onClick={() => handleWorkspaceSelect(null)}
                          className="flex w-full items-start justify-between gap-3 px-3 py-2 text-left text-xs transition-colors hover:bg-accent/50"
                        >
                          <div>
                            <div className="font-semibold text-foreground">
                              {mainWorktree?.branch || mainWorktree?.name || currentProject.default_branch || "main"}
                            </div>
                            <div className="text-muted-foreground">Main workspace</div>
                          </div>
                          {(currentWorktree?.id ?? mainWorktree?.id) === mainWorktree?.id && (
                            <Check className="mt-0.5 h-3.5 w-3.5 text-primary" />
                          )}
                        </button>

                        {selectableWorkspaces.length > 0 ? (
                          selectableWorkspaces.map((worktree) => (
                            <button
                              key={worktree.id}
                              type="button"
                              onClick={() => handleWorkspaceSelect(worktree.id)}
                              className="flex w-full items-start justify-between gap-3 px-3 py-2 text-left text-xs transition-colors hover:bg-accent/50"
                            >
                              <div>
                                <div className="font-semibold text-foreground">
                                  {worktree.branch || worktree.name}
                                </div>
                              </div>
                              {currentWorktree?.id === worktree.id && (
                                <Check className="mt-0.5 h-3.5 w-3.5 text-primary" />
                              )}
                            </button>
                          ))
                        ) : (
                          <div className="px-3 py-2 text-xs text-muted-foreground">
                            No additional workspaces
                          </div>
                        )}
                      </div>

                      <div className="border-t border-border/50 bg-background">
                        <button
                          type="button"
                          onClick={() => {
                            setShowDiscoverWorktreeModal(true);
                            setShowWorkspaceDropdown(false);
                          }}
                          className="flex w-full items-center justify-center gap-2 px-3 py-2 text-xs text-primary transition-colors hover:bg-accent/50"
                        >
                          <Search className="h-3.5 w-3.5" />
                          <span className="font-medium">Discover workspace</span>
                        </button>
                        <button
                          type="button"
                          onClick={() => {
                            setShowCreateWorktreeModal(true);
                            setShowWorkspaceDropdown(false);
                          }}
                          className="flex w-full items-center justify-center gap-2 border-t border-border/50 px-3 py-2 text-xs text-primary transition-colors hover:bg-accent/50"
                        >
                          <LuFolderPlus className="h-3.5 w-3.5" />
                          <span className="font-medium">New workspace</span>
                        </button>
                      </div>
                    </div>
                  )}
                </div>
              </>
            )}
          </div>
        </div>

        {/* Right side - controls */}
        <div className="flex items-center flex-1 justify-end">
          {/* Draggable spacer to the right of search */}
          <div
            className="flex-1 min-w-4"
            style={{ WebkitAppRegion: "drag" } as React.CSSProperties}
          />

          {/* Control buttons (not draggable) */}
          <div
            className="flex items-center gap-1 pr-2 cursor-default"
            style={{ WebkitAppRegion: "no-drag" } as React.CSSProperties}
          >
            {/* Dev-only: Temporal UI button (shown in all modes) */}
            {isDev && (
              <Tooltip
                content="Open Temporal UI"
                placement="bottom"
                delay={300}
              >
                <button
                  onClick={() => {
                    // Get Temporal UI port from config
                    const temporalUIPort =
                      window.RELIANT_CONFIG?.temporalUIPort || 8233;
                    const temporalUIUrl = `http://localhost:${temporalUIPort}/namespaces/reliant/workflows`;
                    void openExternalLink(temporalUIUrl);
                  }}
                  className="header-icon-btn p-1.5 rounded text-xs transition-colors"
                  aria-label="Open Temporal UI"
                >
                  <Activity className="w-4 h-4" />
                </button>
              </Tooltip>
            )}

            {/* Feedback button */}
            {!projectPickerMode && onOpenFeedback && (
              <Tooltip
                content="Report bugs or suggest features"
                placement="bottom"
                delay={300}
              >
                <button
                  onClick={(e) => {
                    e.stopPropagation();
                    onOpenFeedback();
                  }}
                  className="header-icon-btn p-1.5 rounded text-xs transition-colors"
                  aria-label="Send Feedback"
                  style={{ WebkitAppRegion: "no-drag" } as React.CSSProperties}
                >
                  <RiFeedbackLine className="w-4 h-4" />
                </button>
              </Tooltip>
            )}

            {/* Terminal button */}
            {!projectPickerMode && onToggleTerminal && (
              <Tooltip
                content={`Toggle Terminal (${formatShortcut(
                  "onToggleTerminal"
                )})`}
                placement="bottom"
                delay={300}
              >
                <button
                  onClick={onToggleTerminal}
                  className="header-icon-btn p-1.5 rounded text-xs transition-colors"
                  aria-label="Toggle Terminal"
                >
                  <PanelBottom className="w-4 h-4" />
                </button>
              </Tooltip>
            )}

            {/* Toggle File Browser button */}
            {!projectPickerMode && onToggleFileBrowser && (
              <Tooltip
                content={`Toggle File Browser (${formatShortcut(
                  "onToggleFileBrowser"
                )})`}
                placement="bottom"
                delay={300}
              >
                <button
                  onClick={onToggleFileBrowser}
                  className="header-icon-btn p-1.5 rounded text-xs transition-colors"
                  aria-label="Toggle File Browser"
                >
                  <PanelRight className="w-4 h-4" />
                </button>
              </Tooltip>
            )}

            {/* Workspaces button */}
            {!projectPickerMode && onNavigateToWorktrees && (
              <Tooltip
                content={`Workspaces (${formatShortcut("onOpenWorktrees")})`}
                placement="bottom"
                delay={300}
              >
                <button
                  onClick={(e) => {
                    e.stopPropagation();
                    onNavigateToWorktrees();
                  }}
                  className="header-icon-btn p-1.5 rounded text-xs transition-colors"
                  aria-label="Workspaces"
                >
                  <FolderGit2 className="w-4 h-4" />
                </button>
              </Tooltip>
            )}

            {/* Config Health Indicator */}
            {!projectPickerMode && <ConfigHealthIndicator />}

            {/* Settings button */}
            {!projectPickerMode && onNavigateToSettings && (
              <Tooltip
                content={`Settings (${formatShortcut("onToggleSettings")})`}
                placement="bottom"
                delay={300}
              >
                <button
                  onClick={(e) => {
                    // Prevent any potential event bubbling
                    e.stopPropagation();
                    onNavigateToSettings();
                  }}
                  className="header-icon-btn p-1.5 rounded text-xs transition-colors"
                  aria-label="Settings"
                >
                  <Settings className="w-4 h-4" />
                </button>
              </Tooltip>
            )}

            {/* Window controls for non-Mac platforms */}
            {!isMac && (
              <>
                <div className="h-4 w-px bg-border mx-1" />
                <button
                  onClick={handleMinimize}
                  className="header-icon-btn p-1.5 rounded text-xs transition-colors"
                  aria-label="Minimize"
                >
                  <Minus className="w-4 h-4" />
                </button>
                <button
                  onClick={handleMaximize}
                  className="header-icon-btn p-1.5 rounded text-xs transition-colors"
                  aria-label={isMaximized ? "Restore" : "Maximize"}
                >
                  <Square className="w-3.5 h-3.5" />
                </button>
                <button
                  onClick={handleClose}
                  className="p-1.5 hover:bg-destructive hover:text-destructive-foreground rounded text-xs transition-colors"
                  aria-label="Close"
                >
                  <X className="w-4 h-4" />
                </button>
              </>
            )}
          </div>
        </div>
        {currentProject && (
          <>
            <CreateWorktreeModal
              isOpen={showCreateWorktreeModal}
              onClose={() => setShowCreateWorktreeModal(false)}
              onWorktreeCreated={handleWorktreeCreated}
              projectId={currentProject.id}
            />
            <DiscoverWorktreesModal
              isOpen={showDiscoverWorktreeModal}
              onClose={() => setShowDiscoverWorktreeModal(false)}
              onWorktreesImported={handleWorktreesImported}
              projectId={currentProject.id}
            />
          </>
        )}
      </header>
    );
  }
);