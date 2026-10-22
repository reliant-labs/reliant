/**
 * Focus Management System
 * 
 * Centralized focus management for the Reliant app.
 * Provides utilities for focusing specific elements and restoring focus after
 * modal/panel interactions.
 * 
 * Custom Events:
 * - 'focus-chat-input': Focus the chat text area
 * - 'focus-terminal': Focus the active terminal
 * - 'focus-restored': Dispatched after focus is restored (for debugging)
 */

// Track the last meaningful focused element for restoration
let lastMeaningfulElement: HTMLElement | null = null;

/**
 * Elements considered "meaningful" for focus restoration.
 * When a modal/panel closes, we want to restore focus to one of these,
 * not to random UI elements like buttons.
 */
const MEANINGFUL_SELECTORS = [
  'textarea[data-testid="chat-input"]', // Chat input
  '.xterm-helper-textarea', // Terminal input (xterm's hidden textarea)
  'input[type="text"]', // Search inputs
  'input[type="search"]',
];

/**
 * Check if an element is a "meaningful" focus target
 */
function isMeaningfulElement(element: Element | null): boolean {
  if (!element) return false;
  
  return MEANINGFUL_SELECTORS.some(selector => {
    try {
      return element.matches(selector);
    } catch {
      return false;
    }
  });
}

/**
 * Focus the chat input textarea
 */
export function focusChatInput(): void {
  window.dispatchEvent(new CustomEvent('focus-chat-input'));
}

/**
 * Focus the active terminal
 */
export function focusTerminal(): void {
  window.dispatchEvent(new CustomEvent('focus-terminal'));
}

/**
 * Save the current focused element if it's meaningful.
 * Call this before opening a modal/panel.
 */
export function saveFocusedElement(): void {
  const activeElement = document.activeElement as HTMLElement | null;
  if (activeElement && isMeaningfulElement(activeElement)) {
    lastMeaningfulElement = activeElement;
  }
}

/**
 * Restore focus to the last meaningful element, or fall back to chat input.
 * Call this when closing a modal/panel.
 */
export function restoreFocus(): void {
  // Small delay to ensure the modal/panel has fully closed
  requestAnimationFrame(() => {
    if (lastMeaningfulElement && document.body.contains(lastMeaningfulElement)) {
      try {
        lastMeaningfulElement.focus();
        window.dispatchEvent(new CustomEvent('focus-restored', { 
          detail: { element: lastMeaningfulElement.tagName } 
        }));
        return;
      } catch {
        // Element may no longer be focusable
      }
    }
    
    // Fall back to chat input
    focusChatInput();
  });
}

/**
 * Clear the saved focused element.
 * Useful when you want to ensure focus goes to chat input.
 */
export function clearSavedFocus(): void {
  lastMeaningfulElement = null;
}

/**
 * Check if the terminal currently has focus
 */
export function isTerminalFocused(): boolean {
  const activeElement = document.activeElement;
  return activeElement?.closest('.xterm') !== null;
}

/**
 * Check if the chat input currently has focus
 */
export function isChatInputFocused(): boolean {
  const activeElement = document.activeElement;
  return activeElement?.matches('textarea[data-testid="chat-input"]') ?? false;
}

/**
 * Check if any input field has focus (for determining if shortcuts should be global)
 */
export function isInputFocused(): boolean {
  const activeElement = document.activeElement;
  if (!activeElement) return false;
  
  return (
    activeElement.tagName === 'INPUT' ||
    activeElement.tagName === 'TEXTAREA' ||
    activeElement.getAttribute('contenteditable') === 'true' ||
    activeElement.closest('[contenteditable="true"]') !== null
  );
}

/**
 * Hook for components that need focus management
 * Returns utility functions for managing focus
 */
export function useFocusManager() {
  return {
    focusChatInput,
    focusTerminal,
    saveFocusedElement,
    restoreFocus,
    clearSavedFocus,
    isTerminalFocused,
    isChatInputFocused,
    isInputFocused,
  };
}

export default useFocusManager;
