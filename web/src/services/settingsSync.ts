/**
 * Settings Synchronization Service
 * 
 * Synchronizes appearance settings between localStorage and the database.
 * Provides a unified interface for reading and writing settings with automatic
 * persistence to both storage layers.
 */

import type { Setting } from "../api/settings-grpc";
import { api } from "../api/client";
import { waitForConfig } from "../lib/configReady";
import { logger } from "../lib/logger";

// Setting key prefixes for organization
export const SETTINGS_KEYS = {
  // Theme settings
  THEME: "appearance.theme",
  COLOR_SCHEME: "appearance.colorScheme",
  
  // File browser
  SHOW_HIDDEN_FILES: "appearance.showHiddenFiles",
  
  // Fonts
  FONT: "appearance.font",
  CHAT_FONT: "appearance.chatFont",
  EDITOR_FONT: "appearance.editorFont",
  FONT_SIZE: "appearance.fontSize",
  
  // Monaco editor settings (stored as JSON)
  EDITOR_SETTINGS: "appearance.editorSettings",

  // Tool call display settings
  TOOL_COLLAPSE_DEFAULTS: "toolcalls.collapseDefaults",
  
  // Workflow viewer settings
  WORKFLOW_VIEWER_DEFAULT_MODE: "appearance.workflowViewerDefaultMode",
  
  // Spawn display settings
  SPAWN_DISPLAY_MODE: "appearance.spawnDisplayMode",

  // Chat timeline display settings
  CHAT_TIMELINE_VARIANT: "appearance.chatTimelineVariant",
  
  // Notification settings
  NOTIFICATIONS_ENABLED: "notifications.enabled",
  NOTIFICATIONS_SOUND_ENABLED: "notifications.soundEnabled",
  NOTIFICATIONS_WHEN_UNFOCUSED: "notifications.whenUnfocused",
  NOTIFICATIONS_WHEN_DIFFERENT_CHAT: "notifications.whenDifferentChat",
  NOTIFICATIONS_ALWAYS: "notifications.always",
} as const;

export type SettingKey = typeof SETTINGS_KEYS[keyof typeof SETTINGS_KEYS];

/**
 * Settings synchronization service
 */
export class SettingsSyncService {
  private syncInProgress = new Set<string>();
  private initialized = false;
  private settingsAppliedToDOM = false;
  private settingsCache = new Map<string, Setting>();
  private initPromise: Promise<void> | null = null;

  /**
   * Initialize the settings sync service by loading all settings from the database
   */
  async initialize(): Promise<void> {
    if (this.initialized) {
      return;
    }
    if (this.initPromise) {
      return this.initPromise;
    }
    this.initPromise = this.doInitialize();
    return this.initPromise;
  }

