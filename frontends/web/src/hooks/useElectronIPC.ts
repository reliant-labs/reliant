/**
 * useElectronIPC - Handles all Electron IPC event listeners
 *
 * This hook centralizes all Electron main process -> renderer communication,
 * including window management, menu handlers, deep linking, theme toggles,
 * terminal commands, and tab navigation.
 *
 * @module hooks/useElectronIPC
 */

import { useEffect } from "react";
import { logger } from "../lib/logger";
import { useChatNavigationStore } from "../store/chatNavigationStore";
import { settingsSync, SETTINGS_KEYS } from "../services/settingsSync";
import type { HeaderRef } from "../components/Layout/Header";

export interface UseElectronIPCOptions {
  /** Reference to the Header component for search functionality */
  headerRef: React.RefObject<HeaderRef | null>;
}

/**
 * Hook that manages the Electron IPC event listeners that are not shortcuts.
 *
 * Handles:
 * - Close-all-tabs from the menu
 * - Tab navigation (next/previous)
 * - Search focus events
 * - Theme toggle
 *
 * Menu items that ARE shortcuts (New Tab, Close Tab, Reopen Last Tab, Toggle
 * Terminal, New Terminal, Clear Terminal, Next/Previous Chat, Next/Previous
 * Sidebar Tab) are not handled here: they arrive by shortcut id on the one
 * `menu-shortcut` channel, at the keyboard host (components/Shortcuts), which
 * runs them through the same dispatcher a keystroke uses — on every route,
 * not only while this workspace is mounted.
 *
 * All listeners are properly cleaned up on unmount.
 */
export function useElectronIPC(options: UseElectronIPCOptions): void {
  const { headerRef: _headerRef } = options;

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
