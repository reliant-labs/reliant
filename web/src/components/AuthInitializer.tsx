import { useEffect, useRef } from "react";
import { useAuthStore } from "@/store/authStore";
import { useGlobalDataStore } from "@/store/globalDataStore";
import { useApiKeySetupStore } from "@/store/apiKeySetupStore";
import { usePrivacyStore } from "@/store/privacyStore";
import { useProjectStore } from "@/store/projectStore";
import { api } from "@/api/client";
import { logger } from "@/lib/logger";
import { supabase } from "@/lib/supabase";
import { initSentry } from "@/lib/sentry";
import { settingsSync } from "@/services/settingsSync";

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
      }
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

      try {
        await initSentry();
      } catch (error) {
        logger.warn("[AuthInitializer] Sentry initialization failed:", error);
      }
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

        let accessToken = currentSession?.access_token ?? session?.access_token;
        if (!accessToken && window.electronAPI) {
          logger.warn("[AuthInitializer] No access token available yet, waiting longer...");
          await new Promise((resolve) => setTimeout(resolve, 500));
          const {
            data: { session: retrySession },
          } = await supabase.auth.getSession();
          accessToken = retrySession?.access_token ?? useAuthStore.getState().session?.access_token;
        }

        if (accessToken) {
          try {
            const syncResult = await api.settings.syncReliantProvider();
            logger.info("[AuthInitializer] Reliant provider sync completed", {
              synced: syncResult.synced,
              createdOrg: syncResult.created_org,
              createdKey: syncResult.created_key,
              rotatedKey: syncResult.rotated_key,
            });
          } catch (error) {
            logger.warn("[AuthInitializer] Reliant provider sync failed:", error);
          }
        } else {
          logger.info("[AuthInitializer] Skipping Reliant provider sync without access token");
        }

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
          const { useOnboardingChecklistStore } = await import("../store/onboardingChecklistStore");
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