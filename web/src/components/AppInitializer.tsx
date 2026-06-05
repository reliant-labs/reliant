import { useEffect } from 'react';
import { logger } from '../lib/logger';

interface AppInitializerProps {
  onInitialized: () => void;
  children?: React.ReactNode;
}

export function AppInitializer({ onInitialized, children }: AppInitializerProps) {
  useEffect(() => {
    let mounted = true;

    const initializeApp = async () => {
      if (!mounted) return;

      logger.info("🚀 Starting app initialization");

      try {
        // If we're in Electron, wait for the config. Listener-first ordering
        // (see ModernApp.initializeApp) closes the race where preload fires
        // the postMessage between our read and our addEventListener.
        if (typeof window !== "undefined" && window.electronAPI) {
          await new Promise<void>((resolve) => {
            let settled = false;
            const settleWith = (config: typeof window.RELIANT_CONFIG, source: string) => {
              if (settled) return;
              settled = true;
              window.RELIANT_CONFIG = config;
              window.removeEventListener("message", handleMessage);
              logger.info(`[AppInitializer] Config ${source}:`, config?.daemonPort);
              resolve();
            };
            const handleMessage = (event: MessageEvent) => {
              if (
                event.data?.type === 'reliant-config-ready' &&
                event.data?.config?.daemonPort
              ) {
                settleWith(event.data.config, "received via postMessage");
              }
            };
            window.addEventListener("message", handleMessage);

            const current = window.electronAPI?.getConfig();
            if (current?.daemonPort) {
              settleWith(current, "available from electronAPI");
            }
          });
        }

        if (!mounted) return;

        logger.info("[AppInitializer] ✅ Initialization complete");
        
        // Notify parent that initialization is complete
        onInitialized();
      } catch (err) {
        logger.error("Failed to initialize app:", err);
      }
    };

    initializeApp();

    return () => {
      mounted = false;
    };
  }, [onInitialized]);

  return <>{children}</>;
}