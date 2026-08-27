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

// NOTE: OAuth callback handling is done in authStore.initialize() to avoid duplicate listeners

/**
 * Poll `supabase.auth.getSession()` until it yields an access token.
 *
 * This exists because "the auth store has a user" and "an outbound RPC will
 * carry a bearer token" are two different facts, separated by a session write.
 * On Electron that write is an IPC round-trip (lib/supabase.ts -> auth:save),
 * so the gap is real and observable; `api/authProvider.ts` reads the session
 * per-request and returns null throughout it.
 *
 * Polling rather than subscribing to onAuthStateChange: the session is already
 * committed in the common case, so the first check usually returns immediately,
 * and a listener would need its own teardown for a wait this short.
 *
 * Returns null if no token appears within the budget. Callers treat that as
 * "try again later", never as a fatal error.
 */
async function waitForAccessToken(timeoutMs = 10_000): Promise<string | null> {
  const startedAt = performance.now();
  let attempt = 0;

  for (;;) {
    const {
      data: { session: currentSession },
    } = await supabase.auth.getSession();

    const token = currentSession?.access_token ?? null;
    if (token) {
      if (attempt > 0) {
        logger.info("[AuthInitializer] Access token available after", {
          waitedMs: Math.round(performance.now() - startedAt),
          attempts: attempt,
        });
      }
      return token;
    }

    if (performance.now() - startedAt >= timeoutMs) return null;

    attempt += 1;
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
}

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

        // The onboarding checklist is per-user, but it mirrors itself to
        // localStorage (`reliant.checklist.*`) so a reload can decide offline
        // whether the guide was already dismissed. That mirror is device-wide
        // and outlives the session, so without this reset the NEXT account to
        // sign in on this device inherits the previous user's progress.
        //
        // `welcomeShown` is the damaging one: apiKeySetupStore treats it as
        // "this user has been introduced to the product" and defers the
        // api-key-setup modal until it is true. Inherited, it reads as true
        // for a brand-new user, so the modal fires immediately — including
        // over `/onboarding`, whose z-40 overlay it outranks at z-50.
        void import("../store/onboardingChecklistStore").then(
          ({ useOnboardingChecklistStore }) => {
            useOnboardingChecklistStore.getState().reset();
          },
        ).catch((error) => {
          logger.warn("[AuthInitializer] Checklist reset failed:", error);
        });
      }

      if (hasInitializedAuthenticatedStartup.current) {
        logger.info("[AuthInitializer] User logged out, clearing authenticated startup state");
        hasInitializedAuthenticatedStartup.current = false;
        void resetUser();
      }
    }
  }, [user, resetApiKeyCheck]);

  useEffect(() => {
    if (!initialized || loading || !user || hasInitializedAuthenticatedStartup.current) {
      return;
    }

    hasInitializedAuthenticatedStartup.current = true;

    const initializeAuthenticatedStartup = async () => {
      // Wait for a token to actually exist before issuing authenticated RPCs.
      //
      // `user` being set is not the same as the session being committed. Under
      // Electron the session is persisted through an IPC round-trip
      // (lib/supabase.ts -> auth:save), so there is a window where the store
      // has a user but supabase.auth.getSession() still returns null. Every RPC
      // fired in that window goes out with NO Authorization header at all and
      // comes back "missing authorization token" — which then trips each
      // caller's retry ladder (settingsSync alone backs off 1s/2s/3s/3s/3s),
      // turning a sub-second gap into tens of seconds of visible stall.
      const token = await waitForAccessToken();
      if (!token) {
        // Don't burn the one-shot guard on a failed attempt: a later session
        // commit should still be able to run startup.
        hasInitializedAuthenticatedStartup.current = false;
        logger.warn(
          "[AuthInitializer] No access token available; deferring authenticated startup",
        );
        return;
      }

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

      // Wait for the token itself rather than sleeping a fixed 500ms on
      // Electron and hoping the IPC session write has landed. That guess was
      // both too long (the session is usually ready immediately) and too short
      // (it wasn't, whenever the main process was busy), and losing the race
      // meant the entire prefetch fan-out went out unauthenticated.
      void (async () => {
        const token = await waitForAccessToken();
        logger.info("[AuthInitializer] Session check before prefetch:", {
          hasAccessToken: !!token,
          hasAuthStoreSession: !!session,
          hasAuthStoreAccessToken: !!session?.access_token,
          tokenLength: token?.length,
          isElectron: !!window.electronAPI,
        });

        if (!token) {
          logger.warn("[AuthInitializer] No access token available; skipping prefetch");
          hasPrefetched.current = false;
          return;
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
          const [{ useOnboardingChecklistStore }, { useTourStore }] = await Promise.all([
            import("../store/onboardingChecklistStore"),
            import("../store/tourStore"),
          ]);
          // Await loadState() directly instead of firing it as `void` and then
          // polling `isInitialized` in a 100ms-tick loop. The previous pattern
          // wasted up to 5s of timers waiting for a promise we already had,
          // and added scheduler noise to the post-login critical path.
          await Promise.all([
            useOnboardingChecklistStore.getState().loadState().catch((error) => {
              logger.warn("[AuthInitializer] Checklist loadState failed:", error);
            }),
            useTourStore.getState().loadState().catch((error) => {
              logger.warn("[AuthInitializer] Tour loadState failed:", error);
            }),
          ]);
          checkApiKeys().catch((error) => {
            logger.warn("[AuthInitializer] API key check failed:", error);
          });
        };
        void waitForChecklist();
      })();
    }
  }, [initialized, loading, user, session, prefetch, checkApiKeys]);

  return <>{children}</>;
}