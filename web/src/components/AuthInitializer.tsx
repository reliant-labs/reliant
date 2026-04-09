import { useEffect, useRef } from "react";
import { useAuthStore } from "@/store/authStore";
import { useGlobalDataStore } from "@/store/globalDataStore";
import { useApiKeySetupStore } from "@/store/apiKeySetupStore";
import { api } from "@/api/client";
import { logger } from "@/lib/logger";

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
  const hasPrefetched = useRef(false);

  useEffect(() => {
    initialize();
  }, [initialize]);

  // Reset state when user logs out
  useEffect(() => {
    if (!user && hasPrefetched.current) {
      logger.info('[AuthInitializer] User logged out, resetting prefetch and API check state');
      hasPrefetched.current = false;
      resetApiKeyCheck();
    }
  }, [user, resetApiKeyCheck]);

  // Prefetch global data ONCE after auth is ready
  // Using ref to prevent any possibility of double-prefetch
  useEffect(() => {
    if (initialized && !loading && user && !hasPrefetched.current) {
      hasPrefetched.current = true;
      logger.info('[AuthInitializer] Auth ready, triggering global data prefetch...', {
        userId: user.id,
        email: user.email,
        hasSession: !!session,
        hasAccessToken: !!session?.access_token,
      });

      // Session is guaranteed available: this effect only fires when
      // authStore has initialized=true, user!=null, session!=null.
      // authStore.initialize() already awaited supabase.auth.getSession()
      // and onAuthStateChange keeps it in sync (including Electron IPC).

      const start = performance.now();

      (async () => {
        try {
          const syncResult = await api.settings.syncReliantProvider();
          logger.info('[AuthInitializer] Reliant provider sync completed', {
            synced: syncResult.synced,
            createdOrg: syncResult.created_org,
            createdKey: syncResult.created_key,
            rotatedKey: syncResult.rotated_key,
          });
        } catch (error) {
          logger.warn('[AuthInitializer] Reliant provider sync failed:', error);
        }

        // Trigger prefetch - this loads global data needed for the app
        prefetch()
          .then(() => {
            logger.info('[AuthInitializer] Global data prefetch completed in', (performance.now() - start).toFixed(2), 'ms');
          })
          .catch((error) => {
            logger.warn('[AuthInitializer] Global data prefetch failed:', error);
            // Reset prefetch flag if global data fails, as it's critical for app function
            hasPrefetched.current = false;
          });
      })();

      // Independently check API keys
      // Wait for checklist to initialize first to avoid showing modal during welcome
      (async () => {
        const { useOnboardingChecklistStore } = await import("../store/onboardingChecklistStore");
        let attempts = 0;
        while (!useOnboardingChecklistStore.getState().isInitialized && attempts < 50) {
          await new Promise(resolve => setTimeout(resolve, 100));
          attempts++;
        }
        checkApiKeys().catch((error) => {
          logger.warn('[AuthInitializer] API key check failed:', error);
        });
      })();
    }
  }, [initialized, loading, user, session, prefetch, checkApiKeys]);

  return <>{children}</>;
}