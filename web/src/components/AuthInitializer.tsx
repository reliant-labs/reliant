import { useEffect, useRef } from "react";
import { useAuthStore } from "@/store/authStore";
import { useGlobalDataStore } from "@/store/globalDataStore";
import { useApiKeySetupStore } from "@/store/apiKeySetupStore";
import { usePrivacyStore } from "@/store/privacyStore";
import { useProjectStore } from "@/store/projectStore";
import { logger } from "@/lib/logger";
import { supabase } from "@/lib/supabase";
import { initSentry } from "@/lib/sentry";
import { identifyUser, resetUser } from "@/lib/analytics";
import { settingsSync } from "@/services/settingsSync";
import { api } from "@/api/client";
import { triggerRefetch } from "@/store/refetchStore";

// NOTE: OAuth callback handling is done in authStore.initialize() to avoid duplicate listeners

interface AuthInitializerProps {
  children: React.ReactNode;
}

export function AuthInitializer({ children }: AuthInitializerProps) {
  const initialize = useAuthStore((state) => state.initialize);

  const { user, loading, initialized, session } = useAuthStore();
  const prefetch = useGlobalDataStore((state) => state.prefetch);
  const checkApiKeys = useApiKeySetupStore((state) => state.checkApiKeys);
  const resetApiKeyCheck = useApiKeySetupStore((state) => state.reset);
  const initializePrivacy = usePrivacyStore((state) => state.initialize);
  const hasPrefetched = useRef(false);
  const hasInitializedAuthenticatedStartup = useRef(false);
  // Tracks whether SyncReliantProvider has been called for the current session.
  // Reset on logout so the next login re-hydrates the key.
  const hasSyncedReliantProvider = useRef(false);

  useEffect(() => {
    void initialize();
  }, [initialize]);

  // Reset state when user logs out
  useEffect(() => {
    if (!user) {
      if (hasPrefetched.current) {
        logger.info("[AuthInitializer] User logged out, resetting prefetch and API check state");
        hasPrefetched.current = false;
        resetApiKeyCheck();
      }

      if (hasInitializedAuthenticatedStartup.current) {
        logger.info("[AuthInitializer] User logged out, clearing authenticated startup state");
        hasInitializedAuthenticatedStartup.current = false;
        void resetUser();
      }

      // Reset the reliant-sync gate so a subsequent login re-hydrates the
      // per-user internal API key for that account.
      hasSyncedReliantProvider.current = false;
    }
  }, [user, resetApiKeyCheck]);

  useEffect(() => {
    if (!initialized || loading || !user || hasInitializedAuthenticatedStartup.current) {
      return;
    }

    hasInitializedAuthenticatedStartup.current = true;

    const initializeAuthenticatedStartup = async () => {
      logger.info("[AuthInitializer] Auth ready, initializing authenticated startup RPCs", {
        userId: user.id,
        email: user.email,
        hasSession: !!session,
        hasAccessToken: !!session?.access_token,
      });

      try {
        await Promise.all([
          initializePrivacy(),
          settingsSync.initialize(),
          useProjectStore
            .getState()
            .loadProjects()
            .catch((err) => {
              logger.warn("[AuthInitializer] loadProjects failed (will retry in ModernApp):", err);
            }),
        ]);

        logger.info("[AuthInitializer] Applying appearance settings to DOM...");
        settingsSync.applyAppearanceSettingsToDOM();
      } catch (error) {
        logger.warn("[AuthInitializer] Authenticated startup initialization failed:", error);
      }

      // Hydrate the per-user Reliant provider API key from control-plane.
      // Without this, BuildAvailableDrivers finds no rlnt_ key and chats
      // fall through to "no available provider". Idempotent on the server;
      // guarded by a ref so it only fires once per logged-in session.
      // Failures are non-fatal — other providers can still be used and the
      // user can retry later (e.g. next login).
      if (!hasSyncedReliantProvider.current) {
        hasSyncedReliantProvider.current = true;
        try {
          const result = await api.settings.syncReliantProvider();
          logger.info("[AuthInitializer] SyncReliantProvider completed", {
            success: result.success,
            synced: result.synced,
            createdOrg: result.createdOrg,
            createdKey: result.createdKey,
            rotatedKey: result.rotatedKey,
          });

          if (result.success) {
            // Refresh provider-statuses / config-health consumers so the
            // setup-guide tile and any "no key" banners re-evaluate now that
            // reliant is configured.
            triggerRefetch("config_health");
            try {
              const { useOnboardingChecklistStore } = await import(
                "../store/onboardingChecklistStore"
              );
              void useOnboardingChecklistStore.getState().detectCompletedItems();
            } catch (err) {
              logger.warn(
                "[AuthInitializer] Post-sync checklist refresh failed:",
                err,
              );
            }
          }
        } catch (error) {
          // Non-blocking: log only. The user can still use other providers,
          // and the next login will retry the sync.
          logger.warn("[AuthInitializer] SyncReliantProvider failed:", error);
          // Allow a retry within this session if the failure was transient
          // (e.g. control-plane briefly unreachable).
          hasSyncedReliantProvider.current = false;
        }
      }

      try {
        await initSentry();
      } catch (error) {
        logger.warn("[AuthInitializer] Sentry initialization failed:", error);
      }

      // Identify user for analytics
      void identifyUser(user.id, user.email ?? undefined);
    };

    void initializeAuthenticatedStartup();
  }, [initialized, loading, user, session, initializePrivacy]);

  // Prefetch global data ONCE after auth is ready
  // Using ref to prevent any possibility of double-prefetch
  useEffect(() => {
    if (initialized && !loading && user && !hasPrefetched.current) {
      hasPrefetched.current = true;
      logger.info("[AuthInitializer] Auth ready, triggering global data prefetch...", {
        userId: user.id,
        email: user.email,
        hasSession: !!session,
        hasAccessToken: !!session?.access_token,
      });

      // Session is guaranteed available: this effect only fires when
      // authStore has initialized=true, user!=null, session!=null.
      // authStore.initialize() already awaited supabase.auth.getSession()
      // and onAuthStateChange keeps it in sync (including Electron IPC).
      const delay = window.electronAPI ? 500 : 0;

      setTimeout(async () => {
        // Double-check session is available from Supabase before prefetch
        const {
          data: { session: currentSession },
        } = await supabase.auth.getSession();
        logger.info("[AuthInitializer] Session check before prefetch:", {
          hasSession: !!currentSession,
          hasAccessToken: !!currentSession?.access_token,
          hasAuthStoreSession: !!session,
          hasAuthStoreAccessToken: !!session?.access_token,
          tokenLength: (currentSession?.access_token ?? session?.access_token)?.length,
          isElectron: !!window.electronAPI,
        });

        const start = performance.now();

        try {
          await prefetch();
          logger.info(
            "[AuthInitializer] Global data prefetch completed in",
            (performance.now() - start).toFixed(2),
            "ms"
          );
        } catch (error) {
          logger.warn("[AuthInitializer] Global data prefetch failed:", error);
          hasPrefetched.current = false;
        }

        const waitForChecklist = async () => {
          const [{ useOnboardingChecklistStore }, { useTourStore }] = await Promise.all([
            import("../store/onboardingChecklistStore"),
            import("../store/tourStore"),
          ]);
          void useOnboardingChecklistStore.getState().loadState();
          void useTourStore.getState().loadState();
          let attempts = 0;
          while (!useOnboardingChecklistStore.getState().isInitialized && attempts < 50) {
            await new Promise((resolve) => setTimeout(resolve, 100));
            attempts++;
          }
          checkApiKeys().catch((error) => {
            logger.warn("[AuthInitializer] API key check failed:", error);
          });
        };
        void waitForChecklist();
      }, delay);
    }
  }, [initialized, loading, user, session, prefetch, checkApiKeys]);

  return <>{children}</>;
}