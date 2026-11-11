/**
 * Settings Sync Service Tests
 */

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { SettingsSyncService, SETTINGS_KEYS } from '../settingsSync';
import { fetchSettings } from '../../hooks/settings-queries';
import { logger } from '../../lib/logger';

// Mock apiClient
vi.mock('../../hooks/settings-queries', () => ({
  fetchSettings: vi.fn().mockResolvedValue({ settings: [] }),
  batchUpsertSettings: vi.fn().mockResolvedValue([]),
  deleteSetting: vi.fn().mockResolvedValue({}),
}));

vi.mock('../../lib/logger', () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() },
}));

// Mock localStorage for test environment
const localStorageMock = (() => {
  let store: Record<string, string> = {};
  return {
    getItem: (key: string) => store[key] ?? null,
    setItem: (key: string, value: string) => { store[key] = value; },
    removeItem: (key: string) => { delete store[key]; },
    clear: () => { store = {}; },
    get length() { return Object.keys(store).length; },
    key: (i: number) => Object.keys(store)[i] ?? null,
  };
})();

vi.stubGlobal('localStorage', localStorageMock);

describe('SettingsSyncService', () => {
  let service: SettingsSyncService;
  
  beforeEach(() => {
    // Clear localStorage
    localStorage.clear();
    
    // Create fresh service instance
    service = new SettingsSyncService();
    
    // Reset mocks
    vi.clearAllMocks();
  });

  describe('getSetting', () => {
    it('should return default value when setting does not exist', () => {
      const result = service.getSetting(SETTINGS_KEYS.THEME, 'light');
      expect(result).toBe('light');
    });

    it('should return stored value from localStorage', () => {
      localStorage.setItem(SETTINGS_KEYS.THEME, 'dark');
      const result = service.getSetting(SETTINGS_KEYS.THEME, 'light');
      expect(result).toBe('dark');
    });
  });

  describe('getJSONSetting', () => {
    it('should return default value when setting does not exist', () => {
      const defaultSettings = { minimap: true, lineNumbers: true };
      const result = service.getJSONSetting(SETTINGS_KEYS.EDITOR_SETTINGS, defaultSettings);
      expect(result).toEqual(defaultSettings);
    });

    it('should parse and return JSON from localStorage', () => {
      const settings = { minimap: false, lineNumbers: true };
      localStorage.setItem(SETTINGS_KEYS.EDITOR_SETTINGS, JSON.stringify(settings));
      
      const result = service.getJSONSetting(SETTINGS_KEYS.EDITOR_SETTINGS, {});
      expect(result).toEqual(settings);
    });

    it('should return default on invalid JSON', () => {
      localStorage.setItem(SETTINGS_KEYS.EDITOR_SETTINGS, 'invalid-json');
      const defaultSettings = { minimap: true };
      
      const result = service.getJSONSetting(SETTINGS_KEYS.EDITOR_SETTINGS, defaultSettings);
      expect(result).toEqual(defaultSettings);
    });
  });

  describe('isInitialized', () => {
    it('should return false before initialization', () => {
      expect(service.isInitialized()).toBe(false);
    });
  });

  describe('onInitialized', () => {
    it('runs a listener registered before initialization once it completes, with the settings in place', async () => {
      vi.mocked(fetchSettings).mockResolvedValueOnce({
        settings: [{ key: SETTINGS_KEYS.SHOW_HIDDEN_FILES, value: 'true' }],
      } as never);
      const seen: string[] = [];
      service.onInitialized(() => seen.push(service.getSetting(SETTINGS_KEYS.SHOW_HIDDEN_FILES)));
      expect(seen).toEqual([]);

      await service.initialize();
      expect(seen).toEqual(['true']);

      // Once only: a later initialize() is a no-op and notifies nobody again.
      await service.initialize();
      expect(seen).toEqual(['true']);
    });

    it('runs at once when initialization already completed', async () => {
      await service.initialize();
      const listener = vi.fn();
      service.onInitialized(listener);
      expect(listener).toHaveBeenCalledTimes(1);
    });

    it('does not run an unsubscribed listener', async () => {
      const listener = vi.fn();
      const unsubscribe = service.onInitialized(listener);
      unsubscribe();
      await service.initialize();
      expect(listener).not.toHaveBeenCalled();
    });

    it('still completes initialization when a listener throws', async () => {
      const after = vi.fn();
      service.onInitialized(() => {
        throw new Error('boom');
      });
      service.onInitialized(after);
      await expect(service.initialize()).resolves.toBeUndefined();
      expect(after).toHaveBeenCalledTimes(1);
      expect(logger.error).toHaveBeenCalledWith('[SettingsSync] Initialization listener failed:', expect.any(Error));
    });
  });

  describe('startup log lines', () => {
    it('says how many of the received settings went to localStorage, so the counts agree', async () => {
      vi.mocked(fetchSettings).mockResolvedValueOnce({
        settings: [
          { key: 'onboarding.completed', value: 'true' },
          { key: 'model.default', value: 'x' },
          { key: SETTINGS_KEYS.TOOL_COLLAPSE_DEFAULTS, value: '{}' },
        ],
      } as never);
      await service.initialize();
      const infos = vi.mocked(logger.info).mock.calls.map(([message]) => String(message));
      expect(infos).toContain('[SettingsSync] Received 3 total settings from database');
      expect(infos).toContain(
        '[SettingsSync] ✅ Applied 1 of 3 settings to localStorage (appearance/toolcalls/notifications)',
      );
    });

    it('treats a user with no saved appearance settings as normal, not a warning', async () => {
      await service.initialize();
      service.applyAppearanceSettingsToDOM();
      expect(logger.warn).not.toHaveBeenCalled();
      const infos = vi.mocked(logger.info).mock.calls.map(([message]) => String(message));
      expect(infos).toContain('[SettingsSync] No saved appearance settings; keeping localStorage/default values');
    });
  });

  describe('SETTINGS_KEYS', () => {
    it('should have all required setting keys', () => {
      expect(SETTINGS_KEYS.THEME).toBe('appearance.theme');
      expect(SETTINGS_KEYS.COLOR_SCHEME).toBe('appearance.colorScheme');
      expect(SETTINGS_KEYS.SHOW_HIDDEN_FILES).toBe('appearance.showHiddenFiles');
      expect(SETTINGS_KEYS.FONT).toBe('appearance.font');
      expect(SETTINGS_KEYS.CHAT_FONT).toBe('appearance.chatFont');
      expect(SETTINGS_KEYS.EDITOR_FONT).toBe('appearance.editorFont');
      expect(SETTINGS_KEYS.FONT_SIZE).toBe('appearance.fontSize');
      expect(SETTINGS_KEYS.EDITOR_SETTINGS).toBe('appearance.editorSettings');
    });
  });
});
