import { useEffect, useMemo, useCallback, useRef } from 'react';
import { logger } from '../lib/logger';
import { useShortcutsStore, type KeyBinding } from '../store/shortcutsStore';

interface KeyboardShortcutsConfig {
  disableDevTools?: boolean;
}

// Helper function to create a lookup key from a KeyBinding
function createLookupKey(binding: KeyBinding): string {
  // Normalize letter keys to uppercase for consistent matching
  let key = binding.key;
  if (key.length === 1 && /[a-zA-Z]/.test(key)) {
    key = key.toUpperCase();
  }
  
  const modifiers = [
    binding.ctrl ? 'ctrl' : '',
    binding.meta ? 'meta' : '',
    binding.shift ? 'shift' : '',
    binding.alt ? 'alt' : '',
  ].filter(Boolean).join('+');

  return modifiers ? `${modifiers}+${key}` : key;
}

// Helper function to create a lookup key from a KeyboardEvent
function createEventLookupKey(event: KeyboardEvent): string {
  // Normalize arrow key names to match binding format (Down/Up/Left/Right instead of ArrowDown/ArrowUp/etc)
  let key = event.key;
  if (key === 'ArrowDown') key = 'Down';
  else if (key === 'ArrowUp') key = 'Up';
  else if (key === 'ArrowLeft') key = 'Left';
  else if (key === 'ArrowRight') key = 'Right';
  // Normalize bracket keys - when Shift is pressed, [ and ] become { and }, but we want to match [ and ]
  else if (key === '{' && event.shiftKey) key = '[';
  else if (key === '}' && event.shiftKey) key = ']';
  // Normalize letter keys - when Shift is pressed, event.key is uppercase, but we want consistent matching
  // Convert to uppercase for letter keys to ensure consistent matching
  else if (key.length === 1 && /[a-zA-Z]/.test(key)) {
    key = key.toUpperCase();
  }

  const modifiers = [
    event.ctrlKey ? 'ctrl' : '',
    event.metaKey ? 'meta' : '',
    event.shiftKey ? 'shift' : '',
    event.altKey ? 'alt' : '',
  ].filter(Boolean).join('+');

  return modifiers ? `${modifiers}+${key}` : key;
}

// Helper to check if target is in an input field (memoized)
function isTargetInInputField(target: HTMLElement): boolean {
  return (
    target.tagName === 'INPUT' ||
    target.tagName === 'TEXTAREA' ||
    target.contentEditable === 'true' ||
    target.hasAttribute('tabindex') ||
    target.closest('[contenteditable="true"]') !== null ||
    target.closest('input') !== null ||
    target.closest('textarea') !== null
  );
}

// Helper to check if a modal is currently open
function isModalOpen(): boolean {
  // Check for image preview modal or other modals with high z-index
  return document.querySelector('[data-modal-open="true"]') !== null;
}

// Helper to check if Monaco editor is focused (for Escape key handling)
function isMonacoEditorFocused(): boolean {
  const activeElement = document.activeElement;
  if (!activeElement) return false;
  
  // Check if we're inside a Monaco editor
  // Monaco editor has class 'monaco-editor' and the textarea has class 'inputarea'
  const isInMonaco = activeElement.closest('.monaco-editor') !== null;
  const isMonacoTextarea = activeElement.classList.contains('inputarea') || 
                          activeElement.closest('.monaco-editor .inputarea') !== null;
  
  return isInMonaco || isMonacoTextarea;
}

