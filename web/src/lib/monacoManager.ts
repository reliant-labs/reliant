/**
 * Monaco Manager - Singleton pattern for Monaco Editor
 *
 * PROBLEM: Monaco Editor loads JavaScript workers as blob URLs sequentially when loading chats.
 * Each chat load creates NEW blob URLs even when Monaco is already loaded, causing:
 * - Sequential worker loading (slow)
 * - Blob URL accumulation (memory leak)
 * - Second load SLOWER than first load (accumulation overhead)
 *
 * SOLUTION: Global Monaco singleton with per-workspace caching
 * - Single Monaco instance per workspace
 * - Workers loaded once and reused
 * - Blob URLs properly cleaned up
 * - Parallel worker initialization
 */

import { loader } from '@monaco-editor/react';
import type { Monaco } from '@monaco-editor/react';
import { configureMonacoTheme } from './monacoTheme';
import { registerCELLanguage } from './monaco-cel-language';
import { logger } from './logger';
import * as React from 'react';

interface MonacoInstance {
  monaco: Monaco | null;
  initTime: number;
  workerCount: number;
  blobUrls: Set<string>; // Track blob URLs for cleanup
}

// Extend Window interface for debugging
declare global {
  interface Window {
    __monacoManager?: MonacoManager;
  }
}

interface MonacoConfig {
  paths?: {
    vs?: string;
  };
}

class MonacoManager {
  private static instance: MonacoManager;
  private monacoInstance: MonacoInstance | null = null;
  private initPromise: Promise<Monaco> | null = null;
  private isInitializing = false;
  private workspaceId: string | null = null;

  // Intercept blob URL creation for tracking
  private originalCreateObjectURL: typeof URL.createObjectURL | null = null;
  private originalRevokeObjectURL: typeof URL.revokeObjectURL | null = null;
  private pendingBlobUrls = new Set<string>();

  private constructor() {
    this.setupBlobUrlTracking();
  }

  static getInstance(): MonacoManager {
    if (!MonacoManager.instance) {
      MonacoManager.instance = new MonacoManager();
    }
    return MonacoManager.instance;
  }

  /**
   * Track blob URLs created by Monaco workers so we can clean them up
   */
  private setupBlobUrlTracking(): void {
    if (typeof window === 'undefined') return;

    // Save original methods
    this.originalCreateObjectURL = URL.createObjectURL;
    this.originalRevokeObjectURL = URL.revokeObjectURL;

    // eslint-disable-next-line @typescript-eslint/no-this-alias -- needed for URL method overrides
    const self = this;

    // Wrap createObjectURL to track Monaco worker blobs
    URL.createObjectURL = function(blob: Blob | MediaSource): string {
      const url = self.originalCreateObjectURL!.call(this, blob);

      // Track all JavaScript blobs that might be Monaco workers
      // Note: Workers can be created even after initialization completes
      if (blob instanceof Blob && blob.type === 'application/javascript') {

        // Store in current instance if it exists, or create a set for later
        if (self.monacoInstance) {
          self.monacoInstance.blobUrls.add(url);
        } else if (self.isInitializing) {
          // Instance not created yet but we're initializing - track for transfer later
          self.pendingBlobUrls.add(url);
        }
      }

      return url;
    };

  }

  /**
   * Get or create Monaco instance for the current workspace
   */
  async getMonaco(workspaceId?: string): Promise<Monaco> {
    const currentWorkspaceId = workspaceId || 'default';

    // Return existing instance if same workspace (CACHE HIT)
    if (this.monacoInstance && this.workspaceId === currentWorkspaceId) {
      return this.monacoInstance.monaco;
    }

    // If initializing, wait for it
    if (this.isInitializing && this.initPromise) {
      return this.initPromise;
    }

    // Clean up old instance if switching workspaces (CACHE MISS - workspace change)
    if (this.monacoInstance && this.workspaceId !== currentWorkspaceId) {
      this.cleanup();
    }

    return this.initializeMonaco(currentWorkspaceId);
  }

  /**
   * Initialize Monaco Editor with optimizations
   */
  private async initializeMonaco(workspaceId: string): Promise<Monaco> {
    this.isInitializing = true;
    this.workspaceId = workspaceId;

    this.initPromise = (async () => {
      try {
        // PHASE 1: Configure loader
        const config: MonacoConfig = {
          paths: {
            vs: 'https://cdn.jsdelivr.net/npm/monaco-editor@0.52.2/min/vs',
          },
        };
        loader.config(config);

        // PHASE 2: Load Monaco core + workers
        const loadStart = performance.now();

        // Create instance early so blob tracking can work
        this.monacoInstance = {
          monaco: null, // Will be populated below
          initTime: Date.now(),
          workerCount: 0,
          blobUrls: new Set(),
        };

        const monaco = await loader.init();
        void loadStart; // Used for timing diagnostics if needed

        // PHASE 3: Configure theme
        configureMonacoTheme(monaco);

        // PHASE 4: Register custom languages
        registerCELLanguage(monaco);

        // Update instance with Monaco reference
        this.monacoInstance.monaco = monaco;

        // Transfer any blob URLs captured during the init window
        for (const url of this.pendingBlobUrls) {
          this.monacoInstance.blobUrls.add(url);
        }
        this.pendingBlobUrls.clear();

        return monaco;
      } catch (error) {
        logger.error('[MonacoManager] ❌ Failed to initialize Monaco:', error);
        // Clean up failed instance
        this.monacoInstance = null;
        throw error;
      } finally {
        this.isInitializing = false;
      }
    })();

    return this.initPromise;
  }

  /**
   * Clean up Monaco instance and revoke blob URLs
   */
  cleanup(): void {
    if (!this.monacoInstance) return;

    // Revoke all blob URLs
    if (this.originalRevokeObjectURL) {
      this.monacoInstance.blobUrls.forEach(url => {
        try {
          this.originalRevokeObjectURL!(url);
        } catch {
          // Ignore errors revoking blob URLs - may already be revoked
        }
      });
    }

    this.monacoInstance.blobUrls.clear();
    this.monacoInstance = null;
    this.initPromise = null;
    this.workspaceId = null;
  }

  /**
   * Get current Monaco instance status (for debugging)
   */
  getStatus(): { initialized: boolean; workspaceId: string | null; blobUrls: number; age: number | null } {
    return {
      initialized: !!this.monacoInstance,
      workspaceId: this.workspaceId,
      blobUrls: this.monacoInstance?.blobUrls.size || 0,
      age: this.monacoInstance ? Date.now() - this.monacoInstance.initTime : null,
    };
  }

  /**
   * Force re-initialization (for debugging)
   */
  forceReinit(): void {
    this.cleanup();
  }
}

// Export singleton instance
export const monacoManager = MonacoManager.getInstance();

// Export hook for React components
export function useMonaco(workspaceId?: string): Monaco | null {
  const [monaco, setMonaco] = React.useState<Monaco | null>(null);
  const [error, setError] = React.useState<Error | null>(null);

  React.useEffect(() => {
    let mounted = true;

    monacoManager.getMonaco(workspaceId)
      .then(m => {
        if (mounted) {
          setMonaco(m);
          setError(null);
        }
      })
      .catch(err => {
        if (mounted) {
          setError(err);
          logger.error('[useMonaco] Failed to get Monaco:', err);
        }
      });

    return () => {
      mounted = false;
    };
  }, [workspaceId]);

  if (error) throw error;
  return monaco;
}

// Expose manager on window for debugging
if (typeof window !== 'undefined') {
  window.__monacoManager = monacoManager;
}
