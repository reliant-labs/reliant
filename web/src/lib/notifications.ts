/**
 * Notification Service
 * 
 * Cross-platform OS notifications using the Web Notification API.
 * Works on Windows, macOS, and Linux (in both Electron and browser).
 * Uses native OS notification sounds for a native feel.
 * 
 * In Electron, uses native Electron notifications for reliable click handling
 * when the app is already focused.
 */

import { logger } from "./logger";

// Initialize IPC listener for Electron notification clicks
// This must be set up when the module loads, but electronAPI might not be available yet
// So we'll set it up both at module load AND when showNotification is called
function setupNotificationClickListener() {
  if (typeof window === "undefined") return;
  if ((window as any).__notificationClickListenerSetup) return;
  if (!(window as any).electronAPI) return;
  if (!(window.electronAPI as any).onNotificationClick) return;
  
  (window as any).__notificationClickListenerSetup = true;
  
  (window.electronAPI as any).onNotificationClick((tag: string) => {
    const handlers = (window as any).__notificationHandlers;
    if (handlers && handlers instanceof Map && handlers.has(tag)) {
      const handler = handlers.get(tag);
      if (handler && handler.onClick) {
        try {
          handler.onClick();
        } catch (error) {
          logger.error("[Notifications] Error calling onClick handler", error);
        }
      }
      handlers.delete(tag);
    }
  });
}

// Try to set up listener at module load (if electronAPI is available)
if (typeof window !== "undefined") {
  // Use setTimeout to ensure electronAPI is available
  setTimeout(() => {
    setupNotificationClickListener();
  }, 0);
  
  // Also try immediately
  setupNotificationClickListener();
}

const LOG_PREFIX = "[Notifications]";

// Notification permission states
export type NotificationPermission = "granted" | "denied" | "default";

// Notification options
export interface NotificationOptions {
  title: string;
  body: string;
  icon?: string;
  tag?: string; // Used to replace existing notifications with same tag
  onClick?: () => void | Promise<void>; // Can be async
  onClose?: () => void;
  silent?: boolean; // If true, don't play OS notification sound
}

// Sound options (simplified - just controls whether OS sound plays)
export interface SoundOptions {
  enabled: boolean;
}

/**
 * Check if notifications are supported in this environment
 */
export function isNotificationSupported(): boolean {
  return "Notification" in window;
}

/**
 * Check if the app window is currently focused
 */
export function isWindowFocused(): boolean {
  return document.hasFocus();
}

/**
 * Get the current notification permission status
 */
export function getNotificationPermission(): NotificationPermission {
  if (!isNotificationSupported()) {
    return "denied";
  }
  return Notification.permission as NotificationPermission;
}

/**
 * Request notification permission from the user
 * Returns the new permission status
 */
export async function requestNotificationPermission(): Promise<NotificationPermission> {
  if (!isNotificationSupported()) {
    logger.warn(`${LOG_PREFIX} Notifications not supported in this environment`);
    return "denied";
  }

  // If already granted or denied, return current status
  if (Notification.permission !== "default") {
    return Notification.permission as NotificationPermission;
  }

  try {
    const permission = await Notification.requestPermission();
    return permission as NotificationPermission;
  } catch (error) {
    logger.error(`${LOG_PREFIX} Failed to request permission:`, error);
    return "denied";
  }
}

/**
 * Show an OS notification
 * Returns true if notification was shown, false otherwise
 * 
 * Note: This function checks Notification.permission directly.
 * For consistency, prefer using the notification store's permission state
 * via shouldShowNotification() before calling this function.
 */
