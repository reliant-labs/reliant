/**
 * Electron production hardening.
 *
 * This hook has nothing to do with app shortcuts — those live in
 * useAppKeyboardShortcuts and are driven by config/shortcuts.yaml. This is only
 * the set of browser/Chromium affordances we suppress in packaged builds so an
 * installed desktop app does not behave like a web page (reload, view-source,
 * DevTools).
 *
 * It applies ONLY to packaged Electron builds. In dev, and in the browser, none
 * of this runs — a web page has no business blocking a user's DevTools.
 */

import { useEffect } from "react";
import { logger } from "../lib/logger";

interface KeyboardShortcutsConfig {
  disableDevTools?: boolean;
}

/** Chords suppressed in packaged desktop builds, matched case-insensitively. */
const BLOCKED = [
  { ctrlOrMeta: true, key: "r", label: "Page refresh" },
  { key: "F5", label: "Page refresh" },
  { key: "F12", label: "DevTools" },
  { ctrl: true, shift: true, key: "i", label: "DevTools" },
  { meta: true, alt: true, key: "i", label: "DevTools" },
  { ctrl: true, shift: true, key: "c", label: "DevTools" },
  { meta: true, alt: true, key: "c", label: "DevTools" },
  { ctrl: true, shift: true, key: "j", label: "Console" },
  { meta: true, alt: true, key: "j", label: "Console" },
  { ctrlOrMeta: true, key: "u", label: "View source" },
  { ctrlOrMeta: true, key: "s", label: "Save page" },
];

export function useKeyboardShortcuts(config: KeyboardShortcutsConfig = {}) {
  const { disableDevTools = true } = config;

  useEffect(() => {
    const isElectronProd =
      window.RELIANT_CONFIG?.isElectron && !window.RELIANT_CONFIG?.isDev;
    if (!isElectronProd || !disableDevTools) return;

    const preventDevActions = (e: KeyboardEvent) => {
      // The terminal owns its own keys, including Ctrl+R for reverse search.
      const target = e.target as HTMLElement | null;
      if (target?.closest(".xterm")) return;

      const key = e.key.toLowerCase();
      for (const rule of BLOCKED) {
        if (rule.key.toLowerCase() !== key) continue;
        if (rule.ctrlOrMeta && !(e.ctrlKey || e.metaKey)) continue;
        if (rule.ctrl && !e.ctrlKey) continue;
        if (rule.meta && !e.metaKey) continue;
        if (rule.shift && !e.shiftKey) continue;
        if (rule.alt && !e.altKey) continue;

        e.preventDefault();
        logger.info(`🚫 ${rule.label} disabled in production`);
        return;
      }
    };

    document.addEventListener("keydown", preventDevActions);
    return () => document.removeEventListener("keydown", preventDevActions);
  }, [disableDevTools]);

  useEffect(() => {
    const isElectronProd =
      window.RELIANT_CONFIG?.isElectron && !window.RELIANT_CONFIG?.isDev;
    if (!isElectronProd) return;

    const noop = () => {};
    const original = {
      log: window.console.log,
      warn: window.console.warn,
      error: window.console.error,
      info: window.console.info,
      debug: window.console.debug,
      trace: window.console.trace,
    };

    window.console.log = noop;
    window.console.warn = noop;
    window.console.error = noop;
    window.console.info = noop;
    window.console.debug = noop;
    window.console.trace = noop;

    return () => {
      window.console.log = original.log;
      window.console.warn = original.warn;
      window.console.error = original.error;
      window.console.info = original.info;
      window.console.debug = original.debug;
      window.console.trace = original.trace;
    };
  }, []);
}

export { useAppKeyboardShortcuts } from "./useAppKeyboardShortcuts";
export type { ShortcutHandlers } from "./useAppKeyboardShortcuts";
