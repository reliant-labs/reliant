/**
 * useElectronIPC - Handles all Electron IPC event listeners
 *
 * This hook centralizes all Electron main process -> renderer communication,
 * including window management, menu handlers, deep linking, theme toggles,
 * terminal commands, and tab navigation.
 *
 * @module hooks/useElectronIPC
 */

import { useEffect, useCallback, useRef } from "react";
import { logger } from "../lib/logger";
import { useViewerStore } from "../store/viewerStore";
import { useChatNavigationStore } from "../store/chatNavigationStore";
import { settingsSync, SETTINGS_KEYS } from "../services/settingsSync";
import type { HeaderRef } from "../components/Layout/Header";

export interface UseElectronIPCOptions {
  /** Reference to the Header component for search functionality */
  headerRef: React.RefObject<HeaderRef | null>;
  /** Whether the terminal panel is currently open */
  isTerminalOpen: boolean;
  /** Function to toggle terminal visibility */
  toggleTerminal: () => void;
  /** Function to open the terminal panel */
  openTerminal: () => void;
  /** Function to create a new terminal session */
  createTerminalSession: (
    workingDir?: string,
    projectId?: string,
    worktreeId?: string
  ) => void;
  /** Function to get the terminal working directory */
  getTerminalWorkingDir: () => string | undefined;
  /** Function to get the current worktree ID */
  getCurrentWorktreeId: () => string | undefined;
  /** Current project ID */
  currentProjectId?: string;
}

/**
 * Hook that manages all Electron IPC event listeners.
 *
 * Handles:
 * - Tab close/close-all events from menu
 * - Tab navigation (next/previous)
 * - Search focus events
 * - Terminal commands (toggle, new, clear)
 * - Theme toggle
 *
 * All listeners are properly cleaned up on unmount.
 */
