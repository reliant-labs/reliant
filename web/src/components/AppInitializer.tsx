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
        // If we're in Electron, wait for the config
        if (typeof window !== "undefined" && window.electronAPI) {
          // Get config from electronAPI (exposed by preload via contextBridge)
          const config = window.electronAPI.getConfig();

          if (config?.daemonPort) {
            // Config already available from preload
            window.RELIANT_CONFIG = config;
            logger.info("[AppInitializer] Config available from electronAPI:", config.daemonPort);
          } else {
            // Wait for postMessage from preload when config becomes ready
            await new Promise<void>((resolve) => {
              const handleMessage = (event: MessageEvent) => {
                if (event.data?.type === 'reliant-config-ready' && event.data?.config) {
                  const config = event.data.config;
                  if (config.daemonPort) {
                    window.RELIANT_CONFIG = config;
                    logger.info("[AppInitializer] Config received via postMessage:", config.daemonPort);
                    window.removeEventListener("message", handleMessage);
                    resolve();
                  }
                }
              };
              window.addEventListener("message", handleMessage);
            });
          }
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