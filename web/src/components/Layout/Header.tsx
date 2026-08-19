import {
  Minus,
  Square,
  X,
  Activity,
  PanelBottom,
  PanelRight,
  PanelLeft,
  FolderGit2,
  Settings,
} from "lucide-react";
import {
  useState,
  forwardRef,
  useImperativeHandle,
  useEffect,
} from "react";
import { Tooltip } from "../ui/Tooltip";

import { useProjectStore, type Project } from "../../store/projectStore";
import type { SettingsSection } from "../../routeSchemas";
import { useShortcutsStore } from "../../store/shortcutsStore";
import { parseBinding } from "../../lib/keyboard/chord";
import { detectPlatform, formatBinding } from "../../lib/keyboard/platform";
import { useWorktreeStore } from "../../store/worktreeStore";
import { ConfigHealthIndicator } from "./ConfigHealthIndicator";
import { DaemonStatusDot } from "./DaemonStatusDot";
import { DetectedPortsChip } from "./DetectedPortsChip";
import { isDev } from "../../lib/constants";
import { openExternalLink } from "../../lib/open-link";
import { useTitleBarChrome } from "../../hooks/useTitleBarChrome";

interface HeaderProps {
  // Controls logo size in the app bar for tasteful variations
  logoSize?: "sm" | "md" | "lg" | "xl"; // sm:16px, md:20px, lg:24px, xl:28px
  // Whether this header is aligned to the window's left edge (traffic lights on macOS)
  // If false, we assume it's aligned to the content area (e.g., to the right of a vertical nav)
  windowAligned?: boolean;
  onNavigateToSettings?: () => void;
  onNavigateToWorktrees?: () => void;
  onNavigateToSettingsSection?: (section: SettingsSection) => void;
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
    },
    ref
  ) => {
    const [isMaximized, setIsMaximized] = useState(false);

    const {
      isElectron,
      trafficLightPadding,
      dragRegionStyle,
      noDragRegionStyle,
      showWindowControls,
    } = useTitleBarChrome({ alignedToWindowEdge: windowAligned });
    const currentProject = useProjectStore((state) => state.currentProject);
    const currentWorktree = useWorktreeStore((state) => state.currentWorktree);
    const worktrees = useWorktreeStore((state) => state.worktrees);
    const shortcuts = useShortcutsStore((state) => state.shortcuts);

    const activeWorktrees = worktrees.filter((worktree) => !worktree.deleted_at);
    const mainWorktree =
      activeWorktrees.find((worktree) => worktree.is_main) ?? null;



    // Helper to format shortcut display
    const formatShortcut = (handler: string): string => {
      const shortcut = Object.values(shortcuts).find(
        (s) => s.handler === handler
      );
      if (!shortcut) return "";

      const { isMac, isDesktop } = detectPlatform();
      const authored =
        shortcut.currentBinding ??
        (isDesktop ? shortcut.defaultBinding : shortcut.defaultWebBinding);
      return formatBinding(parseBinding(authored, isMac), isMac);
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
        className={`h-12 border-b border-border flex items-center bg-background dense-ui select-none relative z-[100] ${isElectron ? "cursor-move" : ""}`}
        style={dragRegionStyle}
      >
        {/* Left side */}
        <div className="flex items-center flex-1 transition-[padding] duration-200 ease-in-out gap-1" style={{ paddingLeft: trafficLightPadding }}>

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
                style={noDragRegionStyle}
              >
                <PanelLeft className="w-4 h-4" />
              </button>
            </Tooltip>
          )}

        </div>

        {/* Center - Project name and workspace selector */}
        <div className="absolute left-1/2 -translate-x-1/2 flex items-center justify-center">
          <div
            className="flex items-center gap-2 text-sm font-medium text-foreground/80 px-2 py-1 rounded-md bg-accent/20"
            style={noDragRegionStyle}
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
                <span className="flex items-center gap-1.5 px-2 py-0.5 text-muted-foreground">
                  <FolderGit2 className="w-3.5 h-3.5 translate-y-[1px]" />
                  <span>{currentWorktree?.branch || currentWorktree?.name || mainWorktree?.branch || mainWorktree?.name || currentProject.default_branch || "main"}</span>
                </span>
              </>
            )}
          </div>
        </div>

        {/* Right side - controls */}
        <div className="flex items-center flex-1 justify-end">
          {/* Draggable spacer to the right of search */}
          <div
            className="flex-1 min-w-4"
            style={dragRegionStyle}
          />

          {/* Control buttons (not draggable) */}
          <div
            className="flex items-center gap-1 pr-2 cursor-default"
            style={noDragRegionStyle}
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


            {/* Detected workspace ports (open preview) */}
            {!projectPickerMode && <DetectedPortsChip />}

            {/* Daemon status */}
            {!projectPickerMode && <DaemonStatusDot />}

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

            {/* Settings. Deliberately NOT gated on `projectPickerMode`, unlike
                every other control on this row. The Sidebar — which holds the
                app's only other Settings link — renders solely inside the main
                app shell, and that shell requires a selected project. A user
                whose daemon never provisions never gets a project, so without
                this button they cannot reach /settings at all, and therefore
                cannot sign out. /settings itself needs neither a daemon nor a
                project; it is gated only on auth. */}
            {onNavigateToSettings && (
              <Tooltip
                content={`Settings (${formatShortcut("onToggleSettings")})`}
                placement="bottom"
                delay={300}
              >
                <button
                  onClick={onNavigateToSettings}
                  className="header-icon-btn p-1.5 rounded text-xs transition-colors"
                  aria-label="Settings"
                  data-testid="header-settings-button"
                >
                  <Settings className="w-4 h-4" />
                </button>
              </Tooltip>
            )}

            {/* Window controls for non-Mac Electron only — browsers can't drive these */}
            {showWindowControls && (
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
      </header>
    );
  }
);
