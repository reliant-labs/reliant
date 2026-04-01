/**
 * API Key Setup Store
 * 
 * Manages the state for the API key setup modal that prompts users
 * to configure an API key on first launch if none are configured.
 */

import { create } from "zustand";
import { api } from "../api/client";
import { logger } from "../lib/logger";

const DISMISSED_KEY = "reliant.apiKeySetup.dismissed";

/** Match onboarding `detectCompletedItems` / checklist: key or OAuth-backed provider. */
function hasAnyProviderCredentials(
  providers: { hasApiKey: boolean; configured: boolean }[],
): boolean {
  return providers.some((p) => p.hasApiKey || p.configured);
}

interface ApiKeySetupState {
  // Whether the setup modal should be shown
  showModal: boolean;
  
  // Whether we're currently checking for API keys
  isChecking: boolean;
  
  // Whether the check has been completed this session
  hasChecked: boolean;
  
  // Cached result of last API key check (true if user has at least one key)
  hasApiKey: boolean | null;
  
  // Actions
  checkApiKeys: () => Promise<void>;
  ensureApiKeyOrShowModal: () => Promise<void>;
  dismissModal: (permanently?: boolean) => void;
  openModal: () => void;
  reset: () => void;
}

export const useApiKeySetupStore = create<ApiKeySetupState>((set, get) => ({
  showModal: false,
  isChecking: false,
  hasChecked: false,
  hasApiKey: null,

  /**
   * Reset the checked state.
   * Called when user logs out so we can check again on next login.
   */
  reset: () => {
    set({
      hasChecked: false,
      isChecking: false,
      showModal: false,
      hasApiKey: null,
    });
    logger.info("[ApiKeySetupStore] Store state reset");
  },

  /**
   * Check if any API keys are configured.
   * If not, and user hasn't permanently dismissed, show the modal.
   */
  checkApiKeys: async () => {
    const state = get();
    
    // Don't check again if already checked this session
    if (state.hasChecked || state.isChecking) {
      logger.info("[ApiKeySetupStore] Already checked or checking, skipping");
      return;
    }

    // Check if onboarding welcome hasn't been shown yet - don't show API key modal during welcome
    const { useOnboardingChecklistStore } = await import("./onboardingChecklistStore");
    const checklistState = useOnboardingChecklistStore.getState();
    
    if (!checklistState.isInitialized) {
      logger.info("[ApiKeySetupStore] Checklist not initialized yet, deferring check");
      return;
    }
    
    if (!checklistState.welcomeShown) {
      logger.info("[ApiKeySetupStore] Welcome not shown yet, deferring modal check");
      return;
    }

    // Check if user has permanently dismissed the modal
    const wasDismissed = localStorage.getItem(DISMISSED_KEY) === "true";
    if (wasDismissed) {
      logger.info("[ApiKeySetupStore] User previously dismissed setup, skipping");
      set({ hasChecked: true });
      return;
    }

    set({ isChecking: true });
    
    try {
      logger.info("[ApiKeySetupStore] Checking for configured API keys...");
      const providers = await api.settings.getProviders();
      const hasAnyKey = hasAnyProviderCredentials(providers);
      
      logger.info("[ApiKeySetupStore] API key check result", {
        hasAnyKey,
        providers: providers.map((p) => ({ 
          provider: p.provider, 
          hasKey: p.hasApiKey 
        })),
      });

      set({
        showModal: !hasAnyKey,
        isChecking: false,
        hasChecked: true,
        hasApiKey: hasAnyKey,
      });
    } catch (error) {
      logger.error("[ApiKeySetupStore] Failed to check API keys:", error);
      set({
        isChecking: false,
        hasChecked: true,
        hasApiKey: null,
        // Don't show modal on error - user might have keys but API call failed
        showModal: false,
      });
    }
  },

  /**
   * Ensure user has an API key, or show the setup modal.
   * This can be called from any screen that requires an API key.
   * Unlike checkApiKeys, this will re-check if we don't have a cached result
   * or if the cached result says no key is configured.
   */
  ensureApiKeyOrShowModal: async () => {
    const state = get();

    // Check if onboarding welcome hasn't been shown yet - don't show modal during welcome
    const { useOnboardingChecklistStore } = await import("./onboardingChecklistStore");
    const checklistState = useOnboardingChecklistStore.getState();
    
    if (!checklistState.isInitialized) {
      logger.info("[ApiKeySetupStore] Checklist not initialized yet, deferring ensure check");
      return;
    }
    
    if (!checklistState.welcomeShown) {
      logger.info("[ApiKeySetupStore] Welcome not shown yet, deferring ensure modal");
      return;
    }

    // Check if user has permanently dismissed the modal
    const wasDismissed = localStorage.getItem(DISMISSED_KEY) === "true";
    if (wasDismissed) {
      logger.info(
        "[ApiKeySetupStore] User permanently dismissed setup, not showing modal"
      );
      return;
    }

    // If already checking, don't start another check
    if (state.isChecking) {
      logger.info("[ApiKeySetupStore] Already checking, skipping ensure call");
      return;
    }

    // If we've already confirmed user has a key, no need to check again
    if (state.hasApiKey === true) {
      logger.info("[ApiKeySetupStore] User already has API key, no modal needed");
      return;
    }

    // If we haven't checked yet, or we know they don't have a key, check again
    // (they might have added one in settings)
    set({ isChecking: true });

    try {
      logger.info("[ApiKeySetupStore] Ensuring API key is configured...");
      const providers = await api.settings.getProviders();
      const hasAnyKey = hasAnyProviderCredentials(providers);

      logger.info("[ApiKeySetupStore] API key ensure result", { hasAnyKey });

      set({
        hasApiKey: hasAnyKey,
        hasChecked: true,
        isChecking: false,
        // Show modal if no key is configured
        showModal: !hasAnyKey,
      });
    } catch (error) {
      logger.error("[ApiKeySetupStore] Failed to ensure API key:", error);
      set({
        isChecking: false,
        // On error, don't show modal (fail gracefully)
      });
    }
  },

  /**
   * Dismiss the modal.
   * @param permanently - If true, won't show again (stored in localStorage)
   */
  dismissModal: (permanently = false) => {
    if (permanently) {
      localStorage.setItem(DISMISSED_KEY, "true");
      logger.info("[ApiKeySetupStore] Modal dismissed permanently");
    } else {
      logger.info("[ApiKeySetupStore] Modal dismissed for this session");
    }
    set({ showModal: false });
  },

  /**
   * Manually open the modal (e.g., from settings or help menu)
   */
  openModal: () => {
    set({ showModal: true });
  },
}));

/**
 * Reset the dismissed state (useful for testing or if user wants to see it again)
 */
export function resetApiKeySetupDismissed(): void {
  localStorage.removeItem(DISMISSED_KEY);
  useApiKeySetupStore.setState({ hasChecked: false, showModal: false });
  logger.info("[ApiKeySetupStore] Dismissed state reset");
}
