// ModernApp - Main application component
import { ChatState } from "./gen/reliant/v1/chat_pb";
import type { Chat } from "./api/client";
import { logger } from "./lib/logger";
import { cn } from "./lib/utils";
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
import { useProjectStore } from "./store/projectStore";
import { useFileDeletionStore } from "./store/fileDeletionStore";
import { createFile, createFolder, moveFile, deleteFileOrFolder } from "./api/fileSystem";
import { toast } from "./lib/toast-manager";
import { ResizableSidebar } from "./components/Layout/ResizableSidebar";
import { RightSidebar } from "./components/FileBrowser/RightSidebar";
import { GlobalSearch, FindReplace } from "./components/FileBrowser";
import { QuickFileOpen } from "./components/Layout/QuickFileOpen";
import { CommandPalette } from "./components/Layout/CommandPalette";
import { ChatSearch } from "./components/Layout/ChatSearch";
import { NewChatView } from "./components/Chat/NewChatView";
import { WorkflowBuilderPage } from "./components/workflow/WorkflowBuilderPage";

// Wrapper to handle key-based remount for hub view reset
function WorkflowBuilderPageWithKey() {
  const workflowViewKey = useViewerStore((s) => s.workflowViewKey);
  return <WorkflowBuilderPage key={`wf-${workflowViewKey}`} />;
}
import { WorkflowHeader } from "./components/workflow/WorkflowHeader";
import { SettingsPage } from "./components/Settings/SettingsPage";
import { SettingsHeader } from "./components/Settings/SettingsHeader";
import { ContextualTipsLayer, OnboardingWizard } from "./components/Onboarding";

import { Header, type HeaderRef } from "./components/Layout/Header";
import { ProjectPicker } from "./components/Projects/ProjectPicker";
import { InitializeGitModal } from "./components/Git/InitializeGitModal";
import { RescanModal } from "./components/Projects/RescanModal";
import { ApiKeySetupModal } from "./components/ApiKeySetupModal";

import { TerminalPanel } from "./components/Terminal/TerminalPanel";
import { Toaster } from "./lib/toast";
import { LoadingSpinner } from "./components/Layout/LoadingSpinner";
import { GlobalUpdateHandler } from "./components/Settings/GlobalUpdateHandler";
import { useChatStore } from "./store/chatStore";
import { useActivityStore, ChatActivity } from "./store/activityStore";
import { useChatNavigationStore } from "./store/chatNavigationStore";
import { useWindowContext } from "./hooks/useWindowContext";
import { useOpenProjectListener } from "./hooks/useOpenProjectListener";
import {
  useKeyboardShortcuts,
  useAppKeyboardShortcuts,
} from "./hooks/useKeyboardShortcuts";
import { useElectronIPC } from "./hooks/useElectronIPC";
import { useSidebarOverlay } from "./hooks/useSidebarOverlay";
import { useGitInitialization } from "./hooks/useGitInitialization";
import { useProjectRescan } from "./hooks/useProjectRescan";
import { useCancelOnUnload } from "./hooks/useCancelOnUnload";
import { useShortcutsStore } from "./store/shortcutsStore";
import { useTerminalStore } from "./store/terminalStore";
import { useWorktreeStore } from "./store/worktreeStore";
import { useGlobalUpdatesStore } from "./store/globalUpdatesStore";
import { useProcessStore } from "./store/processStore";

import { isGrpcReady } from "./api/grpc-client";
import { useNotificationStore, startPermissionRefresh } from "./store/notificationStore";
import { useWorkspaceStateStore } from "./store/workspaceStateStore";
import { useWorkspaceRestore, useAutoSaveWorkspaceState } from "./hooks/useWorkspaceRestore";
import { focusChatInput } from "./hooks/useFocusManager";
import { useOnboardingChecklistStore } from "./store/onboardingChecklistStore";
import { useApiKeySetupStore } from "./store/apiKeySetupStore";
import { useGlobalDataStore } from "./store/globalDataStore";