export function useKeyboardShortcuts(config: KeyboardShortcutsConfig = {}) {
  const {
    disableDevTools = true
  } = config;

  useEffect(() => {
    // Only apply production restrictions in Electron production builds (installed versions)
    const isElectronProd = window.RELIANT_CONFIG?.isElectron && !window.RELIANT_CONFIG?.isDev;

    // Skip restrictions if not in production or DevTools are allowed
    if (!isElectronProd || !disableDevTools) {
      return;
    }

    const preventDevActions = (e: KeyboardEvent) => {
      // Don't prevent shortcuts in terminal - let terminal handle its own input
      const target = e.target as HTMLElement;
      const isInTerminal = target.closest('.xterm') !== null || 
                          target.closest('[class*="terminal"]') !== null;
      
      if (isInTerminal) {
        return; // Allow terminal to handle all keys
      }

      // Prevent Ctrl+R, Cmd+R, F5 (refresh)
      if ((e.ctrlKey || e.metaKey) && e.key === "r") {
        e.preventDefault();
        logger.info("🚫 Page refresh disabled in production");
        return false;
      }
      if (e.key === "F5") {
        e.preventDefault();
        logger.info("🚫 Page refresh disabled in production");
        return false;
      }

      // Prevent DevTools shortcuts
      if (e.ctrlKey && e.shiftKey && e.key === "I") {
        e.preventDefault();
        logger.info("🚫 DevTools disabled in production");
        return false;
      }
      if (e.metaKey && e.altKey && e.key === "I") {
        e.preventDefault();
        logger.info("🚫 DevTools disabled in production");
        return false;
      }
      if (e.ctrlKey && e.shiftKey && e.key === "C") {
        e.preventDefault();
        logger.info("🚫 DevTools disabled in production");
        return false;
      }
      if (e.metaKey && e.altKey && e.key === "C") {
        e.preventDefault();
        logger.info("🚫 DevTools disabled in production");
        return false;
      }
      if (e.key === "F12") {
        e.preventDefault();
        logger.info("🚫 DevTools disabled in production");
        return false;
      }

      // Prevent View Source shortcuts
      if (e.ctrlKey && e.key === "u") {
        e.preventDefault();
        logger.info("🚫 View Source disabled in production");
        return false;
      }
      if (e.metaKey && e.key === "u") {
        e.preventDefault();
        logger.info("🚫 View Source disabled in production");
        return false;
      }

      // Prevent other dev-related shortcuts
      if (e.ctrlKey && e.key === "i") {
        e.preventDefault();
        logger.info("🚫 Page Info disabled in production");
        return false;
      }
      if (e.ctrlKey && e.shiftKey && e.key === "J") {
        e.preventDefault();
        logger.info("🚫 Console disabled in production");
        return false;
      }
      if (e.metaKey && e.altKey && e.key === "J") {
        e.preventDefault();
        logger.info("🚫 Console disabled in production");
        return false;
      }

      // Note: Cmd/Ctrl+P is now used for search focus, not print prevention
      if (e.ctrlKey && e.key === "s") {
        e.preventDefault();
        logger.info("🚫 Save disabled in production");
        return false;
      }
      if (e.metaKey && e.key === "s") {
        e.preventDefault();
        logger.info("🚫 Save disabled in production");
        return false;
      }
    };

    // Add event listener for keydown
    document.addEventListener("keydown", preventDevActions);

    // Cleanup
    return () => {
      document.removeEventListener("keydown", preventDevActions);
    };
  }, [disableDevTools]);

  // Context menu handling is now done at the Electron level
  // This provides better control and works consistently across all environments

  useEffect(() => {
    const isElectronProd = window.RELIANT_CONFIG?.isElectron && !window.RELIANT_CONFIG?.isDev;

    // Skip console disabling for development builds
    if (!isElectronProd) {
      return;
    }

    // Disable console logging in production
    const noop = () => {};
    const originalConsole = {
      log: window.console.log,
      warn: window.console.warn,
      error: window.console.error,
      info: window.console.info,
      debug: window.console.debug,
      trace: window.console.trace,
    };

    window.console.log = noop;
    window.console.warn = noop;
    window.console.error = noop;
    window.console.info = noop;
    window.console.debug = noop;
    window.console.trace = noop;

    // Cleanup - restore original console methods
    return () => {
      window.console.log = originalConsole.log;
      window.console.warn = originalConsole.warn;
      window.console.error = originalConsole.error;
      window.console.info = originalConsole.info;
      window.console.debug = originalConsole.debug;
      window.console.trace = originalConsole.trace;
    };
  }, []);
}

