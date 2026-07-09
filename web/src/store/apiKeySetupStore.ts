/**
 * API Key Setup Store
 * 
 * Manages the state for the API key setup modal that prompts users
 * to configure an API key on first launch if none are configured.
 */

import { create } from "zustand";
import { api } from "../api/client";
import { logger } from "../lib/logger";
import { useAuthStore } from "./authStore";
import { useModalStore } from "./modalStore";

/** Open the central API-key-setup modal via the unified modal store. */
function openApiKeySetupModal(): void {
  useModalStore.getState().openModal("api-key-setup");
}

/**
 * Close the API-key-setup modal IF it's the active one in the unified store.
 * We intentionally avoid clobbering whatever modal happens to be open (a
 * different one might have been opened during a race).
 */
function closeApiKeySetupModalIfActive(): void {
  const { activeModal, closeModal } = useModalStore.getState();
  if (activeModal === "api-key-setup") {
    closeModal();
  }
}

const DISMISSED_KEY = "reliant.apiKeySetup.dismissed";

const AUTO_MANAGED_PROVIDERS = new Set(["reliant"]);

type ProviderStatus = Awaited<ReturnType<typeof api.settings.getProviders>>[number];

/**
 * Guards the managed-key self-heal to one attempt per session. The
 * control-plane sync RPC is idempotent, but we don't want to fire it on every
 * ensure() call — only when we first observe a signed-in user with no usable
 * provider. Cleared on `reset()` (logout) so the next login re-attempts.
 */
let managedKeySyncAttempted = false;

/**
 * Returns true when the current user is signed into Reliant (has an active
 * session). Such users are eligible for the managed Reliant key, which is
 * provisioned lazily from their JWT — so a missing key here is something we
 * can self-heal rather than a reason to prompt for a manual key.
 */
function isReliantSessionActive(): boolean {
  const { user, session } = useAuthStore.getState();
  return !!(user && session);
}

/**
 * Match onboarding `detectCompletedItems` / checklist: key or OAuth-backed
 * provider. This is backend truth — a live session is NOT treated as
 * credentials, because a signed-in user may not have a synced Reliant key yet
 * (see `loadProviderCredentials`, which self-heals that case).
 */
function hasAnyProviderCredentials(providers: ProviderStatus[]): boolean {
  return providers.some((p) => p.hasApiKey || p.configured);
}

function hasAnyManualProviderCredentials(providers: ProviderStatus[]): boolean {
  return providers.some(
    (p) =>
      !AUTO_MANAGED_PROVIDERS.has((p.provider || "").toLowerCase()) &&
      (p.hasApiKey || p.configured)
  );
}

/**
 * Fetch provider statuses, self-healing the managed Reliant key when the user
 * is signed in but has no usable provider. The setup modal should only appear
 * when the backend genuinely has no credentials — not merely because a synced
 * key hasn't been minted yet. `provisionManagedKey` is idempotent server-side
 * and a no-op in local (no control-plane) mode.
 */
async function loadProviderCredentials(): Promise<{
  providers: ProviderStatus[];
  hasAnyKey: boolean;
  hasAnyManualKey: boolean;
}> {
  let providers = await api.settings.getProviders();

  if (
    !hasAnyProviderCredentials(providers) &&
    isReliantSessionActive() &&
    !managedKeySyncAttempted
  ) {
    managedKeySyncAttempted = true;
    try {
      const { onboardingService } = await import(
        "../services/controlPlane/onboarding"
      );
      const result = await onboardingService.provisionManagedKey();
      if (result.synced) {
        logger.info(
          "[ApiKeySetupStore] Self-healed managed Reliant key; re-checking providers"
        );
        providers = await api.settings.getProviders();
      }
    } catch (error) {
      logger.warn(
        "[ApiKeySetupStore] Managed Reliant key self-heal failed",
        error
      );
    }
  }

  return {
    providers,
    hasAnyKey: hasAnyProviderCredentials(providers),
    hasAnyManualKey: hasAnyManualProviderCredentials(providers),
  };
}

interface ApiKeySetupState {
  /**
   * Legacy visibility flag.
   *
   * Modal visibility now lives in `useModalStore` (Forge Phase 1). This field
   * is retained as a transitional alias: actions on this store still set it
   * for any external consumers (e.g. tests that haven't been migrated yet),
   * but the production `ModalLayer` reads from the modal store.
   */
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
    managedKeySyncAttempted = false;
    set({
      hasChecked: false,
      isChecking: false,
      showModal: false,
      hasApiKey: null,
    });
    closeApiKeySetupModalIfActive();
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
      const { providers, hasAnyKey, hasAnyManualKey } =
        await loadProviderCredentials();

      logger.info("[ApiKeySetupStore] API key check result", {
        hasAnyKey,
        hasAnyManualKey,
        providers: providers.map((p) => ({
          provider: p.provider,
          hasKey: p.hasApiKey
        })),
      });

      const shouldShowModal = !hasAnyKey && !hasAnyManualKey;
      set({
        showModal: shouldShowModal,
        isChecking: false,
        hasChecked: true,
        hasApiKey: hasAnyKey,
      });
      if (shouldShowModal) openApiKeySetupModal();
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
      const { hasAnyKey, hasAnyManualKey } = await loadProviderCredentials();

      logger.info("[ApiKeySetupStore] API key ensure result", { hasAnyKey, hasAnyManualKey });

      const shouldShowModal = !hasAnyKey && !hasAnyManualKey;
      set({
        hasApiKey: hasAnyKey,
        hasChecked: true,
        isChecking: false,
        // Show modal only if neither auto-managed nor manual providers are configured
        showModal: shouldShowModal,
      });
      if (shouldShowModal) openApiKeySetupModal();
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
    closeApiKeySetupModalIfActive();
  },

  /**
   * Manually open the modal (e.g., from settings or help menu)
   */
  openModal: () => {
    set({ showModal: true });
    openApiKeySetupModal();
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