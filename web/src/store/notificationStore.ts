/**
 * Notification Settings Store
 * 
 * Manages notification preferences with persistence via settingsSync.
 * Uses native OS notification sounds for a native feel.
 */

import { create } from "zustand";
import { settingsSync, SETTINGS_KEYS } from "../services/settingsSync";
import {
  getNotificationPermission,
  requestNotificationPermission,
  isNotificationSupported,
  isWindowFocused,
  type NotificationPermission,
} from "../lib/notifications";
import { logger } from "../lib/logger";

const LOG_PREFIX = "[NotificationStore]";

interface NotificationState {
  // Settings
  notificationsEnabled: boolean;
  soundEnabled: boolean;
  notifyWhenUnfocused: boolean;      // Notify when app window is not focused
  notifyWhenDifferentChat: boolean;  // Notify when viewing a different chat
  notifyAlways: boolean;             // Always notify, even when viewing the active chat
  
  // Permission status
  permission: NotificationPermission;
  isSupported: boolean;
  
  // Initialization
  initialized: boolean;
  
  // Actions
  initialize: () => Promise<void>;
  setNotificationsEnabled: (enabled: boolean) => Promise<void>;
  setSoundEnabled: (enabled: boolean) => Promise<void>;
  setNotifyWhenUnfocused: (enabled: boolean) => Promise<void>;
  setNotifyWhenDifferentChat: (enabled: boolean) => Promise<void>;
  setNotifyAlways: (enabled: boolean) => Promise<void>;
  requestPermission: () => Promise<NotificationPermission>;
  refreshPermission: () => void;
}

// Default values
const DEFAULT_NOTIFICATIONS_ENABLED = true;
const DEFAULT_SOUND_ENABLED = true;
const DEFAULT_NOTIFY_WHEN_UNFOCUSED = true;
const DEFAULT_NOTIFY_WHEN_DIFFERENT_CHAT = true;
const DEFAULT_NOTIFY_ALWAYS = false;