  private async doInitialize(): Promise<void> {

    const attemptLoad = async (retryCount = 0): Promise<void> => {
      try {
        // Fetch all appearance settings from the database (via gRPC)
        logger.info('[SettingsSync] Fetching settings from database...');
        const response = await api.settings.listSettings();
        
        const settings = response.settings || [];
        logger.info(`[SettingsSync] Received ${settings.length} total settings from database`);
        
        // Cache ALL settings in memory
        for (const setting of settings) {
          this.settingsCache.set(setting.key, setting);
        }
        logger.info(`[SettingsSync] Cached ${this.settingsCache.size} settings in memory`);

        // Load appearance/toolcalls/notifications settings into localStorage (don't trigger sync back to DB)
        let loadedCount = 0;
        const appearanceSettings: string[] = [];
        for (const setting of settings) {
          if (setting.key.startsWith("appearance.") || setting.key.startsWith("toolcalls.") || setting.key.startsWith("notifications.")) {
            this.syncInProgress.add(setting.key);
            try {
              const oldValue = localStorage.getItem(setting.key);
              localStorage.setItem(setting.key, setting.value);
              loadedCount++;
              if (oldValue !== setting.value) {
                logger.debug(`[SettingsSync] Updated localStorage: ${setting.key} = "${setting.value}" (was: "${oldValue}")`);
              } else {
                logger.debug(`[SettingsSync] Loaded setting: ${setting.key} = "${setting.value}"`);
              }
              if (setting.key.startsWith("appearance.")) {
                appearanceSettings.push(setting.key);
              }
            } catch (error) {
              logger.error(`Failed to load setting ${setting.key} into localStorage:`, error);
            } finally {
              this.syncInProgress.delete(setting.key);
            }
          }
        }

        this.initialized = true;
        logger.info(`[SettingsSync] ✅ Settings loaded successfully: ${loadedCount} settings from database`);
        if (appearanceSettings.length > 0) {
          logger.info(`[SettingsSync] Appearance settings found: ${appearanceSettings.join(', ')}`);
        } else {
          logger.warn(`[SettingsSync] ⚠️ No appearance settings found in database! Will use localStorage values.`);
        }
      } catch (error) {
        const errorMessage = error instanceof Error ? error.message : String(error);
        
        // Retry on network/gRPC errors - these often happen if gRPC isn't ready yet
        const errorString = String(error);
        const isNetworkError = errorMessage.includes('Failed to fetch') || 
                               errorMessage.includes('fetch') ||
                               errorString.includes('Failed to fetch') ||
                               errorMessage.includes('not ready') ||
                               errorMessage.includes('NetworkError');
        
        if (isNetworkError && retryCount < 5) {
          const delay = Math.min(1000 * (retryCount + 1), 3000); // 1s, 2s, 3s, 3s, 3s
          logger.warn(`[SettingsSync] Network/gRPC error (attempt ${retryCount + 1}/5), waiting ${delay}ms and retrying...`, errorMessage);
          try {
            await waitForConfig(5000);
            await new Promise(resolve => setTimeout(resolve, delay));
            return attemptLoad(retryCount + 1);
          } catch (configErr) {
            logger.error('[SettingsSync] Config wait failed:', configErr);
          }
        }
        
        logger.error("[SettingsSync] Failed to initialize after retries:", errorMessage);
        logger.warn("[SettingsSync] Continuing with localStorage-only mode - settings may not persist across restarts");
        // Continue with localStorage-only mode on error
        // This allows the app to work even if database is unavailable
        this.initialized = true;
      }
    };
    
    await attemptLoad();
  }

  /**
   * Set a setting value and sync to both localStorage and database
   */
  async setSetting(key: SettingKey, value: string): Promise<void> {
    // Prevent recursive sync
    if (this.syncInProgress.has(key)) {
      return;
    }

    this.syncInProgress.add(key);

    try {
      // Save to localStorage first (synchronous, immediate)
      localStorage.setItem(key, value);

      // Update in-memory cache
      this.updateCache(key, value);

      // Then sync to database (asynchronous)
      await this.syncToDatabase(key, value);
    } catch (error) {
      console.error(`[SettingsSync] Failed to set setting ${key}:`, error);
      throw error;
    } finally {
      this.syncInProgress.delete(key);
    }
  }

  /**
   * Get a setting value (reads from localStorage)
   */
  getSetting(key: SettingKey, defaultValue: string = ""): string {
    try {
      return localStorage.getItem(key) || defaultValue;
    } catch (error) {
      console.error(`[SettingsSync] Failed to get setting ${key}:`, error);
      return defaultValue;
    }
  }

  /**
   * Set a JSON setting (object/array)
   */
  async setJSONSetting<T>(key: SettingKey, value: T): Promise<void> {
    const jsonString = JSON.stringify(value);
    await this.setSetting(key, jsonString);
  }

  /**
   * Get a JSON setting (object/array)
   */
  getJSONSetting<T>(key: SettingKey, defaultValue: T): T {
    try {
      const value = localStorage.getItem(key);
      if (!value) {
        return defaultValue;
      }
      return JSON.parse(value) as T;
    } catch (error) {
      console.error(`[SettingsSync] Failed to get JSON setting ${key}:`, error);
      return defaultValue;
    }
  }

