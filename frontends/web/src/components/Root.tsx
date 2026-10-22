import { useEffect, useRef, useState } from "react";
import { App } from "./App";
import { isConfigReady, waitForConfig } from "../lib/configReady";
import { logger } from "../lib/logger";
import { LoadingSpinner } from "./Layout/LoadingSpinner";

/**
 * Root component that waits for runtime configuration before rendering the app.
 *
 * IMPORTANT: Authenticated startup work is handled in AuthInitializer after auth
 * hydration completes. Root must stay unauthenticated so login/signup screens can
 * render without issuing authenticated settings/privacy/project RPCs.
 */
export function Root() {
  const [isAppReady, setIsAppReady] = useState(false);
  const initAttempted = useRef(false);

  useEffect(() => {
    // Prevent double initialization in strict mode
    if (initAttempted.current) return;
    initAttempted.current = true;

    const initializeApp = async () => {
      try {
        // Double-check config is ready (should already be from main.tsx)
        // This is a safety net in case React somehow mounted before config.
        if (!isConfigReady()) {
          logger.warn("[Root] Config not ready, waiting (this should not happen)...");
          try {
            await waitForConfig(10000);
          } catch (configErr) {
            logger.error("[Root] Config wait failed:", configErr);
            // Continue anyway so the app can still render.
          }
        }

        logger.info("[Root] ✅ Base app initialization complete");
      } catch (err) {
        const errorMessage = err instanceof Error ? err.message : String(err);
        logger.error("[Root] Failed to initialize:", errorMessage);
      } finally {
        setIsAppReady(true);
      }
    };

    void initializeApp();
  }, []);

  if (!isAppReady) {
    return <LoadingSpinner />;
  }

  return <App />;
}