function App() {
  const loadChats = useChatStore((state) => state.loadChats);
  const chats = useChatStore((state) => state.chats); // Map<string, Chat>
  const chatError = useChatStore((state) => state.error);
  const activeChatId = useChatStore((state) => state.activeChatId);
  const pendingNewChatWorktreeId = useChatStore((state) => state.pendingNewChatWorktreeId);
  const currentProject = useProjectStore((state) => state.currentProject);
  const selectProject = useProjectStore((state) => state.selectProject);
  const loadProjects = useProjectStore((state) => state.loadProjects);
  const initializeShortcuts = useShortcutsStore(
    (state) => state.initializeShortcuts
  );
  const isTerminalOpen = useTerminalStore((state) => state.isOpen);
  const toggleTerminal = useTerminalStore((state) => state.toggleTerminal);
  const createTerminalSession = useTerminalStore(
    (state) => state.createSession
  );
  const openTerminal = useTerminalStore((state) => state.openTerminal);
  const worktrees = useWorktreeStore((state) => state.worktrees);
  const currentWorktree = useWorktreeStore((state) => state.currentWorktree);

  // Backend ready gate is defined later in this file; we must not reference it
  // before initialization.
  const [isBackendReady, setIsBackendReady] = useState(false);

  // Ensure global, project-scoped data (workflows/presets) is loaded whenever a project
  const cachedWorkflows = useGlobalDataStore((s) => s.workflows);

  // Update browser tab title with current project name
  useEffect(() => {
    document.title = currentProject?.name
      ? `Reliant - ${currentProject.name}`
      : "Reliant";
  }, [currentProject?.name]);

  // NOTE: Workflows and presets are fetched by projectStore.setCurrentProject().
  // With singleflight at the gRPC layer, duplicate concurrent calls are deduplicated
  // automatically, so no secondary trigger is needed here.

  // UI state - use persisted values as defaults, initialized lazily from workspace state
  // NOTE: We use lazy initialization with getState() instead of subscribing to the whole
  // worktree state object, which would cause re-renders on every state change.
  const [showFileBrowser, setShowFileBrowserLocal] = useState(() => {
    const projectId = useProjectStore.getState().currentProject?.id;
    const worktreeId = useWorktreeStore.getState().currentWorktree?.id ?? null;
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
      const worktreeId = useWorktreeStore.getState().currentWorktree?.id ?? null;
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
  const viewerProjectId = useViewerStore((state) => state.currentProjectId);
  const activeViewerId = useViewerStore((state) => state.activeViewerId);
  const setActiveViewer = useViewerStore((state) => state.setActiveViewer);
  const isWorkflowMode = useViewerStore((state) => state.isWorkflowMode);
  const setWorkflowMode = useViewerStore((state) => state.setWorkflowMode);
  const isSettingsMode = useViewerStore((state) => state.isSettingsMode);
  const settingsSection = useViewerStore((state) => state.settingsSection);
  const setSettingsMode = useViewerStore((state) => state.setSettingsMode);
  // Calculate hasOpenViewers based on subscribed state
  const hasOpenViewers = viewerProjectId
    ? viewers.filter((v) => v.projectId === viewerProjectId).length > 0
    : viewers.length > 0;

  const headerRef = useRef<HeaderRef>(null);

  // Note: Removed file polling - now handled by viewer store and individual viewer components

  // Track if this is the initial project load (vs manual project switch)
  // Used to prevent clearing chat during workspace restoration
  const hasInitialProjectLoadedRef = useRef(false);
  
  // Handle project switching: clear diff panel and chat
  useEffect(() => {
    const viewerStore = useViewerStore.getState();
    const chatStore = useChatStore.getState();

    if (currentProject?.id) {
      // Set current project in viewer store (this will clear viewers from other projects)
      viewerStore.setCurrentProject(currentProject.id);

      // Only clear current chat when switching projects manually (not during initial load)
      // During initial load, the workspace restore hook will set the correct chat
      if (hasInitialProjectLoadedRef.current) {
        chatStore.clearCurrentChat();
      } else {
        // Mark that initial project has loaded - subsequent changes will clear chat
        hasInitialProjectLoadedRef.current = true;
      }
    } else {
      // No project selected - clear viewer store project
      viewerStore.setCurrentProject(null);
    }
  }, [currentProject?.id]);


  // Restore panel state when worktree changes (file browser is per-worktree, chat sidebar is global)
  useEffect(() => {
    if (!currentProject?.id) return;
    
    const worktreeId = currentWorktree?.id ?? null;
    const state = useWorkspaceStateStore.getState().getWorktreeState(currentProject.id, worktreeId);
    
    // Restore file browser visibility (per-worktree state)
    // Note: Chat sidebar is now global and handled by the store subscription
    setShowFileBrowserLocal(state.rightPanelState.fileBrowser);
    
    logger.debug("[ModernApp] Restored panel state for worktree", {
      worktreeId,
      fileBrowser: state.rightPanelState.fileBrowser,
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

  // Initialize shortcuts store
  useEffect(() => {
    initializeShortcuts();
  }, [initializeShortcuts]);

  // Fire best-effort cancel when closing the browser tab/window
  useCancelOnUnload();

  // Git initialization logic
  const {
    showGitInitModal,
    gitInitProjectInfo,
    checkGitInitialization,
    handleCloseGitInitModal,
  } = useGitInitialization();



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
  const [showCommandPalette, setShowCommandPalette] = useState(false);
  const [showChatSearch, setShowChatSearch] = useState(false);

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
      const { loadWorktrees, restoreLastWorktree } = useWorktreeStore.getState();
      await loadWorktrees(projectId);
      restoreLastWorktree(projectId);
      logger.info('[ModernApp] Reloaded worktrees after git initialization');
    }
    handleCloseGitInitModal();
  }, [handleCloseGitInitModal]);

  // Handler for navigating to project picker
  const handleNavigateToProjectPicker = useCallback(() => {
    useChatStore.getState().clearCurrentChat(null);
    // Clear current project selection to show project picker
    useProjectStore.setState({ currentProject: null });
  }, []);

  // Get current worktreeId from active chat
  const getCurrentWorktreeId = useCallback(() => {
    if (activeChatId) {
      const chat = chats.get(activeChatId);
      return chat?.worktreeId || undefined;
    }
    return undefined;
  }, [activeChatId, chats]);

  // Get terminal working directory based on current context
  const getTerminalWorkingDir = useCallback(() => {
    // Check if there's an active chat with a worktree
    const worktreeId = getCurrentWorktreeId();
    if (worktreeId) {
      // Find the worktree and use its path
      const worktree = worktrees.find((w) => w.id === worktreeId);
      if (worktree?.path) {
        logger.info("[Terminal] Using worktree path:", worktree.path);
        return worktree.path;
      }
    }

    // Fallback to project path
    if (currentProject?.path) {
      logger.info("[Terminal] Using project path:", currentProject.path);
      return currentProject.path;
    }

    return undefined;
  }, [getCurrentWorktreeId, worktrees, currentProject?.path]);

  // Use keyboard shortcuts hook for production security
  useKeyboardShortcuts({
    disableDevTools: true,
  });

  // Helper to close all modals
  const closeAllModals = useCallback(() => {
    setShowCommandPalette(false);
    setShowGlobalSearch(false);
    setShowQuickFileOpen(false);
    setShowFindReplace(false);
    setShowChatSearch(false);
  }, []);

  // Memoize keyboard shortcut handlers to prevent recreation on every render
  const keyboardHandlers = useMemo(
    () => ({
      onNewChat: () => {
        // Clear active chat to show new chat view
        // Preserve current worktree context from active chat or current worktree store
        const chatStore = useChatStore.getState();
        const worktreeStore = useWorktreeStore.getState();
        const activeChatId = chatStore.activeChatId;
        const activeChat = activeChatId ? chatStore.chats.get(activeChatId) ?? null : null;
        const currentWorktreeId = activeChat?.worktreeId || worktreeStore.currentWorktree?.id || null;
        chatStore.clearCurrentChat(currentWorktreeId);
      },
      onOpenProject: () => {
        handleNavigateToProjectPicker();
      },
      onToggleSettings: () => {
        // Toggle settings full-screen mode
        const viewerState = useViewerStore.getState();
        viewerState.setSettingsMode(!viewerState.isSettingsMode);
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
      onCommandPalette: () => {
        closeAllModals();
        setShowCommandPalette(true);
      },
      onChatSearch: () => {
        closeAllModals();
        setShowChatSearch(true);
      },
      onStopStreaming: () => {
        // Priority 1: Close Settings mode if open
        const viewerState = useViewerStore.getState();
        if (viewerState.isSettingsMode) {
          logger.info("🛑 ESC pressed - closing Settings");
          viewerState.setSettingsMode(false);
          // Focus chat input after closing settings
          focusChatInput();
          return;
        }

        // Priority 2: If in Workflow mode, let WorkflowBuilderPage handle ESC
        // This allows the navigation stack: panels → hub → exit
        // The workflow components register their own handlers on window (capture phase)
        // which run before this handler and call stopImmediatePropagation()
        // If we reach here, it means the user is in an input field or modal
        // within the workflow, so we should NOT close workflow mode.
        if (viewerState.isWorkflowMode) {
          // Don't close workflow mode - let the workflow handle its own navigation
          return;
        }

        // Priority 3: Close any open search modals
        // Note: Search modals handle their own ESC key, but if they're open
        // we should not stop streaming. Check all modal states.
        if (showGlobalSearch || showFindReplace || showQuickFileOpen || showCommandPalette || showChatSearch) {
          // Modals handle their own close via ESC
          return;
        }

        // Priority 4: Find the active chat and pause it
        if (activeChatId) {
          const { stopStreaming, pauseChat } =
            useChatStore.getState();
          const activity = useActivityStore.getState().activities.get(activeChatId);
          const isBusy = activity !== undefined && activity >= ChatActivity.RUNNING;

          // Only stop if chat is actually busy
          if (isBusy) {
            logger.info(
              "🛑 ESC pressed - pausing chat for:",
              activeChatId
            );

            // Stop streaming immediately for UI
            stopStreaming(activeChatId);

            // Pause the backend workflow (keeps it resumable)
            pauseChat(activeChatId).catch((error) => {
              logger.error("Failed to pause chat via ESC:", error);
            });
          }
        }
      },
      onApproveToolRequests: () => {
        // Find the active chat and approve all pending tool requests
        if (activeChatId) {
          const { approveAllPending, pendingApprovals } =
            useChatStore.getState();
          const pending = pendingApprovals[activeChatId] || [];

          // Only approve if there are pending requests
          if (pending.length > 0) {
            logger.info(
              "✅ Cmd+Enter pressed - approving all tool requests for:",
              activeChatId,
              "Count:",
              pending.length
            );
            approveAllPending(activeChatId);
          }
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
          // Terminal is closed, open it (focus handled by TerminalPanel effect)
          toggleTerminal();
        }
      },
      onNewTerminal: () => {
        // Open terminal panel if not open
        if (!isTerminalOpen) {
          openTerminal();
        }
        // Create new terminal session with context-aware working directory, project ID, and worktree ID
        const workingDir = getTerminalWorkingDir();
        const projectId = currentProject?.id;
        const worktreeId = getCurrentWorktreeId();
        createTerminalSession(workingDir, projectId, worktreeId);
      },
      onFocusChat: () => {
        // Clear active viewer
        const { setActiveViewer } = useViewerStore.getState();
        setActiveViewer(null);
        // Focus the textarea - it will focus via the custom event
        window.dispatchEvent(new CustomEvent("focus-chat-input"));
      },
      onFocusFileEditor: () => {
        // Focus the active file editor
        const { activeViewerId } = useViewerStore.getState();
        if (activeViewerId) {
          window.dispatchEvent(new CustomEvent('file-viewer-focus', { 
            detail: { viewerId: activeViewerId } 
          }));
        }
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
        setShowFileBrowser((prev) => !prev);
      },
      onToggleSidebar: () => {
        setShowChatSidebar((prev) => !prev);
      },
      onOpenWorkflows: () => {
        useViewerStore.getState().setWorkflowMode(true);
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
    }),

    [
      closeAllModals,
      handleNavigateToProjectPicker,
      activeChatId,
      toggleTerminal,
      isTerminalOpen,
      openTerminal,
      createTerminalSession,
      getTerminalWorkingDir,
      getCurrentWorktreeId,
      currentProject?.id,
      currentWorktree?.id,
      showGlobalSearch,
      showFindReplace,
      showQuickFileOpen,
      showCommandPalette,
      showChatSearch,
      setShowFileBrowser,
      setShowChatSidebar,
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

  // Use app keyboard shortcuts with memoized handlers
  useAppKeyboardShortcuts(keyboardHandlers);

  // Handle all Electron IPC events (menu commands, deep links, etc.)
  useElectronIPC({
    headerRef,
    isTerminalOpen,
    toggleTerminal,
    openTerminal,
    createTerminalSession,
    getTerminalWorkingDir,
    getCurrentWorktreeId,
    currentProjectId: currentProject?.id,
  });

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
          // Get config from electronAPI (exposed by preload via contextBridge)
          const config = window.electronAPI.getConfig();

          if (config?.backendPort) {
            // Config already available from preload
            window.RELIANT_CONFIG = config;
            logger.info(
              "[App] Config available from electronAPI:",
              config.backendPort
            );
          } else {
            // Wait for postMessage from preload when config becomes ready
            await new Promise<void>((resolve) => {
              const handleMessage = (event: MessageEvent) => {
                if (
                  event.data?.type === "reliant-config-ready" &&
                  event.data?.config
                ) {
                  const config = event.data.config;
                  if (config.backendPort) {
                    window.RELIANT_CONFIG = config;
                    logger.info(
                      "[App] Config received via postMessage:",
                      config.backendPort
                    );
                    window.removeEventListener("message", handleMessage);
                    resolve();
                  }
                }
              };
              window.addEventListener("message", handleMessage);
            });
          }
        }

        if (!mounted) return;

        // Projects are loaded in parallel by Root.tsx's Promise.all.
        // Safety net: if they haven't loaded yet (e.g. Root.tsx load failed), retry here.
        const projectStore = useProjectStore.getState();
        if (projectStore.projects.length === 0 && !projectStore.isLoading) {
          await loadProjects();
        }

        if (!mounted) return;

        // Backend is ready — workspace restore and project-change effects handle loadChats.
        setIsBackendReady(true);
      } catch (err) {
        logger.error("Failed to initialize app:", err);
        // Don't mark as ready if we couldn't connect
        // The user will see the loading spinner until the backend is ready
      }
    };

    initializeApp();

    return () => {
      mounted = false;
    };
  }, [loadProjects]); // Run once on mount — workspace restore handles project selection

  // Connect global WebSocket for real-time updates after backend is ready.
  // We await loadChats first so the lastUserUpdateSequence is stored before
  // the stream connects — this prevents the stream from replaying events
  // that were already loaded.
  const projectId = currentProject?.id;
  useEffect(() => {
    if (!isBackendReady || !projectId) return;

    let cancelled = false;

    (async () => {
      // Ensure chats are loaded first — loadChats stores the latest user update
      // sequence which the stream uses as sinceSeq to avoid redundant replay.
      await loadChats();

      if (cancelled) return;

      const globalUpdates = useGlobalUpdatesStore.getState();
      globalUpdates.connect();
    })();

    // Initialize notification store (loads settings from localStorage/DB)
    useNotificationStore.getState().initialize();
    
    // Start permission refresh interval to detect system permission changes
    startPermissionRefresh();

    // Cleanup on unmount
    return () => {
      cancelled = true;
      useGlobalUpdatesStore.getState().disconnect();
    };
  }, [isBackendReady, projectId, loadChats]);

  // Fetch background processes on app mount to ensure we have current state
  // This is critical because processes survive server restarts and we need
  // to load them from the database into our stores on initial load.
  // Streaming events only capture NEW events, not existing state.
  useEffect(() => {
    if (!isBackendReady) return;

    // Guard: gRPC must be ready, otherwise these calls
    // hit a non-existent server and produce ERR_CONNECTION_REFUSED.
    if (!isGrpcReady()) {
      logger.info("⏭️ Skipping background process fetch — gRPC not ready");
      return;
    }

    // Fetch all processes (no worktree filter) to populate stores.
    // Both stores are kept in sync by globalUpdatesStore streaming events;
    // we only need one initial fetch and let the event handler propagate.
    logger.info("🔄 Fetching background processes on app mount");
    useProcessStore.getState().fetchProcesses();
  }, [isBackendReady]);

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
      // Open settings in full-screen mode with prompts section
      useViewerStore.getState().setSettingsMode(true, "prompts");
    };
    window.addEventListener("open-settings-prompts", handler as EventListener);
    return () =>
      window.removeEventListener(
        "open-settings-prompts",
        handler as EventListener
      );
  }, []);

  // Listen for search modal open requests (from Electron menu)
  useEffect(() => {
    const handleQuickFileOpen = () => setShowQuickFileOpen(true);
    const handleChatSearch = () => setShowChatSearch(true);
    const handleFindInFiles = () => setShowGlobalSearch(true);
    const handleCommandPalette = () => setShowCommandPalette(true);
    
    window.addEventListener("open-quick-file-open", handleQuickFileOpen);
    window.addEventListener("open-chat-search", handleChatSearch);
    window.addEventListener("open-find-in-files", handleFindInFiles);
    window.addEventListener("open-command-palette", handleCommandPalette);
    
    return () => {
      window.removeEventListener("open-quick-file-open", handleQuickFileOpen);
      window.removeEventListener("open-chat-search", handleChatSearch);
      window.removeEventListener("open-find-in-files", handleFindInFiles);
      window.removeEventListener("open-command-palette", handleCommandPalette);
    };
  }, []);

  // Load chats when project changes (use stable ID to avoid re-fires from object reference changes)
  useEffect(() => {
    if (projectId && isBackendReady) {
      logger.info(
        "🔄 Project changed, loading chats for project:",
        projectId
      );
      loadChats();
    }
  }, [projectId, isBackendReady, loadChats]);

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
            worktree_count: 0, // Temporary, will be updated when projects load
            last_active: new Date().toISOString(),
            created_at: new Date().toISOString(),
            updated_at: new Date().toISOString(),
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

    const activeChats = Array.from(chats.values()).filter((chat) => chat.state !== ChatState.ARCHIVED);
    const activityMap = useActivityStore.getState().activities;
    const activeWorkflows = activeChats.filter(
      (chat) => {
        const activity = activityMap.get(chat.id);
        return activity !== undefined && activity >= ChatActivity.RUNNING;
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
    // In Electron, skip project restoration - let useWindowContext handle it
    skipProjectRestore: isElectron,
  });

  // Listen for Electron menu events from tray and app menus
  useEffect(() => {
    if (!window.electronAPI) return;

    const cleanups: Array<() => void> = [];

    const goToNewChat = () => {
      const chatStore = useChatStore.getState();
      const worktreeStore = useWorktreeStore.getState();
      const activeChatId = chatStore.activeChatId;
      const activeChat = activeChatId
        ? chatStore.chats.get(activeChatId) ?? null
        : null;
      const currentWorktreeId =
        activeChat?.worktreeId || worktreeStore.currentWorktree?.id || null;
      chatStore.clearCurrentChat(currentWorktreeId);
    };

    const goToRecentChat = async (chatId?: string) => {
      const chatStore = useChatStore.getState();
      const project = useProjectStore.getState().currentProject;
      const worktreeStore = useWorktreeStore.getState();

      const alignWorktreeAndSelect = async (chat: Chat | undefined) => {
        if (!chat) return;
        if (project?.id) {
          if (chat.worktreeId) {
            const targetWorktree = worktreeStore.worktrees.find((worktree) => worktree.id === chat.worktreeId) ?? null;
            if (targetWorktree) {
              await worktreeStore.switchWorktreeContext(project.id, targetWorktree);
            }
          } else {
            await worktreeStore.switchWorktreeContext(project.id, null);
          }
        }
        chatStore.selectChat(chat);
      };

      const target = chatId ? chatStore.chats.get(chatId) : undefined;
      if (target && target.state !== ChatState.ARCHIVED) {
        await alignWorktreeAndSelect(target);
        return;
      }

      const sorted = Array.from(chatStore.chats.values())
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

    if ("onCreateNewTab" in window.electronAPI) {
      const unsubscribe = window.electronAPI.onCreateNewTab(goToNewChat);
      if (typeof unsubscribe === "function") cleanups.push(unsubscribe);
    }

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

        const viewerStore = useViewerStore.getState();
        viewerStore.setSettingsMode(false);
        viewerStore.setWorkflowMode(true);
      });
      if (typeof unsubscribe === "function") cleanups.push(unsubscribe);
    }

    if ("onTrayOpenWorkflow" in window.electronAPI) {
      const unsubscribe = window.electronAPI.onTrayOpenWorkflow((payload) => {
        const projectId = useProjectStore.getState().currentProject?.id;
        if (!projectId) return;

        const workflowName = payload?.workflowName;
        if (!workflowName) return;

        const viewerStore = useViewerStore.getState();
        viewerStore.setSettingsMode(false);
        viewerStore.setWorkflowMode(true, workflowName);
      });
      if (typeof unsubscribe === "function") cleanups.push(unsubscribe);
    }

    if ("onTrayGoToSettings" in window.electronAPI) {
      const unsubscribe = window.electronAPI.onTrayGoToSettings(() => {
        useViewerStore.getState().setSettingsMode(true);
      });
      if (typeof unsubscribe === "function") cleanups.push(unsubscribe);
    }

    if ("onTrayGoToProjectPicker" in window.electronAPI) {
      const unsubscribe = window.electronAPI.onTrayGoToProjectPicker(() => {
        handleNavigateToProjectPicker();
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

        const target = worktreeStore.worktrees.find((worktree) => worktree.id === targetId);
        if (!target) return;
        await worktreeStore.switchWorktreeContext(project.id, target);
      });
      if (typeof unsubscribe === "function") cleanups.push(unsubscribe);
    }

    return () => {
      cleanups.forEach((cleanup) => cleanup());
    };
  }, [handleNavigateToProjectPicker]);

  // Get onboarding state - we defer git init check until onboarding tour is completed
  const hasCompletedOnboarding = useOnboardingChecklistStore((state) => state.hasCompletedOnboarding);
  const checklistInitialized = useOnboardingChecklistStore((state) => state.isInitialized);

  // Check git initialization when a project is selected (use stable ID to avoid re-fires)
  // Skip during onboarding welcome to avoid dual modal confusion
  useEffect(() => {
    if (projectId && isBackendReady) {
      // Wait for checklist state to be loaded from database
      if (!checklistInitialized) {
        logger.info("[ModernApp] Waiting for checklist state to initialize before git check");
        return;
      }

      // Skip git init check if onboarding tour hasn't been completed yet
      if (!hasCompletedOnboarding) {
        logger.info("[ModernApp] Deferring git init check until onboarding tour completes");
        return;
      }

      // If the API key setup modal is currently open, defer git init modal.
      if (useApiKeySetupStore.getState().showModal) {
        logger.info("[ModernApp] Deferring git init check until API key modal closes");
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
    checklistInitialized,
    hasCompletedOnboarding,
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
              <NewChatView
                tabId="new-chat"
                worktreeId={pendingNewChatWorktreeId || undefined}
                onNavigateToWorktrees={noopNavigateWorktrees}
              />
            )}
          </div>
        </div>
      </div>
    );
  };

  // Show loading spinner until backend is ready
  if (!isBackendReady) {
    return <LoadingSpinner />;
  }

  // Show loading spinner while workspace is restoring
  // This prevents the "jitter" where project picker flashes before restoration completes
  if (isWorkspaceRestoring) {
    return <LoadingSpinner />;
  }

  // Show project picker if no project is selected
  if (!currentProject) {
    return (
      <div className="flex flex-col h-screen overflow-hidden">
        <Header
          ref={headerRef}
          logoSize="xl"
          windowAligned={true}
          onNavigateToSettings={() => {
            setSettingsMode(true);
          }}
          onNavigateToWorktrees={() => {
            // No longer used
          }}
          onNavigateToSettingsSection={(section) => {
            setSettingsMode(true, section);
          }}
          onNavigateToChats={() => {
            // No longer used
          }}
          onNavigateToProjects={() => {
            // No longer used
          }}
          projectPickerMode={true}
          onProjectSelect={(project) => {
            selectProject(project);
          }}
          onToggleTerminal={toggleTerminal}
          onToggleFileBrowser={() => setShowFileBrowser((prev) => !prev)}
          onToggleChatSidebar={() => setShowChatSidebar((prev) => !prev)}
        />
        <ProjectPicker
          onProjectSelected={(project) => {
            // Always select the project - initialization check will happen after navigation
            // Ensure all required properties are present for the store
            const fullProject = {
              ...project,
              is_git_repo: project.is_git_repo ?? true,
              worktree_count: project.worktree_count ?? 0,
              last_active: new Date().toISOString(),
              created_at: project.created_at ?? new Date().toISOString(),
              updated_at: project.updated_at ?? new Date().toISOString(),
            };
            selectProject(fullProject);
          }}
        />
      </div>
    );
  }

  // Settings Mode - Full screen layout without sidebars
  if (isSettingsMode) {
    return (
      <div className="flex flex-col h-screen bg-background font-mono dense-ui">
        <SettingsHeader onClose={() => {
          setSettingsMode(false);
          focusChatInput();
        }} />
        <div className="flex-1 overflow-hidden">
          <SettingsPage initialSection={settingsSection} />
        </div>
        {/* Toast Notifications */}
        <Toaster />
        {/* Onboarding wizard must render in all layout modes */}
        <OnboardingWizard />
        <ContextualTipsLayer />
      </div>
    );
  }

  // Workflow Mode - Full screen layout without sidebars
  if (isWorkflowMode) {
    return (
      <div className="flex flex-col h-screen bg-background font-mono dense-ui">
        <WorkflowHeader
          onClose={() => {
            setWorkflowMode(false);
            focusChatInput();
          }}
          onNavigateToSettings={() => {
            // setSettingsMode handles tracking that we came from workflow mode
            setSettingsMode(true);
          }}
        />
        <div className="flex-1 overflow-hidden">
          <WorkflowBuilderPageWithKey />
        </div>
        {/* Toast Notifications */}
        <Toaster />
        {/* Onboarding wizard must render in all layout modes */}
        <OnboardingWizard />
        <ContextualTipsLayer />
      </div>
    );
  }

  return (
    <div className="flex flex-col h-screen bg-background font-mono dense-ui">
      {/* Global Header */}
      <Header
        ref={headerRef}
        logoSize="xl"
        windowAligned={true}
        onNavigateToSettings={() => {
          setSettingsMode(true);
        }}
        onNavigateToSettingsSection={(section) => {
          setSettingsMode(true, section);
        }}
        onNavigateToProjectPicker={handleNavigateToProjectPicker}
        onToggleTerminal={toggleTerminal}
        onToggleFileBrowser={() => setShowFileBrowser((prev) => !prev)}
        onToggleChatSidebar={() => setShowChatSidebar((prev) => !prev)}
        onOpenWorkflows={() => {
          useViewerStore.getState().setWorkflowMode(true);
        }}
        onOpenFeedback={() => {
          setSettingsMode(true, "feedback");
        }}
      />

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
              <Sidebar paddingClass="" />
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
                <Sidebar paddingClass="" />
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
                      getWorkingDirectory={getTerminalWorkingDir}
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

      {/* API Key Setup Modal */}
      <ApiKeySetupModal />

      {/* Git Initialization Modal */}
      {showGitInitModal && gitInitProjectInfo && currentProject && (
        <InitializeGitModal
          isOpen={showGitInitModal}
          onClose={handleCloseGitInitModal}
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
      <CommandPalette
        isOpen={showCommandPalette}
        onClose={() => setShowCommandPalette(false)}
        onNavigateToSettings={() => {
          setShowCommandPalette(false);
          setSettingsMode(true);
        }}
        onNavigateToSettingsSection={(section) => {
          setShowCommandPalette(false);
          setSettingsMode(true, section);
        }}
      />
      <ChatSearch
        isOpen={showChatSearch}
        onClose={() => setShowChatSearch(false)}
      />

      {/* Toast Notifications */}
      <Toaster />

      {/* Global Update Handler - shows update modal when updates are available */}
      <GlobalUpdateHandler />

      {/* Onboarding - welcome modal + floating checklist */}
      <OnboardingWizard />
      <ContextualTipsLayer />
    </div>
  );
}

export default App;