  /**
   * Delete a setting from both localStorage and database
   */
  async deleteSetting(key: SettingKey): Promise<void> {
    try {
      // Remove from localStorage
      localStorage.removeItem(key);

      // Remove from in-memory cache
      this.removeFromCache(key);

      // Remove from database (via gRPC)
      await api.settings.deleteSetting(key);
    } catch (error) {
      console.error(`[SettingsSync] Failed to delete setting ${key}:`, error);
      throw error;
    }
  }

  /**
   * Sync a setting to the database (via gRPC)
   * Uses optimistic create-first approach to avoid 404 errors on GET
   */
  private async syncToDatabase(key: string, value: string): Promise<void> {
    try {
      // Try to create first (optimistic approach)
      try {
        await api.settings.createSetting(key, value, "string");
        logger.debug(`[SettingsSync] Created setting in database: ${key} = ${value}`);
        return;
      } catch (createError: unknown) {
        // If conflict/duplicate, update instead
        // gRPC errors have a 'code' property
        const errorCode = (createError as { code?: number })?.code;
        // ALREADY_EXISTS = 6, INVALID_ARGUMENT = 3
        if (errorCode === 6 || errorCode === 3) {
          await api.settings.updateSetting(key, value, "string");
          logger.debug(`[SettingsSync] Updated setting in database: ${key} = ${value}`);
        } else {
          throw createError;
        }
      }
    } catch (error) {
      logger.error(`[SettingsSync] ❌ Failed to sync ${key} to database:`, error);
      // Don't throw - localStorage update succeeded, DB sync is best-effort
      // But log it so we can see if settings aren't being persisted
    }
  }

  /**
   * Batch load multiple settings from database (via gRPC)
   */
  async loadSettings(keys: SettingKey[]): Promise<Record<string, string>> {
    const result: Record<string, string> = {};

    try {
      const response = await api.settings.listSettings();
      
      const settings = response.settings || [];
      
      for (const setting of settings) {
        if (keys.includes(setting.key as SettingKey)) {
          result[setting.key] = setting.value;
        }
      }
    } catch (error) {
      console.error("[SettingsSync] Failed to load settings:", error);
    }

    return result;
  }

  /**
   * Check if the service is initialized
   */
  isInitialized(): boolean {
    return this.initialized;
  }

  /**
   * Wait for initialization to complete. Resolves immediately if already initialized.
   */
  async waitForInit(): Promise<void> {
    if (this.initialized) return;
    if (this.initPromise) return this.initPromise;
  }

  /**
   * Get a setting from the in-memory cache (populated by ListSettings on init).
   * Returns null if the key is not in the cache or the cache isn't ready.
   */
  getSettingFromCache(key: string): Setting | null {
    return this.settingsCache.get(key) ?? null;
  }

  /**
   * Update the in-memory cache entry for a setting (called after writes).
   */
  updateCache(key: string, value: string): void {
    const existing = this.settingsCache.get(key);
    if (existing) {
      this.settingsCache.set(key, { ...existing, value, updated_at: new Date().toISOString() });
    } else {
      this.settingsCache.set(key, {
        id: '',
        key,
        value,
        value_type: 'string',
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
      });
    }
  }

  /**
   * Remove a setting from the in-memory cache.
   */
  removeFromCache(key: string): void {
    this.settingsCache.delete(key);
  }

