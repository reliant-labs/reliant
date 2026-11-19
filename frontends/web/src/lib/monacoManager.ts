/**
 * Monaco Manager: the one way the app gets the Monaco editor.
 *
 * Monaco is bundled from the `monaco-editor` package (`./monacoBundle`), not
 * fetched from a CDN: its code, CSS, fonts and workers are files of this
 * build, served from our own origin by the web host and by Electron's `app://`
 * handler. The bundle is several megabytes, so it is a chunk of its own,
 * imported here on first use and never by the entry: a page that never opens
 * an editor never downloads it.
 *
 * `getMonaco()` loads that chunk once, hands the instance to
 * `@monaco-editor/loader` (`loader.config({ monaco })`) so that
 * `@monaco-editor/react`'s `<Editor>` uses it instead of injecting the
 * loader's CDN script, and applies the app's theme and CEL language. Every
 * `<Editor>` therefore mounts through `components/shared/MonacoEditor`, which
 * waits for this first: the loader reads its config only on its first
 * `init()`.
 */

import type { Monaco } from '@monaco-editor/react';
import { configureMonacoTheme } from './monacoTheme';
import { registerCELLanguage } from './monaco-cel-language';
import { logger } from './logger';
import * as React from 'react';

// Extend Window interface for debugging
declare global {
  interface Window {
    __monacoManager?: MonacoManager;
  }
}

/** `error` as an Error naming what failed to load. */
function asLoadError(error: unknown): Error {
  const detail = error instanceof Error ? error.message : String(error);
  return new Error(`The Monaco editor chunk did not load: ${detail}`, { cause: error });
}

class MonacoManager {
  private monaco: Monaco | null = null;
  private initPromise: Promise<Monaco> | null = null;
  private initTime: number | null = null;

  /**
   * The Monaco instance, loading it on the first call. A failed load is not
   * cached: the next call tries again (a chunk can fail on a flaky network
   * or across a deploy).
   */
  getMonaco(): Promise<Monaco> {
    if (this.monaco) return Promise.resolve(this.monaco);
    if (!this.initPromise) {
      this.initPromise = this.load().catch((error: unknown) => {
        this.initPromise = null;
        const failure = asLoadError(error);
        logger.error('[MonacoManager] ❌ Failed to initialize Monaco:', failure);
        throw failure;
      });
    }
    return this.initPromise;
  }

  /** The instance if it has already loaded, without starting a load. */
  current(): Monaco | null {
    return this.monaco;
  }

  private async load(): Promise<Monaco> {
    const [{ monaco }, { loader }] = await Promise.all([
      import('./monacoBundle'),
      import('@monaco-editor/react'),
    ]);
    // Before anything calls loader.init(): the loader reads `monaco` only on
    // its first init, and without it injects a CDN script instead.
    loader.config({ monaco });
    await loader.init();

    configureMonacoTheme(monaco);
    registerCELLanguage(monaco);

    this.monaco = monaco;
    this.initTime = Date.now();
    return monaco;
  }

  /**
   * Get current Monaco instance status (for debugging)
   */
  getStatus(): { initialized: boolean; age: number | null } {
    return {
      initialized: !!this.monaco,
      age: this.initTime ? Date.now() - this.initTime : null,
    };
  }
}

// Export singleton instance
export const monacoManager = new MonacoManager();

/**
 * Warm the editor chunk in the background, once the page is idle, so it is
 * ready by the time someone opens a file or the workflow builder. Never on
 * the critical path: it waits for the load event and then for an idle period.
 */
export function prefetchMonaco(): void {
  if (typeof window === 'undefined') return;
  const start = () => {
    const warm = () => {
      monacoManager.getMonaco().catch(() => {
        // Logged by getMonaco; an editor opened later tries again.
      });
    };
    if (typeof window.requestIdleCallback === 'function') {
      window.requestIdleCallback(warm, { timeout: 5_000 });
    } else {
      setTimeout(warm, 1_000);
    }
  };
  if (document.readyState === 'complete') start();
  else window.addEventListener('load', start, { once: true });
}

// Export hook for React components
export function useMonaco(): Monaco | null {
  const [monaco, setMonaco] = React.useState<Monaco | null>(() => monacoManager.current());
  const [error, setError] = React.useState<Error | null>(null);

  React.useEffect(() => {
    if (monaco) return;
    let mounted = true;

    monacoManager.getMonaco()
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
  }, [monaco]);

  if (error) throw error;
  return monaco;
}

// Expose manager on window for debugging
if (typeof window !== 'undefined') {
  window.__monacoManager = monacoManager;
}
