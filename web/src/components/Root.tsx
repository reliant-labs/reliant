import { useEffect, useState, useRef } from "react";
import { usePrivacyStore } from "../store/privacyStore";
import { initSentry } from "../lib/sentry";
import { settingsSync } from "../services/settingsSync";
import { App } from "./App";
import { isConfigReady, waitForConfig } from "../lib/configReady";
import { logger } from "../lib/logger";
import { LoadingSpinner } from "./Layout/LoadingSpinner";
import { useProjectStore } from "../store/projectStore";

/**
 * Root component that initializes privacy settings before rendering the app.
 * Exported as a separate component for Fast Refresh compatibility.
 *
 * IMPORTANT: This component assumes RELIANT_CONFIG is already available.
 * The config wait is done in main.tsx BEFORE React mounts.
 * However, we still add defensive checks here as a belt-and-suspenders approach.
 */
export function Root() {
  const [isPrivacyInitialized, setIsPrivacyInitialized] = useState(false);
  const initialize = usePrivacyStore((state) => state.initialize);
  const initAttempted = useRef(false);

  useEffect(() => {
    // Prevent double initialization in strict mode
    if (initAttempted.current) return;
    initAttempted.current = true;

    const initializeApp = async () => {
      try {
        // Double-check config is ready (should already be from main.tsx)
        // This is a safety net in case React somehow mounted before config
        if (!isConfigReady()) {
          logger.warn('[Root] Config not ready, waiting (this should not happen)...');
          try {
            await waitForConfig(10000);
          } catch (configErr) {
            logger.error('[Root] Config wait failed:', configErr);
            // Continue anyway - will fail on gRPC calls but user sees the app
          }
        }

        // Initialize privacy settings, settings sync, and load projects in parallel.
        // Loading projects here (instead of in ModernApp) eliminates a ~1s serial gap
        // where the UI was blocked behind loadProjects() before any Wave 2 RPCs fired.
        await Promise.all([
          initialize(),
          settingsSync.initialize(),
          useProjectStore.getState().loadProjects().catch((err) => {
            // Non-fatal: ModernApp init has a safety net fallback
            logger.warn('[Root] loadProjects failed (will retry in ModernApp):', err);
          }),
        ]);

        // CRITICAL: Apply appearance settings to DOM immediately after settingsSync initializes
        // This ensures settings are visible before the app renders, preventing the need to refresh
        logger.info("[Root] Applying appearance settings to DOM...");
        settingsSync.applyAppearanceSettingsToDOM();

        // Initialize Sentry after privacy settings are loaded
        await initSentry();
        setIsPrivacyInitialized(true);
        logger.info('[Root] ✅ App initialization complete');
      } catch (err) {
        const errorMessage = err instanceof Error ? err.message : String(err);
        logger.error('[Root] Failed to initialize:', errorMessage);
        // Initialize Sentry anyway with defaults so we capture errors
        await initSentry();
        // Still show the app - some features may work
        setIsPrivacyInitialized(true);
      }
    };

    initializeApp();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []); // Empty deps - only run once on mount

  // Show loading state while privacy settings are being loaded
  if (!isPrivacyInitialized) {
    return <LoadingSpinner />;
  }

  return <App />;
}