export function showNotification(
  options: NotificationOptions,
  soundOptions?: SoundOptions
): boolean {
  // In Electron, use Electron's native notification system
  // This is required because web Notification API onclick doesn't fire when app is already focused
  if (typeof window !== "undefined" && window.electronAPI && (window.electronAPI as any).showNotification) {
    try {
      // Store the onClick handler globally so we can call it when IPC message arrives
      if (options.onClick) {
        const notificationId = options.tag || `notification-${Date.now()}-${Math.random()}`;
        (window as any).__notificationHandlers = (window as any).__notificationHandlers || new Map();
        (window as any).__notificationHandlers.set(notificationId, {
          onClick: options.onClick,
          timestamp: Date.now(),
        });

        // TTL cleanup: remove handler after 5 minutes if notification was never clicked
        const HANDLER_TTL_MS = 5 * 60 * 1000;
        setTimeout(() => {
          const handlers = (window as any).__notificationHandlers;
          if (handlers && handlers instanceof Map) {
            handlers.delete(notificationId);
          }
        }, HANDLER_TTL_MS);
        
        // Send notification request to Electron main process via IPC
        // The main process will create a native notification and send back click events
        (window.electronAPI as any).showNotification({
          title: options.title,
          body: options.body,
          tag: notificationId,
          icon: options.icon,
          silent: options.silent ?? !(soundOptions?.enabled ?? true),
        }).catch((err: any) => {
          logger.warn(`${LOG_PREFIX} Electron native notification failed, falling back to web API`, { error: err });
          // Fall through to web Notification API below
        });
        
        // Ensure IPC listener is set up (in case it wasn't set up at module load)
        setupNotificationClickListener();
        
        return true;
      } else {
        // Still use Electron notification even without onClick
        (window.electronAPI as any).showNotification({
          title: options.title,
          body: options.body,
          tag: options.tag || `notification-${Date.now()}-${Math.random()}`,
          icon: options.icon,
          silent: options.silent ?? !(soundOptions?.enabled ?? true),
        }).catch((err: any) => {
          logger.warn(`${LOG_PREFIX} Electron native notification failed, falling back to web API`, { error: err });
        });
        return true;
      }
    } catch (e) {
      logger.warn(`${LOG_PREFIX} Electron notification failed, falling back to web API`, { error: e });
    }
  }
  
  if (!isNotificationSupported()) {
    logger.warn(`${LOG_PREFIX} Notifications not supported`);
    return false;
  }

  // Double-check permission (may have changed since last check)
  const currentPermission = Notification.permission;
  if (currentPermission !== "granted") {
    logger.debug(`${LOG_PREFIX} Permission not granted: ${currentPermission}`);
    return false;
  }

  try {
    // Determine if we should play the OS sound
    // silent: false = play OS default notification sound
    // silent: true = no sound
    const silent = options.silent ?? !(soundOptions?.enabled ?? true);
    
    // Create the notification with native OS sound
    const notification = new Notification(options.title, {
      body: options.body,
      icon: options.icon || "/icon.png", // Default to app icon
      tag: options.tag,
      silent: silent,
      requireInteraction: true, // Keep visible until user interacts (where supported)
    });
    
    // Log notification errors
    notification.onerror = (err) => {
      logger.error(`${LOG_PREFIX} Notification error:`, err);
    };

    // Handle click - Store reference to prevent garbage collection
    if (options.onClick) {
      const clickHandler = (event: Event) => {
        event.preventDefault();
        event.stopPropagation();
        
        // Close notification first
        try {
          notification.close();
        } catch (e) {
          // Ignore close errors
        }
        
        // Focus the window immediately - CRITICAL for navigation to work
        if (typeof window !== "undefined") {
          window.focus();
          
          // In Electron, we need to use IPC to properly focus
          if (window.electronAPI?.getWindowContext) {
            window.electronAPI.getWindowContext()
              .then((context: any) => {
                if (context?.windowId && window.electronAPI?.focusWindow) {
                  return window.electronAPI.focusWindow(context.windowId);
                }
              })
              .catch(() => {
                // Fallback to window.focus()
                window.focus();
              });
          }
          
          // Multiple focus attempts for reliability
          setTimeout(() => window.focus(), 50);
          setTimeout(() => window.focus(), 150);
        }
        
        // Call the navigation handler IMMEDIATELY - don't delay
        try {
          const result = options.onClick?.();
          // If it returns a promise, await it
          if (result && typeof result.then === 'function') {
            result
              .then(() => {
                // Ensure window is focused after navigation
                if (typeof window !== "undefined") {
                  window.focus();
                }
              })
              .catch((error: any) => {
                logger.error(`${LOG_PREFIX} Error in navigation handler:`, error);
              });
          } else {
            // Ensure window is focused after navigation
            if (typeof window !== "undefined") {
              window.focus();
            }
          }
        } catch (error) {
          logger.error(`${LOG_PREFIX} Exception in navigation handler:`, error);
        }
      };
      
      // In Electron, notification clicks might not work with web API
      // Try multiple approaches to ensure click handler fires
      
      // Method 1: Direct onclick assignment
      notification.onclick = clickHandler;
      
      // Method 2: AddEventListener (some platforms need this)
      try {
        notification.addEventListener('click', clickHandler, { once: false });
      } catch (e) {
        logger.debug(`${LOG_PREFIX} addEventListener not supported:`, e);
      }
      
      // Method 3: Store reference globally to prevent garbage collection
      // Use Map for better performance and easier lookup
      if (typeof window !== "undefined") {
        (window as any).__activeNotifications = (window as any).__activeNotifications || new Map();
        const notificationId = options.tag || `notification-${Date.now()}-${Math.random()}`;
        let onclickFired = false;
        
        // Wrap clickHandler to track if it fired
        const wrappedClickHandler = (event: Event) => {
          if (!onclickFired) {
            onclickFired = true;
            clickHandler(event);
          }
        };
        
        (window as any).__activeNotifications.set(notificationId, {
          notification,
          clickHandler: wrappedClickHandler,
          originalClickHandler: clickHandler,
          tag: options.tag,
          onClick: options.onClick,
          timestamp: Date.now(),
        });
        
        // Update onclick to use wrapped handler
        notification.onclick = wrappedClickHandler;
        
        // CRITICAL: Also set onclick directly on the notification object
        // Some Electron versions need this
        try {
          Object.defineProperty(notification, 'onclick', {
            value: wrappedClickHandler,
            writable: true,
            configurable: true,
          });
        } catch (e) {
          // Ignore if we can't set it
        }
        
        // Clean up old notifications (older than 30 seconds)
        setTimeout(() => {
          const notifications = (window as any).__activeNotifications;
          if (notifications) {
            const now = Date.now();
            for (const [id, notif] of notifications.entries()) {
              if ((notif as any).timestamp && now - (notif as any).timestamp > 30000) {
                notifications.delete(id);
              }
            }
          }
        }, 31000);
        
        // Note: Removed aggressive fallback mechanisms that triggered on any user interaction
        // Navigation should only happen when user actually clicks the notification
        // Electron IPC handler and web Notification onclick should handle actual clicks
      }
    }

    // Handle close
    if (options.onClose) {
      notification.onclose = () => {
        options.onClose?.();
      };
    }

    return true;
  } catch (error) {
    logger.error(`${LOG_PREFIX} Failed to show notification:`, {
      title: options.title,
      error,
    });
    return false;
  }
}