// Additional hook for app-specific keyboard shortcuts
export function useAppKeyboardShortcuts(handlers: {
  onNewChat?: () => void;
  onOpenProject?: () => void;
  onToggleSettings?: () => void;
  onToggleSidebar?: () => void;
  onCloseTab?: () => void;
  onNextChat?: () => void;
  onPrevChat?: () => void;
  onNextFileTab?: () => void;
  onPrevFileTab?: () => void;
  // Search modals
  onQuickFileOpen?: () => void;
  onFindInFiles?: () => void;
  onFindReplace?: () => void;
  onCommandPalette?: () => void;
  onChatSearch?: () => void;
  onFocusChat?: () => void;
  onFocusFileEditor?: () => void;
  onReopenLastClosedFile?: () => void;
  onCutFile?: () => void;
  onCopyFile?: () => void;
  onPasteFile?: () => void;
  onStopStreaming?: () => void;
  onApproveToolRequests?: () => void;
  onToggleTerminal?: () => void;
  onNewTerminal?: () => void;
  onToggleFileBrowser?: () => void;

  onOpenWorktrees?: () => void;
  onOpenProjects?: () => void;
  onOpenWorkflows?: () => void;
  onNextRightSidebarTab?: () => void;
  onPrevRightSidebarTab?: () => void;
}) {
  const shortcuts = useShortcutsStore((state) => state.shortcuts);
  const initializeShortcuts = useShortcutsStore((state) => state.initializeShortcuts);

  // Use ref to track handlers to avoid effect churn
  const handlersRef = useRef(handlers);

  // Update ref when handlers change
  useEffect(() => {
    handlersRef.current = handlers;
  }, [handlers]);

  // Initialize shortcuts on first use
  useEffect(() => {
    initializeShortcuts();
  }, [initializeShortcuts]);

  // Shortcuts that should work even when typing in input fields or terminal
  const globalShortcuts = useMemo(() => {
    const shortcuts = new Set([
      // Search modals
      'onQuickFileOpen',
      'onFindInFiles',
      'onFindReplace',
      'onCommandPalette',
      'onChatSearch',
      'onFocusChat',
      'onFocusFileEditor',
      'onReopenLastClosedFile',
      // File operations (should work everywhere)
      'onCutFile',
      'onCopyFile',
      'onPasteFile',
      // Core actions
      'onStopStreaming',
      'onApproveToolRequests',
      'onNewChat',
      'onOpenProject',
      'onCloseTab',
      'onToggleSettings',
      'onToggleSidebar',
      'onNextChat',
      'onPrevChat',
      'onNextFileTab',
      'onPrevFileTab',
      'onToggleTerminal',
      'onNewTerminal',
      'onToggleFileBrowser',
      // Navigation panels
      'onOpenWorktrees',
      'onOpenProjects',
      'onOpenWorkflows',
      'onNextRightSidebarTab',
      'onPrevRightSidebarTab',
    ]);
    
    return shortcuts;
  }, []);

  // Create O(1) lookup map from shortcuts: key combination -> shortcut
  const shortcutMap = useMemo(() => {
    const map = new Map<string, typeof shortcuts[keyof typeof shortcuts]>();

    Object.values(shortcuts).forEach(shortcut => {
      // Skip shortcuts with empty or invalid bindings
      if (!shortcut.currentBinding || !shortcut.currentBinding.key) {
        return;
      }
      const key = createLookupKey(shortcut.currentBinding);
      map.set(key, shortcut);
    });

    return map;
  }, [shortcuts]);

  // Memoized handler to prevent recreation
  const handleKeyDown = useCallback((e: KeyboardEvent) => {
    // Early return if no shortcuts loaded
    if (shortcutMap.size === 0) {
      return;
    }

    // Check for Cmd+Ctrl+Arrow (Mac) or Ctrl+Alt+Arrow (Windows)
    const isMac = typeof window !== 'undefined' && 
      (window.navigator.platform.toUpperCase().includes('MAC') || 
       window.navigator.userAgent.toUpperCase().includes('MAC'));
    const isCmdCtrl = isMac ? (e.metaKey && e.ctrlKey) : (e.ctrlKey && e.altKey);
    const isCmdCtrlArrow = isCmdCtrl && (e.key === 'ArrowUp' || e.key === 'ArrowDown' || e.key === 'ArrowLeft' || e.key === 'ArrowRight');
    
    // For Cmd+Ctrl+Arrow, immediately stop propagation to prevent Monaco/OS from seeing it
    // This MUST happen before any other processing to prevent system beep
    if (isCmdCtrlArrow) {
      // IMMEDIATELY stop all propagation - this prevents Monaco and OS from handling it
      e.stopImmediatePropagation();
      e.preventDefault();
      e.stopPropagation();
      
      // Look up the shortcut
      const eventKey = createEventLookupKey(e);
      const shortcut = shortcutMap.get(eventKey);
      
      if (shortcut) {
        // Blur Monaco editor if it's focused and focus chat input
        if (isMonacoEditorFocused()) {
          const activeElement = document.activeElement as HTMLElement;
          if (activeElement && activeElement.blur) {
            activeElement.blur();
          }
          setTimeout(() => {
            window.dispatchEvent(new CustomEvent('focus-chat-input'));
          }, 0);
        }
        
        // Execute the handler
        const handler = handlersRef.current[shortcut.handler as keyof typeof handlers];
        if (handler) {
          handler();
        }
      }
      return; // Always return early for Cmd+Ctrl+Arrow to prevent any other handling
    }

    // Skip ESC handling if a modal is open or Monaco editor is focused - let them handle it
    if (e.key === 'Escape' && (isModalOpen() || isMonacoEditorFocused())) {
      return;
    }

    // Handle Cmd+L / Ctrl+L specially when Monaco is focused
    // If there's a selection, add it to chat context first, then focus chat
    // If no selection, just focus chat (normal behavior)
    if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'l') {
      const isMonaco = isMonacoEditorFocused();
      if (isMonaco) {
        // Dispatch event for FileViewerTab to handle (it has access to editor instance)
        // FileViewerTab will check for selection and add context if present
        const customEvent = new CustomEvent('cmd-l-in-monaco', { 
          cancelable: true,
          detail: { originalEvent: e }
        });
        const notCancelled = window.dispatchEvent(customEvent);
        
        // If FileViewerTab handled it (prevented default), don't proceed with normal focus
        if (!notCancelled) {
          e.preventDefault();
          e.stopPropagation();
          return;
        }
        // Otherwise, continue with normal focus chat behavior below
      }
    }

    // Skip Cmd+S/Ctrl+S if Monaco editor is focused - let Monaco handle it
    if ((e.metaKey || e.ctrlKey) && e.key === 's') {
      const isMonaco = isMonacoEditorFocused();
      if (isMonaco) {
        return;
      }
    }

    // Skip Cmd+F/Ctrl+F if Monaco editor is focused - let Monaco handle it (find/replace)
    if ((e.metaKey || e.ctrlKey) && e.key === 'f') {
      const isMonaco = isMonacoEditorFocused();
      if (isMonaco) {
        // Let Monaco handle Cmd+F for find/replace
        return;
      }
    }

    // Cmd+X/C/V are special: they must NOT break normal clipboard ops.
    // Only treat them as "file operations" when focus is in the file tree.
    if ((e.metaKey || e.ctrlKey) && (e.key === 'x' || e.key === 'c' || e.key === 'v')) {
      const target = e.target as HTMLElement;
      // IMPORTANT: `isTargetInInputField` treats any `[tabindex]` element as an input field,
      // which would incorrectly classify the file tree container. For clipboard shortcuts,
      // we only want to treat true text-entry elements as "inputs".
      const inTextEntryField =
        target.tagName === 'INPUT' ||
        target.tagName === 'TEXTAREA' ||
        target.isContentEditable ||
        target.closest('input, textarea, [contenteditable="true"]') !== null;
      const selection = window.getSelection();
      const hasTextSelection = selection && selection.toString().trim().length > 0;
      const activeElement = document.activeElement as HTMLElement | null;
      const inFileTree =
        !!target.closest?.(".file-tree-container") ||
        !!activeElement?.closest?.(".file-tree-container");

      // If the user is editing text or has a text selection, always let the browser/Monaco handle it.
      // This fixes global copy/paste being broken by file clipboard shortcuts.
      if (inTextEntryField || hasTextSelection) {
        return;
      }

      // If we're not in the file tree, do not steal clipboard shortcuts.
      if (!inFileTree) {
        return;
      }
      // Otherwise continue to shortcut handler below (file operations).
    }

    // Only skip Monaco-specific shortcuts when Monaco is focused
    // Skip Cmd+Shift+ArrowLeft/Right (line selection) - Monaco handles this
    if ((e.metaKey || e.ctrlKey) && e.shiftKey && (e.key === 'ArrowLeft' || e.key === 'ArrowRight')) {
      if (isMonacoEditorFocused()) {
        return;
      }
    }

    // Skip Cmd+Shift+Up/Down (top/bottom selection) - Monaco handles this
    if ((e.metaKey || e.ctrlKey) && e.shiftKey && (e.key === 'ArrowUp' || e.key === 'ArrowDown')) {
      if (isMonacoEditorFocused()) {
        return;
      }
    }

    // DO NOT skip Cmd+Ctrl+Arrow - these are for navigation (chats, sidebar) and should work even when Monaco is focused
    // (Note: Cmd+Ctrl+Arrow is handled above with early return)

    // Create lookup key from event - O(1) operation
    const eventKey = createEventLookupKey(e);
    
    // O(1) lookup instead of O(n) iteration
    const shortcut = shortcutMap.get(eventKey);

    if (!shortcut) {
      return; // Early return - no matching shortcut
    }

    // Check if we're in an input field
    const target = e.target as HTMLElement;
    const inInputField = isTargetInInputField(target);

    // Skip non-global shortcuts if we're in an input field
    // NOTE: Clipboard shortcuts (Cmd/Ctrl+X/C/V) are handled above so we don't break normal copy/paste.
    if (inInputField && !globalShortcuts.has(shortcut.handler)) {
      return;
    }

    // Execute handler
    const handler = handlersRef.current[shortcut.handler as keyof typeof handlers];
    if (handler) {
      // CRITICAL: Prevent default BEFORE executing handler to avoid system beep
      // This prevents Monaco or browser from trying to handle it and causing the beep
      // Only prevent default if we haven't already (for file operations we prevent earlier)
      if (!e.defaultPrevented) {
        e.preventDefault();
        e.stopPropagation();
      }
      if (isCmdCtrlArrow) {
        e.stopImmediatePropagation();
      }
      
      // Execute handler
      try {
        handler();
      } catch (error) {
        console.error('[Shortcuts] Handler execution failed:', error);
      }
    }
  }, [shortcutMap, globalShortcuts]);

  useEffect(() => {
    // Don't register if shortcuts aren't loaded
    if (shortcutMap.size === 0) {
      return;
    }

    // Create a separate early handler for Cmd+Ctrl+Arrow that runs FIRST
    // This must run before any other handlers to prevent beep
    const earlyHandler = (e: KeyboardEvent) => {
      const isMac = typeof window !== 'undefined' && 
        (window.navigator.platform.toUpperCase().includes('MAC') || 
         window.navigator.userAgent.toUpperCase().includes('MAC'));
      const isCmdCtrl = isMac ? (e.metaKey && e.ctrlKey) : (e.ctrlKey && e.altKey);
      const isCmdCtrlArrow = isCmdCtrl && (e.key === 'ArrowUp' || e.key === 'ArrowDown' || e.key === 'ArrowLeft' || e.key === 'ArrowRight');
      
      if (isCmdCtrlArrow) {
        // IMMEDIATELY stop everything - this runs before our main handler
        e.stopImmediatePropagation();
        e.preventDefault();
        e.stopPropagation();
        
        // Blur Monaco and focus chat
        if (isMonacoEditorFocused()) {
          const activeElement = document.activeElement as HTMLElement;
          if (activeElement && activeElement.blur) {
            activeElement.blur();
          }
          setTimeout(() => {
            window.dispatchEvent(new CustomEvent('focus-chat-input'));
          }, 0);
        }
        
        // Look up and execute shortcut using current shortcutMap and handlersRef
        const eventKey = createEventLookupKey(e);
        const shortcut = shortcutMap.get(eventKey);
        if (shortcut) {
          const handler = handlersRef.current[shortcut.handler as keyof typeof handlers];
          if (handler) {
            handler();
          }
        }
      }
    };

    // Register early handler on window with capture phase - runs before document handlers
    window.addEventListener('keydown', earlyHandler, true);
    
    // Also register main handler on document
    document.addEventListener('keydown', handleKeyDown, true);

    return () => {
      window.removeEventListener('keydown', earlyHandler, true);
      document.removeEventListener('keydown', handleKeyDown, true);
    };
  }, [handleKeyDown, shortcutMap, handlersRef]);
}