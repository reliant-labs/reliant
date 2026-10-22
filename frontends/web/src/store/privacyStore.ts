import { create } from 'zustand';
import { api } from '../api/client';
import { waitForConfig } from '../lib/configReady';
import { logger } from '../lib/logger';

interface PrivacySettings {
  crashReportingEnabled: boolean;
  analyticsEnabled: boolean;
}

interface PrivacyStore extends PrivacySettings {
  setCrashReporting: (enabled: boolean) => void;
  setAnalytics: (enabled: boolean) => void;
  initialize: () => Promise<void>;
}

export const usePrivacyStore = create<PrivacyStore>((set, _get) => ({
  crashReportingEnabled: true,
  analyticsEnabled: true,

  initialize: async () => {
    // Load from backend database via gRPC (only source of truth)
    const attemptLoad = async (retryCount = 0): Promise<void> => {
      try {
        const settings = await api.settings.getPrivacySettings();
        set({
          crashReportingEnabled: settings.crash_reporting_enabled,
          analyticsEnabled: settings.analytics_enabled,
        });
        logger.info('[Privacy] Settings loaded successfully');
      } catch (err) {
        const errorMessage = err instanceof Error ? err.message : String(err);
        
        // If gRPC not ready, wait for config and retry
        if (errorMessage.includes('not ready') && retryCount < 2) {
          logger.warn('[Privacy] gRPC not ready, waiting for config and retrying...');
          try {
            await waitForConfig(5000);
            return attemptLoad(retryCount + 1);
          } catch (configErr) {
            logger.error('[Privacy] Config wait failed:', configErr);
          }
        }
        
        logger.error('[Privacy] Failed to load privacy settings:', errorMessage);
        // If backend is down, the app won't work anyway.
        // Keep current state (defaults to enabled on first load).
      }
    };
    
    await attemptLoad();
  },

  setCrashReporting: async (enabled: boolean) => {
    set({ crashReportingEnabled: enabled });
    
    // Save to backend database via gRPC (single source of truth)
    try {
      await api.settings.updatePrivacySettings({
        crash_reporting_enabled: enabled,
      });
    } catch (err) {
      logger.error('[Privacy] Failed to save crash reporting settings:', err);
      // Revert optimistic update on error
      set({ crashReportingEnabled: !enabled });
    }
  },

  setAnalytics: async (enabled: boolean) => {
    set({ analyticsEnabled: enabled });

    // Save to backend database via gRPC (single source of truth)
    try {
      await api.settings.updatePrivacySettings({
        analytics_enabled: enabled,
      });
    } catch (err) {
      logger.error('[Privacy] Failed to save analytics settings:', err);
      // Revert optimistic update on error
      set({ analyticsEnabled: !enabled });
    }
  },
}));

// Export helper to get current state from store
export function getPrivacySettings(): PrivacySettings {
  return usePrivacyStore.getState();
}
