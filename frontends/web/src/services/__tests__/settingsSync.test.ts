/**
 * Settings Sync Service Tests
 */

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { SettingsSyncService, SETTINGS_KEYS } from '../settingsSync';

// Mock apiClient
vi.mock('../../api/client', () => ({
  apiClient: {
    get: vi.fn(),
    post: vi.fn(),
    put: vi.fn(),
    delete: vi.fn(),
  },
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
