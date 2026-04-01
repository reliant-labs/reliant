import { useEffect, useRef } from "react";
import { useAuthStore } from "@/store/authStore";
import { useGlobalDataStore } from "@/store/globalDataStore";
import { useApiKeySetupStore } from "@/store/apiKeySetupStore";
import { logger } from "@/lib/logger";
import { initDaemonTransport } from "@/api/grpc-client";
import { supabase } from "@/lib/supabase";

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

      // Add a delay to ensure session is fully propagated in Electron
      // In Electron, session storage is async via IPC calls, so we need
      // to wait for the session to be fully available before making API calls
      const delay = window.electronAPI ? 500 : 0;

      setTimeout(async () => {
        // Double-check session is available from Supabase before prefetch
        const { data: { session: currentSession } } = await supabase.auth.getSession();
        logger.info('[AuthInitializer] Session check before prefetch:', {
          hasSession: !!currentSession,
          hasAccessToken: !!currentSession?.access_token,
          tokenLength: currentSession?.access_token?.length,
          isElectron: !!window.electronAPI,
        });

        if (!currentSession?.access_token && window.electronAPI) {
          logger.warn('[AuthInitializer] No access token available yet, waiting longer...');
          // Wait a bit more for Electron
          await new Promise(resolve => setTimeout(resolve, 500));
        }

        const start = performance.now();

        // Start daemon transport discovery in parallel with prefetch.
        // getDaemonTransport() will await this promise so FileSystem/Terminal/Background
        // calls wait for the daemon URL rather than falling back to the main server.
        const daemonReady = initDaemonTransport().catch((error) => {
          logger.warn('[AuthInitializer] Daemon transport discovery failed:', error);
        });

        // Trigger prefetch - this loads global data needed for the app
        try {
          await prefetch();
          logger.info('[AuthInitializer] Global data prefetch completed in', (performance.now() - start).toFixed(2), 'ms');
        } catch (error) {
          logger.warn('[AuthInitializer] Global data prefetch failed:', error);
          // Reset prefetch flag if global data fails, as it's critical for app function
          hasPrefetched.current = false;
        }

        // Ensure daemon transport is ready before rendering project UI
        await daemonReady;

        // Independently check API keys
        // Wait for checklist to initialize first to avoid showing modal during welcome
        const waitForChecklist = async () => {
          const { useOnboardingChecklistStore } = await import("../store/onboardingChecklistStore");
          let attempts = 0;
          while (!useOnboardingChecklistStore.getState().isInitialized && attempts < 50) {
            await new Promise(resolve => setTimeout(resolve, 100));
            attempts++;
          }
          // Now check API keys after checklist state is known
          checkApiKeys().catch((error) => {
            logger.warn('[AuthInitializer] API key check failed:', error);
          });
        };
        waitForChecklist();
      }, delay);
    }
  }, [initialized, loading, user, session, prefetch, checkApiKeys]);

  return <>{children}</>;
}
