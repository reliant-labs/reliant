// ModernApp - Main application component
import { useSearch, useNavigate, useLocation } from '@tanstack/react-router';
import { useQueryClient } from '@tanstack/react-query';
import { GITHUB_CREDENTIAL_QUERY_KEY } from './hooks/useGitHubCredential';
import { ChatState } from "./gen/reliant/v1/chat_pb";
import type { Chat } from "./api/client";
import { logger } from "./lib/logger";
import { cn } from "./lib/utils";
import { isWorkflowSurfacePath } from "./lib/workflowsArea";
import {
  useEffect,
  useState,
  useCallback,
  useMemo,
  useRef,
  startTransition,
} from "react";
import { ChatInterface } from "./components/Chat/ChatInterface";
import { Sidebar } from "./components/Layout/Sidebar";
import { TabbedViewerPanel } from "./components/Layout/TabbedViewerPanel";
import { useViewerStore } from "./store/viewerStore";
import { mostRecentProject, useProjectStore } from "./store/projectStore";
import { ProjectFilterRouteSync, useRouteProjectId } from "./components/Layout/ProjectFilterRouteSync";
import { fetchProjects, getCachedProjects, useProjects } from "./hooks/project-queries";
import { useFileDeletionStore } from "./store/fileDeletionStore";
import { createFile, createFolder, moveFile, deleteFileOrFolder } from "./api/fileSystem";
import { toast } from "./lib/toast-manager";
import { ResizableSidebar } from "./components/Layout/ResizableSidebar";
import { RightSidebar } from "./components/FileBrowser/RightSidebar";
import { GlobalSearch, FindReplace } from "./components/FileBrowser";
import { QuickFileOpen } from "./components/Layout/QuickFileOpen";
import { HomeNewChat } from "./components/Apps/HomeNewChat";
import { useCurrentUser } from "@/hooks/useOnboardingQueries";
import { approveAllPendingApprovals } from "./hooks/approval-queries";
import { useChatListWithWorkspaces, getCachedChatList, getChatFromCache } from "./hooks/chat-queries";


import { Header, type HeaderRef } from "./components/Layout/Header";
import { NoProjectsState } from "./components/Projects/NoProjectsState";
import { InitializeGitModal } from "./components/Git/InitializeGitModal";
import { RescanModal } from "./components/Projects/RescanModal";

import { TerminalPanel } from "./components/Terminal/TerminalPanel";
import { LoadingSpinner } from "./components/Layout/LoadingSpinner";
import { GlobalUpdateHandler } from "./components/Settings/GlobalUpdateHandler";
import { useChatStore } from "./store/chatStore";
import { useActivityStore, ChatActivity, isBusyActivity } from "./store/activityStore";
import { useChatNavigationStore } from "./store/chatNavigationStore";
import { useWindowContext } from "./hooks/useWindowContext";
import { useOpenProjectListener } from "./hooks/useOpenProjectListener";
import { openChat, openNewChat } from "./store/openChat";
import { openAddProject } from "./store/addProjectDialogStore";
import { useKeyboardShortcuts } from "./hooks/useKeyboardShortcuts";
import { useShortcutHost, useShortcutLayer } from "./hooks/useShortcutHost";
import { useElectronIPC } from "./hooks/useElectronIPC";
import { useSidebarOverlay } from "./hooks/useSidebarOverlay";
import { useGitInitialization } from "./hooks/useGitInitialization";
import { useDaemonStatus } from "./hooks/useDaemonStatus";
import { useProjectRescan } from "./hooks/useProjectRescan";
import { useCancelOnUnload } from "./hooks/useCancelOnUnload";
import { useTerminalStore } from "./store/terminalStore";
import { useWorktreeStore } from "./store/worktreeStore";
import { resolveTerminalWorkingDir } from "./lib/terminalWorkingDir";
import { useUpdateStreamConnection } from "./hooks/useUpdateStreamConnection";

import { useNotificationStore, startPermissionRefresh } from "./store/notificationStore";
import { useWorkspaceStateStore, type RightSidebarTab } from "./store/workspaceStateStore";
import {
  clearPane,
  focusEventName,
  focusPane,
  focusPrevious,
  isFocusWithin,
  type Pane,
} from "./lib/keyboard/focus";
import { useWorkspaceRestore, useAutoSaveWorkspaceState } from "./hooks/useWorkspaceRestore";
import { focusChatInput } from "./hooks/useFocusManager";
import { useTourStore } from "./store/tourStore";
import { useModalStore } from "./store/modalStore";
import { useCachedWorkflows } from "./hooks/workflow-queries";
import { trackEvent } from "./lib/analytics";
import { useCurrentWorktree, resolveWorktree, useCachedWorktrees, getCurrentWorktree, invalidateWorktreeLists } from "./hooks/worktree-queries";
import { restoreLastWorktree } from "./hooks/worktree-actions";

// Stable empty reference so the useChatList default doesn't churn the tray effect.
const EMPTY_CHAT_LIST: Chat[] = [];