export function useElectronIPC(options: UseElectronIPCOptions): void {
  const {
    headerRef: _headerRef,
    isTerminalOpen,
    toggleTerminal,
    openTerminal,
    createTerminalSession,
    getTerminalWorkingDir,
    getCurrentWorktreeId,
    currentProjectId,
  } = options;

  // Use refs for values that change frequently to avoid effect re-runs
  const isTerminalOpenRef = useRef(isTerminalOpen);
  const currentProjectIdRef = useRef(currentProjectId);
  const getTerminalWorkingDirRef = useRef(getTerminalWorkingDir);
  const getCurrentWorktreeIdRef = useRef(getCurrentWorktreeId);

  // Keep refs in sync
  useEffect(() => {
    isTerminalOpenRef.current = isTerminalOpen;
  }, [isTerminalOpen]);

  useEffect(() => {
    currentProjectIdRef.current = currentProjectId;
  }, [currentProjectId]);

  useEffect(() => {
    getTerminalWorkingDirRef.current = getTerminalWorkingDir;
  }, [getTerminalWorkingDir]);

  useEffect(() => {
    getCurrentWorktreeIdRef.current = getCurrentWorktreeId;
  }, [getCurrentWorktreeId]);

  // Memoized handler for closing current tab
  const handleCloseCurrentTab = useCallback(() => {
    try {
      const viewerStore = useViewerStore.getState();
      const { viewers } = viewerStore;
      const viewerClosed = viewerStore.closeActiveViewer();

      if (viewerClosed) {
        logger.info("🗑️ Electron: Closed active viewer tab");
        return;
      }

      // No viewers left - close the window
      if (viewers.length === 0 && window.electronAPI?.closeWindowIfNoTabs) {
        logger.info("🗑️ Electron: No tabs remaining, closing window");
        window.electronAPI.closeWindowIfNoTabs(0);
      } else {
        logger.info("🗑️ Electron: No active viewer to close");
      }
    } catch (error) {
      logger.error("🛡️ Electron close tab handler error:", error);
    }
  }, []);

  // Close current tab listener
  useEffect(() => {
    if (!window.electronAPI?.onCloseCurrentTab) return;

    const unsubscribe = window.electronAPI.onCloseCurrentTab(() => {
      handleCloseCurrentTab();
    });

    return typeof unsubscribe === "function" ? unsubscribe : undefined;
  }, [handleCloseCurrentTab]);

  // Close all tabs listener
  useEffect(() => {
    if (!window.electronAPI?.onCloseAllTabs) return;

    const unsubscribe = window.electronAPI.onCloseAllTabs(() => {
      useChatNavigationStore.getState().clearQueue();
    });

    return typeof unsubscribe === "function" ? unsubscribe : undefined;
  }, []);

  // Tab navigation listener
  useEffect(() => {
    if (!window.electronAPI?.onNavigateTab) return;

    const unsubscribe = window.electronAPI.onNavigateTab(
      (direction: "next" | "previous") => {
        const { navigateNext, navigatePrev } =
          useChatNavigationStore.getState();
        if (direction === "next") {
          navigateNext();
        } else {
          navigatePrev();
        }
      }
    );

    return typeof unsubscribe === "function" ? unsubscribe : undefined;
  }, []);

  // Global search focus listener - triggers quick file open
  useEffect(() => {
    if (!window.electronAPI?.onFocusGlobalSearch) return;

    const unsubscribe = window.electronAPI.onFocusGlobalSearch(() => {
      // Dispatch custom event that ModernApp listens for
      window.dispatchEvent(new CustomEvent("open-quick-file-open"));
    });

    return typeof unsubscribe === "function" ? unsubscribe : undefined;
  }, []);

  // Chat search focus listener - triggers chat search modal
  useEffect(() => {
    if (!window.electronAPI?.onFocusChatSearch) return;

    const unsubscribe = window.electronAPI.onFocusChatSearch(() => {
      // Dispatch custom event that ModernApp listens for
      window.dispatchEvent(new CustomEvent("open-chat-search"));
    });

    return typeof unsubscribe === "function" ? unsubscribe : undefined;
  }, []);

  // Toggle terminal listener
  useEffect(() => {
    if (!window.electronAPI || !("onToggleTerminal" in window.electronAPI)) return;

    const unsubscribe = (
      window.electronAPI as unknown as {
        onToggleTerminal: (callback: () => void) => (() => void) | void;
      }
    ).onToggleTerminal(() => {
      toggleTerminal();
    });

    return typeof unsubscribe === "function" ? unsubscribe : undefined;
  }, [toggleTerminal]);

  // New terminal listener
  useEffect(() => {
    if (!window.electronAPI || !("onNewTerminal" in window.electronAPI)) return;

    const unsubscribe = (
      window.electronAPI as unknown as {
        onNewTerminal: (callback: () => void) => (() => void) | void;
      }
    ).onNewTerminal(() => {
      // Open terminal panel if not open
      if (!isTerminalOpenRef.current) {
        openTerminal();
      }
      // Create new terminal session with context-aware working directory
      const workingDir = getTerminalWorkingDirRef.current();
      const projectId = currentProjectIdRef.current;
      const worktreeId = getCurrentWorktreeIdRef.current();
      createTerminalSession(workingDir, projectId, worktreeId);
    });

    return typeof unsubscribe === "function" ? unsubscribe : undefined;
  }, [openTerminal, createTerminalSession]);

  // Clear terminal listener
  useEffect(() => {
    if (!window.electronAPI || !("onClearTerminal" in window.electronAPI)) return;

    const unsubscribe = (
      window.electronAPI as unknown as {
        onClearTerminal: (callback: () => void) => (() => void) | void;
      }
    ).onClearTerminal(() => {
      // Send clear command to active terminal
      const event = new CustomEvent("clear-active-terminal");
      window.dispatchEvent(event);
    });

    return typeof unsubscribe === "function" ? unsubscribe : undefined;
  }, []);

  // Theme toggle listener
  useEffect(() => {
    const handleThemeToggle = () => {
      const isDark = document.documentElement.classList.contains("dark");
      const newTheme = isDark ? "light" : "dark";

      if (newTheme === "dark") {
        document.documentElement.classList.add("dark");
      } else {
        document.documentElement.classList.remove("dark");
      }

      // Save using settingsSync for consistent storage
      settingsSync.setSetting(SETTINGS_KEYS.THEME, newTheme).catch((error) => {
        logger.error("Failed to save theme setting:", error);
      });

      // Dispatch theme-applied event so components can update
      window.dispatchEvent(new CustomEvent("theme-applied"));
    };

    window.addEventListener("theme-toggle", handleThemeToggle);
    return () => window.removeEventListener("theme-toggle", handleThemeToggle);
  }, []);
}
