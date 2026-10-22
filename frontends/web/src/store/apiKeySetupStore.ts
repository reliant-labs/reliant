/**
 * API Key Setup Store
 * 
 * Manages the state for the API key setup modal that prompts users
 * to configure an API key on first launch if none are configured.
 */

import { create } from "zustand";
import { api } from "../api/client";
import { logger } from "../lib/logger";
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
 * Match onboarding `detectCompletedItems` / checklist: key or OAuth-backed
 * provider. This is backend truth — a live session is NOT treated as
 * credentials. Being signed in says nothing about whether the user has paid
 * for a managed key, and treating it as a credential is what previously let a
 * new account appear to have the Reliant provider configured.
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
 * Fetch provider statuses. Reports exactly what the backend says — nothing
 * here provisions anything.
 *
 * ── Why there is deliberately NO managed-key self-heal ──
 *
 * This used to mint the managed Reliant key whenever the caller was signed in
 * and had no provider credentials, on the theory that a missing key was a sync
 * gap rather than a real absence. Both of those conditions are true of a
 * brand-new user standing on the onboarding model step, and `checkApiKeys` runs
 * from `AuthInitializer` on every sign-in — so signing up silently enabled the
 * Reliant provider for someone who had not paid for it. They then hit the LLM
 * proxy's wallet gate on their first message and were told a quota was
 * exhausted, for a provider they never finished choosing.
 *
 * A managed key is a PURCHASED entitlement now, so granting one belongs at a
 * commit point — an explicit user action, or a webhook confirming money moved.
 * `commitLaunchPlan`'s `grantAiAccess` is that place: it is guarded on the user
 * having actually chosen `reliant_credits` and fires only from a terminal
 * onboarding step. This function was a second, unguarded path to the same
 * grant, which is precisely the pattern `commitLaunchPlan`'s header forbids
 * ("never from a `useEffect` observing a state change, and never as a side
 * effect of preparing an option the user has not yet taken").
 *
 * The behaviour it bought — don't prompt a user whose key hasn't minted yet —
 * no longer describes anything real: minting requires payment, so a user with
 * no credentials genuinely has none, and the setup modal is the correct answer.
 *
 * Users who legitimately want a managed key reach it through explicit UI:
 * `CombinedGeneralSettings`' enable-Reliant button, the mobile providers panel,
 * or a coupon redemption in `RedeemCouponForm`.
 */
async function loadProviderCredentials(): Promise<{
  providers: ProviderStatus[];
  hasAnyKey: boolean;
  hasAnyManualKey: boolean;
}> {
  const providers = await api.settings.getProviders();

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