function App() {
  const chatError = useChatStore((state) => state.error);
  const activeChatId = useChatStore((state) => state.activeChatId);
  const currentProject = useProjectStore((state) => state.currentProject);
  // Chat list from the React Query cache (the single source of truth), used by
  // the tray-status effect below: the sidebar's list, every project or the
  // filter's one.
  const projectFilterId = useWorkspaceStateStore((state) => state.projectFilterId);
  const { data: sidebarList } = useChatListWithWorkspaces(projectFilterId);
  const chats = sidebarList?.chats ?? EMPTY_CHAT_LIST;
  const selectProject = useProjectStore((state) => state.selectProject);
  const { projects, isLoading: projectsLoading } = useProjects();
  // Onboarding state (`plan`, `reset-onboarding`) now lives on /onboarding —
  // see OnboardingRoute. We still read the github_* params here because non-
  // onboarding OAuth callers (GitHubSyncStatus, AddRepoModal) return to /
  // or /project/$id and we want a toast + credential cache refresh at the
  // route they came back to. Settings has its own local handler.
  const { github_connected, github_installed, github_error, github_error_msg } = useSearch({ from: '/_app' });
  const navigate = useNavigate();
  const { data: currentUser, isLoading: isUserLoading } = useCurrentUser();
  const queryClient = useQueryClient();

  const isTerminalOpen = useTerminalStore((state) => state.isOpen);
  const toggleTerminal = useTerminalStore((state) => state.toggleTerminal);
  const createTerminalSession = useTerminalStore(
    (state) => state.createSession
  );
  const openTerminal = useTerminalStore((state) => state.openTerminal);
  const currentWorktree = useCurrentWorktree();
  // The tray lists the workspaces the app already holds; it never pages them in.
  const worktrees = useCachedWorktrees(currentProject?.id);

  // Backend ready gate is defined later in this file; we must not reference it
  // before initialization.
  const [isBackendReady, setIsBackendReady] = useState(false);

  // The current project's workflows as already cached (the tray menu lists
  // them; it does not fetch them on its own).
  const cachedWorkflows = useCachedWorkflows();

  // Track session start once on mount
  useEffect(() => {
    trackEvent("session_start");
  }, []);

  // Update browser tab title with current project name
  useEffect(() => {
    document.title = currentProject?.name
      ? `Reliant - ${currentProject.name}`
      : "Reliant";
  }, [currentProject?.name]);

  // UI state - use persisted values as defaults, initialized lazily from workspace state
  // NOTE: We use lazy initialization with getState() instead of subscribing to the whole
  // worktree state object, which would cause re-renders on every state change.
  const [showFileBrowser, setShowFileBrowserLocal] = useState(() => {
    const projectId = useProjectStore.getState().currentProject?.id;
    const worktreeId = useWorktreeStore.getState().currentWorktreeId ?? null;
    if (!projectId) return false;
    return useWorkspaceStateStore.getState().getWorktreeState(projectId, worktreeId).rightPanelState.fileBrowser;
  });
  
  // Left sidebar (chat sidebar) state - global across all workspaces
  const showChatSidebar = useWorkspaceStateStore((state) => state.leftSidebarExpanded);
  const setShowChatSidebar = useCallback((value: boolean | ((prev: boolean) => boolean)) => {
    const currentValue = useWorkspaceStateStore.getState().leftSidebarExpanded;
    const newValue = typeof value === 'function' ? value(currentValue) : value;
    useWorkspaceStateStore.getState().setLeftSidebarExpandedGlobal(newValue);
  }, []);
  
  // Sidebar overlay behavior for collapsed sidebar
  const {
    showOverlaySidebar,
    isSidebarHovered,
    handleSidebarMouseEnter,
    handleSidebarMouseLeave,
    handleOverlayMouseEnter,
    handleOverlayMouseLeave,
  } = useSidebarOverlay({ isSidebarExpanded: showChatSidebar });

  // Wrap setters to also persist to workspace state
  const setShowFileBrowser = useCallback((value: boolean | ((prev: boolean) => boolean)) => {
    setShowFileBrowserLocal((prev) => {
      const newValue = typeof value === 'function' ? value(prev) : value;
      // Persist to workspace state
      const projectId = useProjectStore.getState().currentProject?.id;
      const worktreeId = useWorktreeStore.getState().currentWorktreeId ?? null;
      if (projectId) {
        useWorkspaceStateStore.getState().setRightPanelState(projectId, worktreeId, { fileBrowser: newValue });
      }
      return newValue;
    });
  }, []);

  // Auto-save workspace state on visibility change or unmount
  useAutoSaveWorkspaceState();
  // Subscribe to viewers array so component re-renders when viewers change
  const viewers = useViewerStore((state) => state.viewers);
  // The viewer "current project" is now the projectStore's currentProject — no
  // dual-write. Use currentProject?.id directly.
  const viewerProjectId = currentProject?.id ?? null;
  const activeViewerId = useViewerStore((state) => state.activeViewerId);
  const setActiveViewer = useViewerStore((state) => state.setActiveViewer);
  const location = useLocation();
  // Startup restoration and the mounted filter bridge share route identity.
  const routeProjectId = useRouteProjectId();
  const isSettingsMode = location.pathname.startsWith("/settings");
  const isWorkflowMode = isWorkflowSurfacePath(location.pathname);
  // Calculate hasOpenViewers based on subscribed state
  const hasOpenViewers = viewerProjectId
    ? viewers.filter((v) => v.projectId === viewerProjectId).length > 0
    : viewers.length > 0;

  const headerRef = useRef<HeaderRef>(null);

  // Note: Removed file polling - now handled by viewer store and individual viewer components

  // Restore panel state when worktree changes (file browser is per-worktree, chat sidebar is global)
  useEffect(() => {
    if (!currentProject?.id) return;
    
    const worktreeId = currentWorktree?.id ?? null;
    const state = useWorkspaceStateStore.getState().getWorktreeState(currentProject.id, worktreeId);
    
    // Restore file browser visibility (per-worktree state)
    // Note: Chat sidebar is now global and handled by the store subscription
    setShowFileBrowserLocal(state.rightPanelState.fileBrowser);

    // Terminal visibility is also per-worktree. terminalStore.isOpen is not
    // persisted itself, so without this a project selected outside the restore
    // path (first run, manual switch) would ignore the saved value. Restored,
    // not opened: the user did not ask for the terminal, so it takes no focus.
    useTerminalStore.getState().restoreOpen(state.terminalOpen);

    logger.debug("[ModernApp] Restored panel state for worktree", {
      worktreeId,
      fileBrowser: state.rightPanelState.fileBrowser,
      terminalOpen: state.terminalOpen,
    });
  }, [currentProject?.id, currentWorktree?.id]);

  // Listen for external requests to open the file browser (from ChatHeader, etc.)
  // Using events instead of store subscription to avoid potential infinite loop issues
  useEffect(() => {
    const handleOpenFileBrowser = () => {
      setShowFileBrowserLocal(true);
    };
    window.addEventListener("open-file-browser", handleOpenFileBrowser);
    return () => {
      window.removeEventListener("open-file-browser", handleOpenFileBrowser);
    };
  }, []);

  // Auto-activate viewers when files are open but no viewer is active
  useEffect(() => {
    const projectViewers = viewerProjectId
      ? viewers.filter((v) => v.projectId === viewerProjectId)
      : viewers;

    const hasFileViewers = projectViewers.length > 0;

    // If files are open but no viewer is active, activate first file
    if (hasFileViewers && !activeViewerId && projectViewers[0]) {
      setActiveViewer(projectViewers[0].id);
    }
  }, [
    viewers,
    viewers.length,
    viewerProjectId,
    activeViewerId,
    setActiveViewer,
  ]);

  // Initialize window context handling
  useWindowContext();

  // Listen for CLI open-project events (reliant <path>)
  useOpenProjectListener();

  // Fire best-effort cancel when closing the browser tab/window
  useCancelOnUnload();

  // Git initialization logic
  const {
    showGitInitModal,
    gitInitProjectInfo,
    checkGitInitialization,
    handleCloseGitInitModal,
    handleDismissGitInit,
  } = useGitInitialization();

  // Mirrored into a ref so the git-init effect can check "is the dialog
  // already open?" without taking it as a dependency (which would re-run the
  // effect the moment the modal opens).
  const showGitInitModalRef = useRef(showGitInitModal);
  showGitInitModalRef.current = showGitInitModal;



  // Project rescan logic
  const {
    showRescanModal,
    commitCount,
    checkForRescan,
    handleRescan,
    handleDismissRescan,
    handleDismissForever,
  } = useProjectRescan();

  // NOTE: isBackendReady is declared near the top of App() to avoid TDZ issues
  const [showGlobalSearch, setShowGlobalSearch] = useState(false);
  const [showFindReplace, setShowFindReplace] = useState(false);
  const [showQuickFileOpen, setShowQuickFileOpen] = useState(false);

  // Git initialization modal handlers
  const handleGitInitSuccess = useCallback(async () => {
    // Reload the project to get updated git status
    // Use getState() to get fresh project reference (closure may have stale value)
    const projectStore = useProjectStore.getState();
    const projectId = projectStore.currentProject?.id;
    
    if (projectId) {
      await projectStore.refreshCurrentProject();
      logger.info('[ModernApp] Refreshed project after git initialization');
      
      // Reload worktrees to get the updated main worktree with correct branch name
      // This is important because git init may have created a branch with a custom name
      // (e.g., "master" instead of "main") and the main worktree needs to reflect this
      await invalidateWorktreeLists(queryClient);
      await restoreLastWorktree(projectId);
      logger.info('[ModernApp] Reloaded worktrees after git initialization');
    }
    handleCloseGitInitModal();
  }, [handleCloseGitInitModal, queryClient]);

  // Get current worktreeId from the global worktree store (single source of truth)
  const getCurrentWorktreeId = useCallback(() => {
    return useWorktreeStore.getState().currentWorktreeId || undefined;
  }, []);

  // Working directory for a new terminal in the current workspace, or
  // undefined when there is none to give yet (a workspace still being created,
  // or one that failed). Callers must not create a session on undefined: the
  // daemon would start the shell in its own cwd, and this used to fall back to
  // the project root — a shell in the main checkout under a branch's tab.
  // TerminalPanel creates the session itself once the path settles.
  const getTerminalWorkingDir = useCallback(() => {
    const worktree = getCurrentWorktree();
    const resolved = resolveTerminalWorkingDir(worktree, currentProject);
    if (resolved.kind !== "ready") {
      logger.info("[Terminal] Workspace has no directory yet; not creating a terminal", {
        worktreeId: worktree?.id,
        state: resolved.kind,
      });
      return undefined;
    }
    return resolved.path;
  }, [currentProject]);

  // Use keyboard shortcuts hook for production security
  useKeyboardShortcuts({
    disableDevTools: true,
  });

  // The keyboard host owns the dispatcher and the app-level overlays (command
  // palette, chat search); this component contributes its workspace handlers
  // to it, below, once the workspace is ready.
  const shortcutHost = useShortcutHost();

  // The search overlays this component owns.
  const closeLocalModals = useCallback(() => {
    setShowGlobalSearch(false);
    setShowQuickFileOpen(false);
    setShowFindReplace(false);
  }, []);

  // Before opening one overlay, close every other — the host's included.
  const closeAllModals = useCallback(() => {
    closeLocalModals();
    shortcutHost?.closeOverlays();
  }, [closeLocalModals, shortcutHost]);

  /** Reveal the right sidebar and select a tab. */
  const showSidebarTab = useCallback(
    (tab: RightSidebarTab) => {
      const projectId = currentProject?.id;
      if (!projectId) return;
      setShowFileBrowser(true);
      useWorkspaceStateStore
        .getState()
        .setRightSidebarTab(projectId, currentWorktree?.id ?? null, tab);
    },
    [currentProject?.id, currentWorktree?.id, setShowFileBrowser],
  );

  /**
   * Move the sidebar's project filter to the next/previous project (from All
   * projects, to the first/last). Wraps around, matching chat cycling.
   */
  const cycleProject = useCallback(
    async (direction: 1 | -1) => {
      const projects = getCachedProjects();
      if (projects.length === 0) return;
      const filter = useWorkspaceStateStore.getState().projectFilterId;
      const index = projects.findIndex((p) => p.id === filter);
      const nextIndex =
        index === -1
          ? direction === 1 ? 0 : projects.length - 1
          : (index + direction + projects.length) % projects.length;
      const next = projects[nextIndex];
      if (next && next.id !== filter) {
        await useProjectStore.getState().setProjectFilter(next.id);
      }
    },
    [],
  );

  // Memoize keyboard shortcut handlers to prevent recreation on every render
  const keyboardHandlers = useMemo(
    () => ({
      onNewChat: () => {
        // The new-chat view, in the filter's project, else the open chat's,
        // else the last one used (openNewChat).
        void openNewChat();
      },
      onCloseTab: () => {
        try {
          // Priority 1: Close active viewer tab if any are open
          const viewerStore = useViewerStore.getState();
          const { viewers } = viewerStore;

          logger.info("🗑️ Cmd+W pressed - State:", {
            viewersCount: viewers.length,
          });

          const viewerClosed = viewerStore.closeActiveViewer();

          if (viewerClosed) {
            logger.info("🗑️ Cmd+W: Closed active viewer tab");
            return;
          }

          // Priority 2: No viewers, close the window
          if (viewers.length === 0 && window.electronAPI?.closeWindowIfNoTabs) {
            logger.info("🗑️ Cmd+W: No tabs remaining, closing window");
            window.electronAPI.closeWindowIfNoTabs(0);
          } else {
            logger.info(
              "🗑️ Cmd+W: No active viewer to close"
            );
          }
        } catch (error) {
          logger.error("🛡️ Cmd+W handler error:", error);
        }
      },

      onNextChat: () => {
        const { navigateNext } = useChatNavigationStore.getState();
        startTransition(() => {
          navigateNext();
        });
      },
      onPrevChat: () => {
        const { navigatePrev } = useChatNavigationStore.getState();
        startTransition(() => {
          navigatePrev();
        });
      },
      onNextFileTab: () => {
        const { nextViewer } = useViewerStore.getState();
        nextViewer();
      },
      onPrevFileTab: () => {
        const { prevViewer } = useViewerStore.getState();
        prevViewer();
      },

      // New search modal handlers
      onQuickFileOpen: () => {
        closeAllModals();
        setShowQuickFileOpen(true);
      },
      onFindInFiles: () => {
        closeAllModals();
        setShowGlobalSearch(true);
      },
      onFindReplace: () => {
        closeAllModals();
        setShowFindReplace(true);
      },
      onStopStreaming: () => {
        // Pause the active chat. Registered only while the workspace is
        // mounted, so there is no Settings or workflow route to defer to: off
        // the workspace Escape is not a shortcut at all (lib/keyboard/scope).
        //
        // Modals no longer need checking here. An open modal puts the
        // dispatcher in the `modal` context, where dismissModal shadows this
        // handler entirely — so if we are running at all, there is no modal up.
        // The old five-boolean check could only see ModernApp's own modals and
        // paused the chat behind every other one.
        if (activeChatId) {
          const { pauseChat } =
            useChatStore.getState();
          const activity = useActivityStore.getState().activities.get(activeChatId);
          const isBusy = isBusyActivity(activity);

          // Only stop if chat is actually busy
          if (isBusy) {
            logger.info(
              "🛑 ESC pressed - pausing chat for:",
              activeChatId
            );

            // Pause the backend workflow (keeps it resumable)
            pauseChat(activeChatId).catch((error) => {
              logger.error("Failed to pause chat via ESC:", error);
            });
          }
        }
      },
      onApproveToolRequests: () => {
        // Find the active chat and approve all pending tool requests.
        // Approvals are server data in the React Query cache; the helper reads
        // pending straight from the cache and batch-approves optimistically.
        if (activeChatId) {
          logger.info(
            "✅ Cmd+Enter pressed - approving all tool requests for:",
            activeChatId,
          );
          void approveAllPendingApprovals(activeChatId);
        }
      },
      onToggleTerminal: () => {
        // If terminal is already open, check if it has focus
        if (isTerminalOpen) {
          // Check if terminal has focus by checking if active element is inside xterm
          const activeElement = document.activeElement;
          const isTerminalFocused = activeElement?.closest(".xterm");

          if (isTerminalFocused) {
            // Terminal has focus, close it and focus chat input
            toggleTerminal();
            // Focus chat input after terminal closes
            focusChatInput();
          } else {
            // Terminal is open but not focused, focus it
            window.dispatchEvent(new CustomEvent("focus-terminal"));
          }
        } else {
          // Terminal is closed, open it (opening asks the terminal for focus)
          toggleTerminal();
        }
      },
      onClearTerminal: () => {
        // The active session's terminal clears itself: only it holds the
        // xterm instance.
        window.dispatchEvent(new CustomEvent("clear-active-terminal"));
      },
      onNewTerminal: () => {
        // Open terminal panel if not open
        if (!isTerminalOpen) {
          openTerminal();
        }
        // Create new terminal session with context-aware working directory, project ID, and worktree ID
        const workingDir = getTerminalWorkingDir();
        if (!workingDir) return;
        const projectId = currentProject?.id;
        const worktreeId = getCurrentWorktreeId();
        createTerminalSession(workingDir, projectId, worktreeId);
      },
      onFocusChat: () => {
        // Clear active viewer
        const { setActiveViewer } = useViewerStore.getState();
        setActiveViewer(null);
        focusPane("chat-input");
      },
      onFocusFileEditor: () => {
        // Focus the active file editor
        const { activeViewerId } = useViewerStore.getState();
        if (activeViewerId) {
          window.dispatchEvent(new CustomEvent('file-viewer-focus', { 
            detail: { viewerId: activeViewerId } 
          }));
        }
        // Tracked as a pane move so `focusPrevious` can come back here.
        focusPane("editor");
      },
      onReopenLastClosedFile: () => {
        // Reopen the last closed file
        const { reopenLastClosedFile } = useViewerStore.getState();
        reopenLastClosedFile();
      },
      onCutFile: () => {
        // Always dispatch to file tree handler - it will prioritize focused path over active viewer
        window.dispatchEvent(new CustomEvent('file-tree-cut'));
      },
      onCopyFile: () => {
        // Always dispatch to file tree handler - it will prioritize focused path over active viewer
        window.dispatchEvent(new CustomEvent('file-tree-copy'));
      },
      onPasteFile: () => {
        // Paste file - paste to focused directory in file tree
        window.dispatchEvent(new CustomEvent('file-tree-paste'));
      },
      onToggleFileBrowser: () => {
        setShowFileBrowser((prev) => {
          const next = !prev;
          // Closing a pane the cursor is inside would strand focus on a
          // detached element, so hand it back to where it came from.
          if (!next && isFocusWithin("right-sidebar")) {
            clearPane("right-sidebar");
            focusPrevious();
          }
          return next;
        });
      },
      onToggleSidebar: () => {
        setShowChatSidebar((prev) => {
          const next = !prev;
          if (!next && isFocusWithin("left-sidebar")) {
            clearPane("left-sidebar");
            focusPrevious();
          }
          return next;
        });
      },
      onNextRightSidebarTab: () => {
        const projectId = currentProject?.id;
        if (!projectId) return;
        const worktreeId = currentWorktree?.id ?? null;
        useWorkspaceStateStore.getState().nextRightSidebarTab(projectId, worktreeId);
      },
      onPrevRightSidebarTab: () => {
        const projectId = currentProject?.id;
        if (!projectId) return;
        const worktreeId = currentWorktree?.id ?? null;
        useWorkspaceStateStore.getState().prevRightSidebarTab(projectId, worktreeId);
      },

      // --- Right sidebar: direct tab access ---
      // Selecting a tab also reveals the sidebar; jumping to a hidden tab would
      // otherwise look like the shortcut did nothing.
      onSidebarFiles: () => showSidebarTab("files"),
      onSidebarChanges: () => showSidebarTab("changes"),
      onSidebarTasks: () => showSidebarTab("tasks"),
      onSidebarProcesses: () => showSidebarTab("processes"),
      onSidebarBrowser: () => showSidebarTab("browser"),
      onFocusRightSidebar: () => {
        // Reveal before focusing — focusing a hidden pane does nothing.
        setShowFileBrowser(true);
        // The active tab's own arrow/Enter handling takes over from there; we
        // deliberately do not implement sidebar-level item navigation.
        focusPane("right-sidebar");
      },
      onFocusLeftSidebar: () => {
        setShowChatSidebar(true);
        focusPane("left-sidebar");
      },
      onFocusTranscript: () => {
        focusPane("transcript");
      },
      onFocusTerminal: () => {
        if (!isTerminalOpen) openTerminal();
        focusPane("terminal");
      },
      onFocusPrevious: () => {
        focusPrevious();
      },
      onRunPackageCommand: () => {
        showSidebarTab("processes");
        window.dispatchEvent(new CustomEvent("focus-right-sidebar"));
      },

      // --- Chat and project switching ---
      onSwitchWorktree: () => {
        if (currentProject?.id) {
          useViewerStore.getState().openWorktreesViewer(currentProject.id);
        }
      },
      onNextProject: () => {
        void cycleProject(1);
      },
      onPrevProject: () => {
        void cycleProject(-1);
      },

      onDismissModal: () => {
        // Modals that own their Escape (the shared ui/Modal, the command
        // palette, the search overlays) close themselves on the same keydown.
        // This entry exists so the `modal` context claims Escape at all and
        // stopStreaming cannot fire behind them; closing ModernApp's own
        // modals here covers the ones whose open state we hold.
        closeAllModals();
      },
      onNextAttentionChat: () => {
        const { navigateToNextAttention } = useChatNavigationStore.getState();
        void navigateToNextAttention().then((found) => {
          if (!found) toast.success("No chats need your attention");
        });
      },
    }),

    [
      closeAllModals,
      activeChatId,
      toggleTerminal,
      isTerminalOpen,
      openTerminal,
      createTerminalSession,
      getTerminalWorkingDir,
      getCurrentWorktreeId,
      currentProject?.id,
      currentWorktree?.id,
      setShowFileBrowser,
      setShowChatSidebar,
      showSidebarTab,
      cycleProject,
    ]
  );

  // Register file operations undo/redo handler - do this early but after React is ready
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      // Check if Cmd+Z (Mac) or Ctrl+Z (Windows/Linux) is pressed
      const isUndo = (e.metaKey || e.ctrlKey) && e.key === "z" && !e.shiftKey;
      const isRedo = (e.metaKey || e.ctrlKey) && (e.key === "z" && e.shiftKey || e.key === "y");

      if (!isUndo && !isRedo) {
        return;
      }

      // IMMEDIATELY check if there are any file operations - do this FIRST
      const store = useFileDeletionStore.getState();
      const lastAvailableUndo = store.getLastOperation();
      const canUndo = !!lastAvailableUndo && (lastAvailableUndo.type !== "delete" || lastAvailableUndo.data.canUndo);

      if (!canUndo) {
        return; // No undoable file operations, let other handlers process Cmd+Z
      }

      // Check if we're in an input field
      const activeElement = document.activeElement;
      const isInputFocused =
        activeElement?.tagName === "INPUT" ||
        activeElement?.tagName === "TEXTAREA" ||
        activeElement?.getAttribute("contenteditable") === "true" ||
        activeElement?.closest('.monaco-editor') !== null ||
        activeElement?.closest('.monaco-editor .inputarea') !== null;

      if (isInputFocused) {
        return; // Let inputs handle their own undo/redo
      }

      // Get project
      const projectStore = useProjectStore.getState();
      const currentProjectId = projectStore.currentProject?.id;

      if (!currentProjectId) {
        return;
      }

      // Get the most recent operation
      const lastOp = lastAvailableUndo;
      if (!lastOp || 
          (lastOp.type === "delete" && lastOp.data.projectId !== currentProjectId) ||
          (lastOp.type === "move" && lastOp.data.projectId !== currentProjectId) ||
          (lastOp.type === "copy" && lastOp.data.projectId !== currentProjectId)) {
        return;
      }

      // STOP EVERYTHING IMMEDIATELY
      e.preventDefault();
      e.stopPropagation();
      e.stopImmediatePropagation();

      // Handle undo
      if (isUndo) {
        (async () => {
          try {
            if (lastOp.type === "delete") {
              // Restore deleted file
              if (lastOp.data.type === "file") {
                await createFile(
                  lastOp.data.path,
                  lastOp.data.content || "",
                  lastOp.data.worktreeId
                );
                toast.success(`File restored: ${lastOp.data.path.split("/").pop()}`);
              } else {
                await createFolder(lastOp.data.path, lastOp.data.worktreeId);
                toast.success(`Folder restored: ${lastOp.data.path.split("/").pop()}`);
              }
              store.removeLastDeletedFile();
              window.dispatchEvent(new CustomEvent("file-deletion-undone", {
                detail: { path: lastOp.data.path }
              }));
            } else if (lastOp.type === "move") {
              // Undo move: move file back to source
              await moveFile(lastOp.data.destinationPath, lastOp.data.sourcePath, lastOp.data.worktreeId);
              toast.success(`Move undone: ${lastOp.data.fileName}`);
              store.removeLastMovedFile();
              window.dispatchEvent(new CustomEvent("file-move-undone", {
                detail: { path: lastOp.data.sourcePath }
              }));
            } else if (lastOp.type === "copy") {
              // Undo copy: delete the copied file
              await deleteFileOrFolder(lastOp.data.destinationPath, lastOp.data.worktreeId);
              toast.success(`Copy undone: ${lastOp.data.fileName}`);
              store.removeLastCopiedFile();
              window.dispatchEvent(new CustomEvent("file-copy-undone", {
                detail: { path: lastOp.data.destinationPath }
              }));
            }
          } catch (error) {
            console.error('[FileOperationUndo] Failed to undo operation:', error);
            toast.error(
              `Failed to undo: ${error instanceof Error ? error.message : "Unknown error"}`
            );
          }
        })();
      }
      // Note: Redo would require maintaining a redo stack, which is more complex
      // For now, we'll just handle undo
    };

    const handleUndoEvent = () => {
      // Trigger the same logic as keyboard shortcut
      const store = useFileDeletionStore.getState();
      const lastAvailableUndo = store.getLastOperation();
      if (!lastAvailableUndo || (lastAvailableUndo.type === "delete" && !lastAvailableUndo.data.canUndo)) return;

      const projectStore = useProjectStore.getState();
      const currentProjectId = projectStore.currentProject?.id;
      if (!currentProjectId) return;

      const lastOp = lastAvailableUndo;
      if (!lastOp || 
          (lastOp.type === "delete" && lastOp.data.projectId !== currentProjectId) ||
          (lastOp.type === "move" && lastOp.data.projectId !== currentProjectId) ||
          (lastOp.type === "copy" && lastOp.data.projectId !== currentProjectId)) {
        return;
      }

      (async () => {
        try {
          if (lastOp.type === "delete") {
            if (lastOp.data.type === "file") {
              await createFile(
                lastOp.data.path,
                lastOp.data.content || "",
                lastOp.data.worktreeId
              );
              toast.success(`File restored: ${lastOp.data.path.split("/").pop()}`);
            } else {
              await createFolder(lastOp.data.path, lastOp.data.worktreeId);
              toast.success(`Folder restored: ${lastOp.data.path.split("/").pop()}`);
            }
            store.removeLastDeletedFile();
            window.dispatchEvent(new CustomEvent("file-deletion-undone", {
              detail: { path: lastOp.data.path }
            }));
          } else if (lastOp.type === "move") {
            await moveFile(lastOp.data.destinationPath, lastOp.data.sourcePath, lastOp.data.worktreeId);
            toast.success(`Move undone: ${lastOp.data.fileName}`);
            store.removeLastMovedFile();
            window.dispatchEvent(new CustomEvent("file-move-undone", {
              detail: { path: lastOp.data.sourcePath }
            }));
          } else if (lastOp.type === "copy") {
            await deleteFileOrFolder(lastOp.data.destinationPath, lastOp.data.worktreeId);
            toast.success(`Copy undone: ${lastOp.data.fileName}`);
            store.removeLastCopiedFile();
            window.dispatchEvent(new CustomEvent("file-copy-undone", {
              detail: { path: lastOp.data.destinationPath }
            }));
          }
        } catch (error) {
          console.error('[FileOperationUndo] Failed to undo operation:', error);
          toast.error(
            `Failed to undo: ${error instanceof Error ? error.message : "Unknown error"}`
          );
        }
      })();
    };

    window.addEventListener("keydown", handleKeyDown, { capture: true, passive: false });
    document.addEventListener("keydown", handleKeyDown, { capture: true, passive: false });
    window.addEventListener("file-deletion-undo", handleUndoEvent);
    
    return () => {
      window.removeEventListener("keydown", handleKeyDown, { capture: true });
      document.removeEventListener("keydown", handleKeyDown, { capture: true });
      window.removeEventListener("file-deletion-undo", handleUndoEvent);
    };
  }, []);

  // Bridge pane-focus requests onto the events each pane already listens for.
  //
  // The panes predate the focus helper and each has its own established focus
  // path (xterm's hidden textarea, the composer's contenteditable, FileTree's
  // imperative handle). Translating here keeps that working rather than
  // rewriting five components to listen for a second event.
  useEffect(() => {
    const bridges: Array<[Pane, string]> = [
      ["chat-input", "focus-chat-input"],
      ["terminal", "focus-terminal"],
      ["right-sidebar", "focus-right-sidebar"],
      ["left-sidebar", "focus-left-sidebar"],
      ["transcript", "focus-transcript"],
    ];

    const unsubscribers = bridges.map(([pane, legacyEvent]) => {
      const relay = () => window.dispatchEvent(new CustomEvent(legacyEvent));
      const name = focusEventName(pane);
      window.addEventListener(name, relay);
      return () => window.removeEventListener(name, relay);
    });

    return () => unsubscribers.forEach((off) => off());
  }, []);

  // Handle the Electron IPC events that are not shortcuts (close all tabs,
  // tab navigation, search focus, theme). Menu items that ARE shortcuts arrive
  // by id at the keyboard host, on every route.
  useElectronIPC({ headerRef });

  // Note: Appearance settings (theme, fonts, color scheme) are applied in two phases:
  // 1. index.html applies from localStorage immediately (prevents flash)
  // 2. useThemeInitialization in App.tsx re-applies from database after settingsSync.initialize()

  useEffect(() => {
    let mounted = true;

    const initializeApp = async () => {
      if (!mounted) return;

      logger.info("🚀 Starting app initialization");

      try {
        // If we're in Electron, wait for the config
        if (typeof window !== "undefined" && window.electronAPI) {
          // Attach the postMessage listener BEFORE we read the current
          // config from preload. Otherwise, if preload fires its
          // `reliant-config-ready` message between our read and the
          // addEventListener call, we miss the event and hang on the
          // LoadingSpinner forever (until the user refreshes, which races
          // the opposite way: preload's reliantConfig is already cached so
          // the synchronous read returns it). The re-check inside the
          // listener-setup branch closes the window where preload posts
          // between read and listener attach.
          await new Promise<void>((resolve) => {
            let settled = false;
            const settleWith = (config: typeof window.RELIANT_CONFIG, source: string) => {
              if (settled) return;
              settled = true;
              window.RELIANT_CONFIG = config;
              window.removeEventListener("message", handleMessage);
              logger.info(`[App] Config ${source}:`, config?.daemonPort);
              resolve();
            };
            const handleMessage = (event: MessageEvent) => {
              if (event.source !== window) return;
              if (event.origin !== window.location.origin) return;
              if (
                event.data?.type === "reliant-config-ready" &&
                event.data?.config?.daemonPort
              ) {
                settleWith(event.data.config, "received via postMessage");
              }
            };
            window.addEventListener("message", handleMessage);

            // Synchronous re-check: if preload had the config ready before
            // we attached, the cached `getConfig()` returns it now.
            const current = window.electronAPI?.getConfig();
            if (current?.daemonPort) {
              settleWith(current, "available from electronAPI");
            }
          });
        }

        if (!mounted) return;

        // Projects are loaded in parallel by Root.tsx's Promise.all.
        // Safety net: if they haven't loaded yet (e.g. Root.tsx load failed), retry here.
        if (getCachedProjects().length === 0) {
          // Timeout prevents hanging when no daemon/backend is available (browser-only users)
          await Promise.race([
            fetchProjects(),
            new Promise((resolve) => setTimeout(resolve, 3000)),
          ]);
        }

        if (!mounted) return;

        // Backend is ready — workspace restore / selectProject already loaded chats.
        setIsBackendReady(true);
      } catch (err) {
        logger.error("Failed to initialize app:", err);
        // Still mark as ready so the UI renders — the app can handle
        // a missing daemon connection gracefully (daemon status dot,
        // chat gating, etc.)
        if (mounted) {
          setIsBackendReady(true);
        }
      }
    };

    initializeApp();

    return () => {
      mounted = false;
    };
  }, []); // Run once on mount — workspace restore handles project selection

  // Load the sidebar's chat list (every project, or the filter's one) once
  // the backend is ready: it seeds activity and the stream's resume point.
  // The list follows the filter, never the context project.
  const [chatsLoaded, setChatsLoaded] = useState(false);
  useEffect(() => {
    if (!isBackendReady) return;
    let cancelled = false;
    void useChatStore
      .getState()
      .loadChats()
      .finally(() => {
        if (!cancelled) setChatsLoaded(true);
      });
    return () => {
      cancelled = true;
    };
  }, [isBackendReady]);

  // Connect the live update stream once the chat list has loaded. It spans
  // every project, so switching projects never reconnects it.
  useUpdateStreamConnection(isBackendReady && chatsLoaded);
  const projectId = currentProject?.id;

  useEffect(() => {
    if (!isBackendReady || !projectId) return;

    // Initialize notification store (loads settings from localStorage/DB)
    useNotificationStore.getState().initialize();
    
    // Start permission refresh interval to detect system permission changes
    startPermissionRefresh();
  }, [isBackendReady, projectId]);

  // Listen for toggle file browser requests
  useEffect(() => {
    const handler = () => {
      setShowFileBrowser((prev) => !prev);
    };
    window.addEventListener("toggle-file-browser", handler as EventListener);
    return () =>
      window.removeEventListener(
        "toggle-file-browser",
        handler as EventListener
      );
  }, [setShowFileBrowser]);

  // Listen for open prompts settings requests from UI
  useEffect(() => {
    const handler = () => {
      // Navigate to the prompts settings section
      navigate({ to: '/settings/$section', params: { section: 'prompts' } });
    };
    window.addEventListener("open-settings-prompts", handler as EventListener);
    return () =>
      window.removeEventListener(
        "open-settings-prompts",
        handler as EventListener
      );
  }, [navigate]);

  // Listen for search modal open requests (from Electron menu)
  useEffect(() => {
    const handleQuickFileOpen = () => setShowQuickFileOpen(true);
    // Chat search is the keyboard host's overlay.
    const handleChatSearch = () => void shortcutHost?.run("onChatSearch");
    const handleFindInFiles = () => setShowGlobalSearch(true);
    
    window.addEventListener("open-quick-file-open", handleQuickFileOpen);
    window.addEventListener("open-chat-search", handleChatSearch);
    window.addEventListener("open-find-in-files", handleFindInFiles);
    
    return () => {
      window.removeEventListener("open-quick-file-open", handleQuickFileOpen);
      window.removeEventListener("open-chat-search", handleChatSearch);
      window.removeEventListener("open-find-in-files", handleFindInFiles);
    };
  }, [shortcutHost]);

  // Chats are already loaded by workspace restore / selectProject — no need to reload here.

  // Periodic error clearing to prevent persistent error states
  useEffect(() => {
    const errorClearInterval = setInterval(() => {
      // If we're connected and there's an error, try to clear it
      if (isBackendReady && useChatStore.getState().error) {
        logger.info(
          "🔄 Periodic error clearing - attempting to clear persistent error"
        );
        useChatStore.getState().clearError();
      }
    }, 30000); // Check every 30 seconds

    return () => clearInterval(errorClearInterval);
  }, [isBackendReady]);

  // Listen for project info from Electron
  // Note: In Electron, project restoration is handled by useWindowContext hook,
  // not by useWorkspaceRestore. This listener handles additional project info
  // that might be sent for restored windows.
  useEffect(() => {
    if (window.electronAPI && "onProjectInfo" in window.electronAPI) {
      const unsubscribe = window.electronAPI.onProjectInfo(
        (projectInfo: { id?: string; name?: string; path?: string }) => {
          logger.info("Received project info from Electron:", projectInfo);
          // Create a project object from the info
          const project = {
            id: projectInfo.path || "", // Use path as temporary ID
            name: projectInfo.name || "",
            path: projectInfo.path || "",
            is_git_repo: true,
            last_active: new Date().toISOString(),
            created_at: new Date().toISOString(),
            updated_at: new Date().toISOString(),
            is_forge: false, // Temporary, refreshed when projects load from the server
          };
          // Use skipClear and skipWorkspaceStateSave since this is an Electron-initiated project set
          // We don't want to save this to lastProjectId (that would affect other windows)
          selectProject(project, { skipClear: true, skipWorkspaceStateSave: true });
        }
      );

      if (typeof unsubscribe === "function") {
        return unsubscribe;
      }
    }
  }, [selectProject]);

  // Use workspace restore hook instead of clearing queue
  // This will restore the last project, worktree, chat, and viewers
  // In Electron, skip project restoration - useWindowContext handles it based on per-window context
  // This ensures each window restores to its own project, not the global lastProjectId from localStorage
  const isElectron = !!window.electronAPI;

  // Keep tray status in sync for menu-bar quick glance info
  useEffect(() => {
    if (!isElectron || !window.electronAPI?.setTrayStatus) {
      return;
    }

    const activeChats = chats.filter((chat) => chat.state !== ChatState.ARCHIVED);
    const activityMap = useActivityStore.getState().activities;
    const activeWorkflows = activeChats.filter(
      (chat) => {
        const activity = activityMap.get(chat.id);
        return isBusyActivity(activity);
      },
    ).length;
    const canCreateChat = Boolean(currentProject) && !isSettingsMode && !isWorkflowMode;

    const latestActivityMs = activeChats.reduce((latest, chat) => {
      const timestamp = Date.parse(chat.updatedAt || chat.createdAt || "");
      if (Number.isNaN(timestamp)) {
        return latest;
      }
      return Math.max(latest, timestamp);
    }, 0);

    const activeChat = activeChatId
      ? activeChats.find((chat) => chat.id === activeChatId) || null
      : null;

    const activityLabel = (chatId: string): string => {
      const activity = activityMap.get(chatId) ?? ChatActivity.IDLE;
      switch (activity) {
        case ChatActivity.RUNNING:
          return "running";
        case ChatActivity.AWAITING_INPUT:
          return "awaiting_input";
        case ChatActivity.PAUSED:
          return "paused";
        case ChatActivity.WAITING_FOR_USAGE_LIMIT:
          return "waiting_for_usage_limit";
        default:
          return "idle";
      }
    };

    const recentChats = [...activeChats]
      .sort((a, b) => {
        const aTime = Date.parse(a.updatedAt || a.createdAt || "");
        const bTime = Date.parse(b.updatedAt || b.createdAt || "");
        return (Number.isNaN(bTime) ? 0 : bTime) - (Number.isNaN(aTime) ? 0 : aTime);
      })
      .slice(0, 5)
      .map((chat) => ({
        id: chat.id,
        title: chat.title || "Untitled chat",
        activity: activityLabel(chat.id),
        needsRecovery: Boolean(chat.needsRecovery),
      }));

    const workspaceOptions = worktrees
      .filter((workspace) => !workspace.deleted_at)
      .slice(0, 10)
      .map((workspace) => ({
        id: workspace.id,
        name: workspace.name,
        isMain: Boolean(workspace.is_main),
      }));

    const workflowOptions = cachedWorkflows
      .filter((workflow) => typeof workflow.name === "string" && workflow.name.length > 0)
      .slice(0, 20)
      .map((workflow) => ({
        name: workflow.name,
        source: workflow.source,
      }));

    const agentStatus: "idle" | "running" | "error" = chatError
      ? "error"
      : activeWorkflows > 0
        ? "running"
        : "idle";

    const timeout = setTimeout(() => {
      window.electronAPI
        .setTrayStatus({
          agentStatus,
          activeWorkflows,
          hasChats: activeChats.length > 0,
          canCreateChat,
          currentProjectName: currentProject?.name || null,
          currentWorktreeName: currentWorktree?.name || null,
          currentWorktreeId: currentWorktree?.id || "__main__",
          activeChatId: activeChat?.id || null,
          activeChatTitle: activeChat?.title || null,
          recentChats,
          workspaces: workspaceOptions,
          workflows: workflowOptions,
          lastActivityAt:
            latestActivityMs > 0
              ? new Date(latestActivityMs).toISOString()
              : null,
        })
        .catch((error) => {
          logger.debug("[ModernApp] Failed to sync tray status", error);
        });
    }, 200);

    return () => clearTimeout(timeout);
  }, [
    isElectron,
    chats,
    activeChatId,
    chatError,
    currentProject,
    currentWorktree,
    worktrees,
    cachedWorkflows,
    isSettingsMode,
    isWorkflowMode,
  ]);

  const { isRestoring: isWorkspaceRestoring } = useWorkspaceRestore({
    autoRestore: isBackendReady,
    // In Electron, skip project restoration - let useWindowContext handle it.
    // Also skip if the URL deep-links to a specific project (`/project/$projectId`) —
    // ProjectFilterRouteSync owns project selection in that case so we
    // don't race lastProjectId vs the URL.
    skipProjectRestore: isElectron || Boolean(routeProjectId),
  });

  // With projects but none in context (the last one was removed in another
  // window, a restore that found nothing), open the most recent one: the
  // window never stops on a project picker. Electron windows resolve their
  // own context (useWindowContext), which falls back the same way.
  useEffect(() => {
    if (isElectron || !isBackendReady || isWorkspaceRestoring || currentProject) return;
    const next = mostRecentProject(projects);
    if (next) void useProjectStore.getState().selectProject(next);
  }, [isElectron, isBackendReady, isWorkspaceRestoring, currentProject, projects]);

  // Listen for Electron menu events from tray and app menus
  useEffect(() => {
    if (!window.electronAPI) return;

    const cleanups: Array<() => void> = [];

    // "New Tab" from the File menu, the tray and the dock arrives as the
    // newChat shortcut, through the keyboard host's menu bridge.

    const goToRecentChat = async (chatId?: string) => {
      // The tray lists chats of every project: openChat moves the context to
      // the chat's project and workspace.
      const alignWorktreeAndSelect = async (chat: Chat | undefined) => {
        if (!chat) return;
        try {
          await openChat(chat);
        } catch (error) {
          void toast.error(error);
        }
      };

      const target = chatId ? getChatFromCache(chatId) : undefined;
      if (target && target.state !== ChatState.ARCHIVED) {
        await alignWorktreeAndSelect(target);
        return;
      }

      const sorted = getCachedChatList(useWorkspaceStateStore.getState().projectFilterId)
        .filter((chat) => chat.state !== ChatState.ARCHIVED)
        .sort((a, b) => {
          const aTime = Date.parse(a.updatedAt || a.createdAt || "");
          const bTime = Date.parse(b.updatedAt || b.createdAt || "");
          return (Number.isNaN(bTime) ? 0 : bTime) - (Number.isNaN(aTime) ? 0 : aTime);
        });

      const mostRecent = sorted[0];
      if (mostRecent) {
        await alignWorktreeAndSelect(mostRecent);
      }
    };

    if ("onResumeLastChat" in window.electronAPI) {
      const unsubscribe = window.electronAPI.onResumeLastChat(() => goToRecentChat());
      if (typeof unsubscribe === "function") cleanups.push(unsubscribe);
    }

    if ("onTrayGoToChat" in window.electronAPI) {
      const unsubscribe = window.electronAPI.onTrayGoToChat((payload) => {
        goToRecentChat(payload?.chatId);
      });
      if (typeof unsubscribe === "function") cleanups.push(unsubscribe);
    }

    if ("onTrayGoToWorkflowHub" in window.electronAPI) {
      const unsubscribe = window.electronAPI.onTrayGoToWorkflowHub(() => {
        const projectId = useProjectStore.getState().currentProject?.id;
        if (!projectId) return;
        navigate({ to: '/workflows' });
      });
      if (typeof unsubscribe === "function") cleanups.push(unsubscribe);
    }

    if ("onTrayOpenWorkflow" in window.electronAPI) {
      const unsubscribe = window.electronAPI.onTrayOpenWorkflow((payload) => {
        const projectId = useProjectStore.getState().currentProject?.id;
        if (!projectId) return;

        const workflowName = payload?.workflowName;
        if (!workflowName) return;

        navigate({
          to: '/workflow/$workflowName',
          params: { workflowName },
        });
      });
      if (typeof unsubscribe === "function") cleanups.push(unsubscribe);
    }

    if ("onTrayGoToSettings" in window.electronAPI) {
      const unsubscribe = window.electronAPI.onTrayGoToSettings(() => {
        navigate({ to: '/settings' });
      });
      if (typeof unsubscribe === "function") cleanups.push(unsubscribe);
    }

    if ("onTrayAddProject" in window.electronAPI) {
      const unsubscribe = window.electronAPI.onTrayAddProject(() => {
        openAddProject();
      });
      if (typeof unsubscribe === "function") cleanups.push(unsubscribe);
    }

    if ("onTraySwitchWorkspace" in window.electronAPI) {
      const unsubscribe = window.electronAPI.onTraySwitchWorkspace(async (payload) => {
        const project = useProjectStore.getState().currentProject;
        if (!project) return;

        const targetId = payload?.workspaceId;
        const worktreeStore = useWorktreeStore.getState();

        if (!targetId || targetId === "__main__") {
          await worktreeStore.switchWorktreeContext(project.id, null);
          return;
        }

        const target = await resolveWorktree(targetId);
        if (!target) return;
        await worktreeStore.switchWorktreeContext(project.id, target);
      });
      if (typeof unsubscribe === "function") cleanups.push(unsubscribe);
    }

    return () => {
      cleanups.forEach((cleanup) => cleanup());
    };
  }, [navigate]);

  // Get onboarding state - we defer git init check until onboarding tour is completed
  const hasCompletedOnboarding = useTourStore((state) => state.hasCompletedOnboarding);
  const tourInitialized = useTourStore((state) => state.isInitialized);
  const { activeDaemon, loading: daemonStatusLoading } = useDaemonStatus();
  // The git-init effect only needs to know *whether* a daemon is connected.
  // Depending on the activeDaemon object itself re-runs the effect on every
  // 5s poll, because the daemon carries live memory/port telemetry that
  // changes each cycle and defeats React Query's structural sharing.
  const hasActiveDaemon = Boolean(activeDaemon);

  // Check git initialization when a project is selected (use stable ID to avoid re-fires)
  // Skip during onboarding welcome to avoid dual modal confusion
  useEffect(() => {
    if (projectId && isBackendReady) {
      // Wait for checklist state to be loaded from database
      if (!tourInitialized) {
        logger.info("[ModernApp] Waiting for tour state to initialize before git check");
        return;
      }

      // Skip git init check if onboarding tour hasn't been completed yet
      if (!hasCompletedOnboarding) {
        logger.info("[ModernApp] Deferring git init check until onboarding tour completes");
        return;
      }

      // Don't prompt to initialize git if there's no daemon connected — the
      // operation runs on the daemon, so it would fail. Also covers the
      // brief window during daemon status fetch where we don't yet know.
      if (daemonStatusLoading || !hasActiveDaemon) {
        logger.info("[ModernApp] Deferring git init check while no daemon is connected");
        return;
      }

      // If any modal is currently open via the unified modal store, defer the
      // git-init modal so we never stack two modals on top of each other.
      if (useModalStore.getState().activeModal !== null) {
        logger.info("[ModernApp] Deferring git init check while another modal is open");
        return;
      }

      // The git-init modal is not registered with the modal store, so the
      // guard above cannot see it. Check it explicitly — otherwise the effect
      // re-runs while the dialog is already open and refreshes the project out
      // from under it.
      if (showGitInitModalRef.current) {
        logger.info("[ModernApp] Git init modal already open, skipping check");
        return;
      }

      // Refresh project to get fresh is_git_repo status from backend before checking
      const checkWithFreshData = async () => {
        try {
          await useProjectStore.getState().refreshCurrentProject();
          const refreshedProject = useProjectStore.getState().currentProject;
          if (!refreshedProject) return;
          checkGitInitialization(refreshedProject);
          if (refreshedProject.is_git_repo) {
            checkForRescan(refreshedProject.id, refreshedProject.path);
          }
        } catch (err) {
          const project = useProjectStore.getState().currentProject;
          if (!project) return;
          logger.warn("[ModernApp] Failed to refresh project before git check", err);
          checkGitInitialization(project);
          if (project.is_git_repo) {
            checkForRescan(project.id, project.path);
          }
        }
      };
      
      checkWithFreshData();
    }
  }, [
    projectId,
    checkGitInitialization,
    checkForRescan,
    isBackendReady,
    tourInitialized,
    hasCompletedOnboarding,
    hasActiveDaemon,
    daemonStatusLoading,
  ]);

  // Note: Sidebar timeout cleanup is now handled by useSidebarOverlay hook

  // Stable empty callbacks for ChatInterface
  const noopNavigateWorktrees = useCallback(() => {}, []);

  const renderMainContent = () => {
    return (
      // NOTE: min-h-0 on all flex children is critical for proper scrolling behavior.
      // Without it, flex items won't shrink below their content height, breaking
      // overflow-y-auto in nested components like ChatMessagesContainer.
      <div className="flex flex-col h-full min-h-0">
        <div className="flex-1 overflow-hidden min-h-0">
          {/* NOTE: Using overflow-hidden instead of overflow-auto to allow sticky positioning to work in ChatMessagesContainer */}
          <div className="h-full overflow-hidden min-h-0">
            {activeChatId ? (
              <ChatInterface
                tabId={activeChatId}
                onNavigateToWorktrees={noopNavigateWorktrees}
              />
            ) : (
              <HomeNewChat onNavigateToWorktrees={noopNavigateWorktrees} />
            )}
          </div>
        </div>
      </div>
    );
  };

  // Whether the user is mid-onboarding. The onboarding UI itself lives at
  // /onboarding now (see OnboardingRoute); this flag exists only so we can
  // bounce stray landings on / or /project/$id over there.
  const inOnboarding =
    !isUserLoading && (!currentUser || !currentUser.onboardingCompleted);

  // If we land here while the user still needs to onboard, redirect to the
  // dedicated route. Preserve any incoming search (e.g. legacy /?plan=... or
  // /?github_connected=true) — onboardingSearchSchema accepts the same
  // fields, and OnboardingRoute will pick them up.
  //
  // We deliberately do NOT gate on `isBackendReady` or `isWorkspaceRestoring`:
  // a mid-onboarding user has no projects to list and no workspace to restore,
  // and waiting for ListProjects to complete (which can run several hundred
  // ms to a couple seconds before the shared fetch inside fetchProjects
  // resolves) just delays the redirect — leaving the user staring at a
  // LoadingSpinner before the OnboardingPage / ComputeStep mounts and fires
  // its own queries (ListDaemons, etc.). Gate only on `isUserLoading` so we
  // don't bounce twice (once before `currentUser` arrives, once after).
  useEffect(() => {
    if (isUserLoading) return;
    if (!inOnboarding) return;
    navigate({
      to: '/onboarding',
      search: (prev: Record<string, unknown>) => prev,
      replace: true,
    });
  }, [isUserLoading, inOnboarding, navigate]);

  // GitHub OAuth return for app-shell triggers (GitHubSyncStatus, AddRepoModal).
  // /onboarding and /settings/git-connections handle their own returns in-route.
  // Here we just toast, refresh the credential cache, and strip the params so
  // a reload doesn't replay the toast.
  useEffect(() => {
    if (!isBackendReady) return;
    if (isUserLoading || !currentUser) return;
    if (!github_connected && !github_installed && !github_error) return;

    if (github_connected) {
      toast.success("GitHub connected successfully");
      void queryClient.invalidateQueries({ queryKey: GITHUB_CREDENTIAL_QUERY_KEY });
    } else if (github_installed) {
      // Returned from GitHub's App install/setup flow. No credential was
      // created, so the wording is about access rather than connection — but
      // the refresh is the same one, because which repositories we can see has
      // just changed server-side.
      toast.success("GitHub access updated");
      void queryClient.invalidateQueries({ queryKey: GITHUB_CREDENTIAL_QUERY_KEY });
      // The repo list is the whole reason the user went to GitHub — a newly
      // granted repository only appears if we re-list. The credential query
      // alone would leave the picker showing the pre-install set.
      void queryClient.invalidateQueries({ queryKey: ['onboarding', 'gitRepos'] });
    } else if (github_error) {
      toast.error(github_error_msg || github_error || "GitHub connection failed");
    }

    navigate({
      to: '/',
      search: (prev: Record<string, unknown>) => {
        const { github_connected: _c, github_installed: _i, github_error: _e, github_error_msg: _m, ...rest } = prev;
        return rest;
      },
      replace: true,
    });
  }, [isBackendReady, isUserLoading, currentUser, github_connected, github_installed, github_error, github_error_msg, navigate, queryClient]);

  // ─── Branch dispatch ────────────────────────────────────────────────────
  // Compute the per-branch UI as a value rather than returning early. The
  // app-global overlays (ModalLayer, Toaster, ContextualTipsLayer,
  // GitHubSyncStatus, OnboardingWizard) now live on the root route in
  // routes.tsx — they render on every route, including /settings,
  // /workflow/*, and unauthenticated routes.
  const renderBranches = () => {
    // Show loading spinner until backend is ready
    if (!isBackendReady) {
      return <LoadingSpinner />;
    }

    // Show loading spinner while workspace is restoring or user is loading
    if (isWorkspaceRestoring || isUserLoading) {
      return <LoadingSpinner />;
    }

    // Mid-onboarding users get redirected to /onboarding by the effect above;
    // show a spinner instead of the picker for the one render between
    // currentUser arriving and the navigate landing.
    if (inOnboarding) {
      return <LoadingSpinner />;
    }

    // There is no project picker. With projects, the window always works in
    // one (the effect above selects the most recent while none is); only an
    // account with no projects yet sees the first-run state. Header is
    // hoisted to the App-level return — this branch returns only the content
    // rendered below it.
    if (!currentProject) {
      if (!projectsLoading && projects.length === 0) {
        return (
          <div className="flex-1 min-h-0 overflow-hidden">
            <NoProjectsState />
          </div>
        );
      }
      return <LoadingSpinner />;
    }

    // Workflow routes are handled by /workflow/* in routes.tsx — they render
    // WorkflowPage directly, not App, so we never reach here while on
    // /workflow.

    return renderMainAppShell();
  };

  const renderMainAppShell = () => (
    // Header is hoisted to the App-level return; this wrapper renders only the
    // chat-shell content below it.
    <div className="flex flex-col flex-1 min-h-0 bg-background font-sans dense-ui">
      <div className="flex flex-1 min-h-0 relative">
        {/* Hover trigger area - shows when sidebar is closed */}
        {!showChatSidebar && (
          <div
            className="absolute left-0 top-0 bottom-0 w-2 z-40 hover:bg-primary/10 transition-colors"
            onMouseEnter={handleSidebarMouseEnter}
            onMouseLeave={handleSidebarMouseLeave}
          />
        )}

        {/* Chat Sidebar - Toggle with Shift+Cmd+B */}
        {showChatSidebar && (
          <ResizableSidebar storageKey="chat-sidebar-width">
            <div id="layout-left-sidebar" className="flex flex-col h-full">
              <Sidebar
                paddingClass=""
                onOpenWorkflows={() => {
                  navigate({ to: '/workflows' });
                }}
                onOpenInbox={() => {
                  navigate({ to: '/inbox' });
                }}
                onOpenRuns={(search) => {
                  navigate({ to: '/workflows/runs', search: search ?? {} });
                }}
                onOpenChatSearch={() => void shortcutHost?.run("onChatSearch")}
                onNavigateToSettings={() => {
                  navigate({ to: '/settings' });
                }}
                onManageProjects={() => {
                  navigate({ to: '/settings/$section', params: { section: 'projects' } });
                }}
                onOpenForge={() => {
                  navigate({ to: '/apps' });
                }}
              />
            </div>
          </ResizableSidebar>
        )}

        {/* Overlay sidebar - shows on hover when closed */}
        {!showChatSidebar && showOverlaySidebar && (
          <div
            className={cn(
              "absolute left-0 top-0 bottom-0 z-50 shadow-2xl transition-all duration-300 ease-out",
              isSidebarHovered ? "translate-x-0 opacity-100" : "-translate-x-full opacity-0"
            )}
            onMouseEnter={handleOverlayMouseEnter}
            onMouseLeave={handleOverlayMouseLeave}
          >
            <ResizableSidebar storageKey="chat-sidebar-width">
              <div className="flex flex-col h-full">
                <Sidebar
                  paddingClass=""
                  onOpenWorkflows={() => {
                    navigate({ to: '/workflows' });
                  }}
                  onOpenInbox={() => {
                    navigate({ to: '/inbox' });
                  }}
                  onOpenRuns={(search) => {
                    navigate({ to: '/workflows/runs', search: search ?? {} });
                  }}
                  onOpenChatSearch={() => void shortcutHost?.run("onChatSearch")}
                  onNavigateToSettings={() => {
                    navigate({ to: '/settings' });
                  }}
                  onManageProjects={() => {
                    navigate({ to: '/settings/$section', params: { section: 'projects' } });
                  }}
                  onOpenForge={() => {
                    navigate({ to: '/apps' });
                  }}
                />
              </div>
            </ResizableSidebar>
          </div>
        )}

        {/* Main Content Area */}
        <div className="flex-1 flex flex-col min-w-0">
          <main className="flex-1 flex overflow-hidden relative">
            {/* Main Chat Content Area */}
            <div
              id="layout-main-content"
              className="flex-1 flex flex-col overflow-hidden transition-all duration-300 ease-in-out"
            >
              {renderMainContent()}
            </div>

            {/* Right area: Viewer Panel + Terminal */}
            {(hasOpenViewers || currentProject) && (
              <div id="layout-right-panel" className="flex flex-col h-full">
                {hasOpenViewers && (
                  <TabbedViewerPanel
                    hasTerminal={isTerminalOpen && !!currentProject}
                  />
                )}

                {/* Keep terminal mounted but hidden to preserve running processes */}
                {currentProject && (
                  <div
                    style={{
                      display: isTerminalOpen ? "flex" : "none",
                      flex: 1,
                      minHeight: 0,
                    }}
                  >
                    <TerminalPanel
                      key={`terminal-panel-${currentProject.id}`}
                      hasViewer={hasOpenViewers}
                    />
                  </div>
                )}
              </div>
            )}

            {/* File Browser Sidebar */}
            {showFileBrowser && (
              <ResizableSidebar
                defaultWidth={200}
                minWidth={200}
                maxWidth={600}
                side="right"
                storageKey="file-browser-width"
              >
                <div id="layout-right-sidebar" className="flex flex-col h-full w-full">
                  <RightSidebar onCloseSidebar={() => setShowFileBrowser(false)} />
                </div>
              </ResizableSidebar>
            )}

          </main>
        </div>
      </div>

      {/* Git Initialization Modal */}
      {showGitInitModal && gitInitProjectInfo && currentProject && (
        <InitializeGitModal
          isOpen={showGitInitModal}
          onClose={handleDismissGitInit}
          onSuccess={handleGitInitSuccess}
          projectId={gitInitProjectInfo.id}
          projectName={gitInitProjectInfo.name}
        />
      )}

      {/* Project Rescan Modal */}
      {showRescanModal && currentProject && (
        <RescanModal
          isOpen={showRescanModal}
          projectName={currentProject.name}
          commitCount={commitCount}
          onConfirm={handleRescan}
          onCancel={handleDismissRescan}
          onDismissForever={handleDismissForever}
        />
      )}

      {/* Search Modals */}
      <GlobalSearch
        isOpen={showGlobalSearch}
        onClose={() => setShowGlobalSearch(false)}
      />
      <FindReplace
        isOpen={showFindReplace}
        onClose={() => setShowFindReplace(false)}
      />
      <QuickFileOpen
        isOpen={showQuickFileOpen}
        onClose={() => setShowQuickFileOpen(false)}
      />

      {/* Global Update Handler - shows update modal when updates are available */}
      <GlobalUpdateHandler />
    </div>
  );

  // ─── Final return ────────────────────────────────────────────────────────
  // App-global overlays (ModalLayer, Toaster, ContextualTipsLayer,
  // GitHubSyncStatus, OnboardingWizard) are mounted on the root route — see
  // routes.tsx. The wizard gates its own visibility internally and is fully
  // URL-decoupled, so it is safe to mount globally.

  // Header is meaningful only after backend is ready and the workspace/user
  // state has settled. Mid-onboarding users get redirected to /onboarding —
  // we don't render the app-shell header during that bounce. Mirrors the
  // gates inside renderBranches() that previously suppressed the header.
  const showHeader =
    isBackendReady &&
    !isWorkspaceRestoring &&
    !isUserLoading &&
    !inOnboarding;

  // Contribute the workspace's shortcut handlers to the keyboard host — from
  // the moment the workspace can act on them (the same gate as the header:
  // backend up, workspace restored). Registering earlier would let a shortcut
  // run against a chat or panel the restore is about to replace; until then
  // the host queues workspace shortcuts and runs them here once this lands,
  // which is also how a shortcut pressed on /settings arrives.
  useShortcutLayer(
    {
      handlers: keyboardHandlers,
      workspace: true,
      closeOverlays: closeLocalModals,
    },
    showHeader,
  );

  return (
    <div className="flex flex-col h-screen">
      <ProjectFilterRouteSync />
      {showHeader && (
        <Header
          ref={headerRef}
          logoSize="xl"
          windowAligned={true}
          onNavigateToSettings={() => {
            navigate({ to: '/settings' });
          }}
          onNavigateToSettingsSection={(section) => {
            navigate({ to: '/settings/$section', params: { section } });
          }}
          onToggleTerminal={toggleTerminal}
          onToggleFileBrowser={() => setShowFileBrowser((prev) => !prev)}
          onToggleChatSidebar={() => setShowChatSidebar((prev) => !prev)}
          onOpenWorkflows={() => {
            navigate({ to: '/workflows' });
          }}
        />
      )}
      {renderBranches()}
    </div>
  );
}

export default App;