/**
 * Show a workflow completion notification
 * Convenience function with sensible defaults for workflow notifications
 */
export function showWorkflowCompletionNotification(
  chatId: string,
  chatTitle: string,
  onNavigateToChat: (chatId: string) => void,
  soundOptions?: SoundOptions
): boolean {
  return showNotification(
    {
      title: "Workflow Complete",
      body: chatTitle || "A workflow has completed",
      tag: `workflow-${chatId}`, // Replace previous notifications for same chat
      onClick: () => {
        onNavigateToChat(chatId);
      },
    },
    soundOptions
  );
}

/**
 * Show an approval required notification
 * Convenience function for when a tool needs user approval
 */
export function showApprovalRequiredNotification(
  chatId: string,
  chatTitle: string,
  onNavigateToChat: (chatId: string) => void,
  soundOptions?: SoundOptions
): boolean {
  return showNotification(
    {
      title: "Approval Required",
      body: chatTitle ? `${chatTitle} needs your approval` : "A tool needs your approval",
      tag: `approval-${chatId}`, // Replace previous notifications for same chat
      onClick: () => {
        onNavigateToChat(chatId);
      },
    },
    soundOptions
  );
}

/**
 * Show a test notification (for settings page)
 */
export function showTestNotification(soundOptions?: SoundOptions): boolean {
  return showNotification(
    {
      title: "Test Notification",
      body: "Notifications are working correctly!",
      tag: "test-notification",
    },
    soundOptions
  );
}