export const useNotificationStore = create<NotificationState>((set, get) => ({
  // Initial state
  notificationsEnabled: DEFAULT_NOTIFICATIONS_ENABLED,
  soundEnabled: DEFAULT_SOUND_ENABLED,
  notifyWhenUnfocused: DEFAULT_NOTIFY_WHEN_UNFOCUSED,
  notifyWhenDifferentChat: DEFAULT_NOTIFY_WHEN_DIFFERENT_CHAT,
  notifyAlways: DEFAULT_NOTIFY_ALWAYS,
  permission: "default",
  isSupported: isNotificationSupported(),
  initialized: false,

  initialize: async () => {
    if (get().initialized) {
      return;
    }

    try {
      // Load settings from localStorage (settingsSync already loaded from DB)
      const enabledStr = settingsSync.getSetting(
        SETTINGS_KEYS.NOTIFICATIONS_ENABLED,
        String(DEFAULT_NOTIFICATIONS_ENABLED)
      );
      const soundEnabledStr = settingsSync.getSetting(
        SETTINGS_KEYS.NOTIFICATIONS_SOUND_ENABLED,
        String(DEFAULT_SOUND_ENABLED)
      );
      const whenUnfocusedStr = settingsSync.getSetting(
        SETTINGS_KEYS.NOTIFICATIONS_WHEN_UNFOCUSED,
        String(DEFAULT_NOTIFY_WHEN_UNFOCUSED)
      );
      const whenDifferentChatStr = settingsSync.getSetting(
        SETTINGS_KEYS.NOTIFICATIONS_WHEN_DIFFERENT_CHAT,
        String(DEFAULT_NOTIFY_WHEN_DIFFERENT_CHAT)
      );
      const alwaysStr = settingsSync.getSetting(
        SETTINGS_KEYS.NOTIFICATIONS_ALWAYS,
        String(DEFAULT_NOTIFY_ALWAYS)
      );

      const notificationsEnabled = enabledStr === "true";
      const soundEnabled = soundEnabledStr === "true";
      const notifyWhenUnfocused = whenUnfocusedStr === "true";
      const notifyWhenDifferentChat = whenDifferentChatStr === "true";
      const notifyAlways = alwaysStr === "true";

      // Get current permission status
      const permission = getNotificationPermission();

      set({
        notificationsEnabled,
        soundEnabled,
        notifyWhenUnfocused,
        notifyWhenDifferentChat,
        notifyAlways,
        permission,
        initialized: true,
      });

      logger.info(`${LOG_PREFIX} Initialized`, {
        notificationsEnabled,
        soundEnabled,
        notifyWhenUnfocused,
        notifyWhenDifferentChat,
        notifyAlways,
        permission,
      });
    } catch (error) {
      logger.error(`${LOG_PREFIX} Failed to initialize:`, error);
      set({ initialized: true });
    }
  },

  setNotificationsEnabled: async (enabled: boolean) => {
    set({ notificationsEnabled: enabled });
    
    try {
      await settingsSync.setSetting(
        SETTINGS_KEYS.NOTIFICATIONS_ENABLED,
        String(enabled)
      );
      logger.debug(`${LOG_PREFIX} Notifications enabled: ${enabled}`);
    } catch (error) {
      logger.error(`${LOG_PREFIX} Failed to save notifications enabled:`, error);
    }
  },

  setSoundEnabled: async (enabled: boolean) => {
    set({ soundEnabled: enabled });
    
    try {
      await settingsSync.setSetting(
        SETTINGS_KEYS.NOTIFICATIONS_SOUND_ENABLED,
        String(enabled)
      );
      logger.debug(`${LOG_PREFIX} Sound enabled: ${enabled}`);
    } catch (error) {
      logger.error(`${LOG_PREFIX} Failed to save sound enabled:`, error);
    }
  },

  setNotifyWhenUnfocused: async (enabled: boolean) => {
    set({ notifyWhenUnfocused: enabled });
    
    try {
      await settingsSync.setSetting(
        SETTINGS_KEYS.NOTIFICATIONS_WHEN_UNFOCUSED,
        String(enabled)
      );
      logger.debug(`${LOG_PREFIX} Notify when unfocused: ${enabled}`);
    } catch (error) {
      logger.error(`${LOG_PREFIX} Failed to save notify when unfocused:`, error);
    }
  },

  setNotifyWhenDifferentChat: async (enabled: boolean) => {
    set({ notifyWhenDifferentChat: enabled });
    
    try {
      await settingsSync.setSetting(
        SETTINGS_KEYS.NOTIFICATIONS_WHEN_DIFFERENT_CHAT,
        String(enabled)
      );
      logger.debug(`${LOG_PREFIX} Notify when different chat: ${enabled}`);
    } catch (error) {
      logger.error(`${LOG_PREFIX} Failed to save notify when different chat:`, error);
    }
  },

  setNotifyAlways: async (enabled: boolean) => {
    set({ notifyAlways: enabled });
    
    try {
      await settingsSync.setSetting(
        SETTINGS_KEYS.NOTIFICATIONS_ALWAYS,
        String(enabled)
      );
      logger.debug(`${LOG_PREFIX} Notify always: ${enabled}`);
    } catch (error) {
      logger.error(`${LOG_PREFIX} Failed to save notify always:`, error);
    }
  },

  requestPermission: async () => {
    const permission = await requestNotificationPermission();
    set({ permission });
    return permission;
  },

  refreshPermission: () => {
    const permission = getNotificationPermission();
    const currentPermission = get().permission;
    if (permission !== currentPermission) {
      logger.debug(`${LOG_PREFIX} Permission refreshed: ${currentPermission} -> ${permission}`);
    }
    set({ permission });
  },
}));

// Global permission refresh interval (runs every 10 seconds)
// This ensures permission changes in system settings are detected
let permissionRefreshInterval: NodeJS.Timeout | null = null;

/**
 * Start global permission refresh interval
 * Call this when the app initializes
 */
export function startPermissionRefresh() {
  if (permissionRefreshInterval) {
    return; // Already started
  }
  
  permissionRefreshInterval = setInterval(() => {
    const store = useNotificationStore.getState();
    if (store.initialized) {
      store.refreshPermission();
    }
  }, 10000); // Refresh every 10 seconds
  
  logger.debug(`${LOG_PREFIX} Started global permission refresh interval`);
}

/**
 * Stop global permission refresh interval
 */