  /**
   * Apply all appearance settings to the DOM immediately
   * This should be called right after initialize() to ensure settings are visible on startup
   */
  applyAppearanceSettingsToDOM(): void {
    if (!this.initialized) {
      logger.warn('[SettingsSync] Cannot apply settings - not initialized yet');
      return;
    }

    // Check if we have any appearance settings in localStorage
    // If initialization failed, we might only have defaults
    const hasTheme = localStorage.getItem(SETTINGS_KEYS.THEME);
    const hasColorScheme = localStorage.getItem(SETTINGS_KEYS.COLOR_SCHEME);
    
    if (!hasTheme && !hasColorScheme) {
      logger.warn('[SettingsSync] No appearance settings found in localStorage - using defaults. Settings may not have loaded from database.');
    }

    const FONT_SIZE_MAP: Record<string, string> = {
      xs: "12px",
      sm: "13px",
      md: "14px",
      lg: "15px",
      xl: "16px",
    };

    const COLOR_SCHEME_MAP: Record<string, string> = {
      blue: "professional-blue",
      neutral: "refined-neutral",
      teal: "modern-teal",
      slate: "slate",
      forest: "forest",
      purple: "purple-classic",
      pink: "vibrant-pink",
      orange: "energetic-orange",
      red: "bold-red",
      black: "pure-black",
    };

    // Apply theme mode (light/dark)
    const theme = this.getSetting(SETTINGS_KEYS.THEME, "");
    logger.info(`[SettingsSync] Applying theme: "${theme}" (from localStorage)`);
    if (theme === "dark") {
      document.documentElement.classList.add("dark");
      logger.debug('[SettingsSync] Added dark class');
    } else if (theme === "light") {
      document.documentElement.classList.remove("dark");
      logger.debug('[SettingsSync] Removed dark class');
    } else {
      logger.warn(`[SettingsSync] No theme found, keeping current state`);
    }

    // Apply color scheme
    const colorScheme = this.getSetting(SETTINGS_KEYS.COLOR_SCHEME, "black");
    const colorSchemeAttr = COLOR_SCHEME_MAP[colorScheme] || "pure-black";
    logger.info(`[SettingsSync] Applying color scheme: "${colorScheme}" -> "${colorSchemeAttr}"`);
    document.documentElement.setAttribute("data-color-scheme", colorSchemeAttr);

    // Apply font settings
    const font = this.getSetting(SETTINGS_KEYS.FONT, "system");
    const chatFont = this.getSetting(SETTINGS_KEYS.CHAT_FONT, "default");
    const editorFont = this.getSetting(SETTINGS_KEYS.EDITOR_FONT, "default");
    const fontSize = this.getSetting(SETTINGS_KEYS.FONT_SIZE, "md");

    logger.info(`[SettingsSync] Applying fonts: font="${font}", chatFont="${chatFont}", editorFont="${editorFont}", fontSize="${fontSize}"`);

    document.documentElement.dataset.font = font;
    document.documentElement.dataset.chatFont = chatFont;
    document.documentElement.dataset.editorFont = editorFont;
    document.documentElement.style.fontSize = FONT_SIZE_MAP[fontSize] || "14px";

    this.settingsAppliedToDOM = true;
    
    // Verify settings were actually applied
    const appliedTheme = document.documentElement.classList.contains('dark') ? 'dark' : 'light';
    const appliedColorScheme = document.documentElement.getAttribute('data-color-scheme');
    const appliedFont = document.documentElement.dataset.font;
    
    logger.info('[SettingsSync] ✅ Appearance settings applied to DOM', {
      theme: appliedTheme,
      colorScheme: appliedColorScheme,
      font: appliedFont,
      fontSize: document.documentElement.style.fontSize
    });
    
    // Double-check: if settings don't match what we tried to apply, log a warning
    if (theme && theme !== appliedTheme) {
      logger.warn(`[SettingsSync] ⚠️ Theme mismatch! Tried to apply "${theme}" but DOM shows "${appliedTheme}"`);
    }
    if (colorSchemeAttr !== appliedColorScheme) {
      logger.warn(`[SettingsSync] ⚠️ Color scheme mismatch! Tried to apply "${colorSchemeAttr}" but DOM shows "${appliedColorScheme}"`);
    }
  }

  /**
   * Check if settings have been applied to the DOM
   */
  hasAppliedSettingsToDOM(): boolean {
    return this.settingsAppliedToDOM;
  }
}

// Export singleton instance
export const settingsSync = new SettingsSyncService();