export function stopPermissionRefresh() {
  if (permissionRefreshInterval) {
    clearInterval(permissionRefreshInterval);
    permissionRefreshInterval = null;
    logger.debug(`${LOG_PREFIX} Stopped global permission refresh interval`);
  }
}

/**
 * Helper to get sound options for notifications
 */
export function getNotificationSoundOptions() {
  const state = useNotificationStore.getState();
  return {
    enabled: state.soundEnabled,
  };
}

/**
 * Check if notifications are enabled and permitted
 * Ensures store is initialized before checking
 */
export async function areNotificationsEnabled(): Promise<boolean> {
  const state = useNotificationStore.getState();
  
  // Ensure store is initialized
  if (!state.initialized) {
    logger.debug(`${LOG_PREFIX} Store not initialized, initializing now`);
    await state.initialize();
  }
  
  // Refresh permission to ensure it's up to date
  const currentPermission = getNotificationPermission();
  if (currentPermission !== state.permission) {
    logger.debug(`${LOG_PREFIX} Permission changed, updating: ${state.permission} -> ${currentPermission}`);
    useNotificationStore.getState().refreshPermission();
  }
  
  const updatedState = useNotificationStore.getState();
  return (
    updatedState.isSupported &&
    updatedState.notificationsEnabled &&
    updatedState.permission === "granted"
  );
}

/**
 * Synchronous version for cases where we can't await
 * More reliable - initializes store if needed and checks permission
 */
export function areNotificationsEnabledSync(): boolean {
  const state = useNotificationStore.getState();
  
  // If not initialized, try to initialize synchronously (best effort)
  if (!state.initialized) {
    // Try to initialize, but don't await (fire and forget)
    state.initialize().catch((err) => {
      logger.error(`${LOG_PREFIX} Failed to initialize store:`, err);
    });
    // Use current state (might have defaults) - but check permission directly
    const currentPermission = getNotificationPermission();
    return (
      state.isSupported &&
      state.notificationsEnabled &&
      currentPermission === "granted"
    );
  }
  
  // Always refresh permission to ensure it's up to date (this is synchronous)
  const currentPermission = getNotificationPermission();
  if (currentPermission !== state.permission) {
    useNotificationStore.getState().refreshPermission();
  }
  
  // Get fresh state after potential permission update
  const updatedState = useNotificationStore.getState();
  
  // Check if notifications are enabled
  return (
    updatedState.isSupported &&
    updatedState.notificationsEnabled &&
    updatedState.permission === "granted"
  );
}

/**
 * Determine if we should show a notification based on current context
 * Ensures store is initialized before checking
 */
export async function shouldShowNotification(isViewingThisChat: boolean): Promise<boolean> {
  const state = useNotificationStore.getState();
  
  // Ensure store is initialized
  if (!state.initialized) {
    logger.debug(`${LOG_PREFIX} Store not initialized, initializing now`);
    await state.initialize();
  }
  
  // Check if notifications are enabled
  const enabled = await areNotificationsEnabled();
  if (!enabled) {
    return false;
  }
  
  const updatedState = useNotificationStore.getState();
  
  // Always notify if this setting is enabled
  if (updatedState.notifyAlways) {
    return true;
  }
  
  const isWindowInBackground = !isWindowFocused();
  
  // Check conditions based on user preferences
  if (isWindowInBackground && updatedState.notifyWhenUnfocused) {
    return true;
  }
  
  if (!isViewingThisChat && updatedState.notifyWhenDifferentChat) {
    return true;
  }
  
  return false;
}

/**
 * Synchronous version for cases where we can't await
 * More reliable - initializes store if needed
 */
export function shouldShowNotificationSync(isViewingThisChat: boolean): boolean {
  // Check if notifications are enabled (sync version)
  const enabled = areNotificationsEnabledSync();
  if (!enabled) {
    return false;
  }
  
  const currentState = useNotificationStore.getState();
  
  // Always notify if this setting is enabled
  if (currentState.notifyAlways) {
    return true;
  }
  
  const isWindowInBackground = !isWindowFocused();
  
  // Check conditions based on user preferences
  if (isWindowInBackground && currentState.notifyWhenUnfocused) {
    return true;
  }
  
  if (!isViewingThisChat && currentState.notifyWhenDifferentChat) {
    return true;
  }
  
  return false;
}
