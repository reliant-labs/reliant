// =============================================================================
// Keyboard Shortcuts Store
// =============================================================================
//
// This store manages keyboard shortcuts state. The default shortcuts are
// generated from the source of truth at: config/shortcuts.yaml
//
// To modify shortcuts, edit config/shortcuts.yaml and run: make generate-shortcuts
//
// Bindings are authored and persisted as STRINGS ("Cmd+Shift+P", "Cmd+K C").
// The platform fold (Cmd -> Meta on macOS, Control elsewhere) happens here at
// load time via parseBinding(), so one stored value works on every platform and
// a user's remap follows them between machines.
//
// =============================================================================

import { create } from "zustand";
import { api } from "../api/client";
import { logger } from "../lib/logger";
import { parseBinding } from "../lib/keyboard/chord";
import { detectPlatform } from "../lib/keyboard/platform";
import { getReservation, type Reservation } from "../lib/keyboard/reserved";
import { ShortcutRegistry, type ResolvedShortcut } from "../lib/keyboard/registry";
import { defaultShortcuts } from "./shortcutsData.generated";

export interface ShortcutDefinition {
  id: string;
  name: string;
  description: string;
  category: string;
  /** Authored desktop binding, e.g. "Cmd+Shift+P". */
  defaultBinding: string;
  /** Authored browser binding; equals defaultBinding when no override. */
  defaultWebBinding: string;
  /** User override, or null when using the default. */
  currentBinding: string | null;
  context: string;
  handler: string;
  allowInInput?: boolean;
  passthrough?: boolean;
}

interface ShortcutsState {
  shortcuts: Record<string, ShortcutDefinition>;
  registry: ShortcutRegistry;
  isEditing: string | null;
  isLoading: boolean;
  _initialized: boolean;

  initializeShortcuts: () => Promise<void>;
  updateShortcut: (id: string, binding: string) => Promise<void>;
  resetShortcut: (id: string) => Promise<void>;
  resetAllShortcuts: () => Promise<void>;
  setEditing: (id: string | null) => void;
  /** The binding in effect for this platform, authored form. */
  getEffectiveBinding: (id: string) => string;
  /** Another shortcut in the same context already claims this binding. */
  findConflict: (binding: string, excludeId?: string) => ShortcutDefinition | undefined;
  /** Browser reservation for a binding, when running in a browser tab. */
  getReservationFor: (binding: string) => Reservation | undefined;
}

export { defaultShortcuts };

/**
 * The binding a shortcut should use right now.
 *
 * A user override always wins. Otherwise desktop takes the plain default and
 * the browser takes the web default, which is where reserved chords like Cmd+T
 * get replaced by their Cmd+K sequences.
 */
function effectiveBinding(
  shortcut: ShortcutDefinition,
  isDesktop: boolean,
): string {
  if (shortcut.currentBinding) return shortcut.currentBinding;
  return isDesktop ? shortcut.defaultBinding : shortcut.defaultWebBinding;
}

/**
 * Push effective bindings to the Electron main process.
 *
 * The native menu's accelerators are handled by the OS before the renderer sees
 * a keydown, so if they were hardcoded a user's remap would appear to save but
 * never take effect. Sending them here keeps the menu in sync; the main process
 * ignores ids it does not put on the menu.
 */
function syncToNativeMenu(shortcuts: Record<string, ShortcutDefinition>): void {
  if (typeof window === "undefined" || !window.electronAPI?.updateShortcutBindings) {
    return;
  }

  const { isDesktop } = detectPlatform();
  const bindings: Record<string, string> = {};
  for (const [id, shortcut] of Object.entries(shortcuts)) {
    bindings[id] = effectiveBinding(shortcut, isDesktop);
  }

  void window.electronAPI.updateShortcutBindings(bindings).catch((error) => {
    logger.error("Failed to sync shortcuts to native menu", error);
  });
}

/** Compile the store's shortcuts into the dispatch registry. */
function buildRegistry(
  shortcuts: Record<string, ShortcutDefinition>,
): ShortcutRegistry {
  const { isMac, isDesktop } = detectPlatform();
  const resolved: ResolvedShortcut[] = [];

  for (const shortcut of Object.values(shortcuts)) {
    const authored = effectiveBinding(shortcut, isDesktop);
    if (!authored) continue;

    resolved.push({
      id: shortcut.id,
      handler: shortcut.handler,
      binding: parseBinding(authored, isMac),
      context: shortcut.context,
      allowInInput: shortcut.allowInInput,
      passthrough: shortcut.passthrough,
    });
  }

  return new ShortcutRegistry(resolved);
}

/** Merge generated defaults with the user's persisted overrides. */
function hydrate(
  saved: Record<string, { currentBinding?: string | null }>,
): Record<string, ShortcutDefinition> {
  const out: Record<string, ShortcutDefinition> = {};

  for (const [id, shortcut] of Object.entries(defaultShortcuts)) {
    out[id] = {
      ...shortcut,
      // Only a string override counts; anything else falls back to the default.
      currentBinding:
        typeof saved[id]?.currentBinding === "string"
          ? (saved[id].currentBinding as string)
          : null,
    };
  }

  return out;
}

async function persist(shortcuts: Record<string, ShortcutDefinition>) {
  // Persist overrides only. Storing the whole definition would freeze names,
  // contexts, and defaults at the version that wrote them, so later changes to
  // shortcuts.yaml could never reach a user who had opened Settings once.
  const overrides: Record<string, { currentBinding: string }> = {};
  for (const [id, shortcut] of Object.entries(shortcuts)) {
    if (shortcut.currentBinding) {
      overrides[id] = { currentBinding: shortcut.currentBinding };
    }
  }
  await api.settings.updateShortcuts(JSON.stringify(overrides));
}

export const useShortcutsStore = create<ShortcutsState>()((set, get) => ({
  shortcuts: {},
  registry: new ShortcutRegistry([]),
  isEditing: null,
  isLoading: false,
  _initialized: false,

  initializeShortcuts: async () => {
    if (get()._initialized || get().isLoading) return;

    // Always land on working defaults, even if the backend never answers.
    const settle = (saved: Record<string, { currentBinding?: string | null }>) => {
      const shortcuts = hydrate(saved);
      set({
        shortcuts,
        registry: buildRegistry(shortcuts),
        isLoading: false,
        _initialized: true,
      });
      syncToNativeMenu(shortcuts);
    };

    try {
      set({ isLoading: true });

      if (
        typeof window !== "undefined" &&
        window.location.protocol === "file:"
      ) {
        // Electron: the gRPC endpoint is injected after the page loads.
        const maxWaitTime = 5000;
        const startTime = Date.now();
        while (
          !window.RELIANT_CONFIG?.grpcUrl &&
          Date.now() - startTime < maxWaitTime
        ) {
          await new Promise((resolve) => setTimeout(resolve, 100));
        }
        if (!window.RELIANT_CONFIG?.grpcUrl) {
          throw new Error("Backend config not ready");
        }
      }

      const response = await api.settings.getShortcuts();
      let saved: Record<string, { currentBinding?: string | null }> = {};

      if (response.shortcuts && response.shortcuts !== "{}") {
        try {
          saved = JSON.parse(response.shortcuts);
        } catch (error) {
          logger.error("Failed to parse shortcuts from backend", error);
        }
      }

      settle(saved);
    } catch (error) {
      logger.error("Failed to load shortcuts from backend, using defaults", error);
      settle({});
    }
  },

  updateShortcut: async (id: string, binding: string) => {
    const previous = get().shortcuts;
    if (!previous[id]) return;

    const updated = {
      ...previous,
      [id]: { ...previous[id], currentBinding: binding },
    };

    set({ shortcuts: updated, registry: buildRegistry(updated) });
    syncToNativeMenu(updated);

    try {
      await persist(updated);
    } catch (error) {
      logger.error("Failed to save shortcuts to backend", error);
      set({ shortcuts: previous, registry: buildRegistry(previous) });
      throw error;
    }
  },

  resetShortcut: async (id: string) => {
    const previous = get().shortcuts;
    if (!previous[id]) return;

    const updated = {
      ...previous,
      [id]: { ...previous[id], currentBinding: null },
    };

    set({ shortcuts: updated, registry: buildRegistry(updated) });
    syncToNativeMenu(updated);

    try {
      await persist(updated);
    } catch (error) {
      logger.error("Failed to save shortcuts to backend", error);
      set({ shortcuts: previous, registry: buildRegistry(previous) });
      throw error;
    }
  },

  resetAllShortcuts: async () => {
    const previous = get().shortcuts;
    const updated: Record<string, ShortcutDefinition> = {};
    for (const [id, shortcut] of Object.entries(previous)) {
      updated[id] = { ...shortcut, currentBinding: null };
    }

    set({ shortcuts: updated, registry: buildRegistry(updated) });
    syncToNativeMenu(updated);

    try {
      await persist(updated);
    } catch (error) {
      logger.error("Failed to save shortcuts to backend", error);
      set({ shortcuts: previous, registry: buildRegistry(previous) });
      throw error;
    }
  },

  setEditing: (id: string | null) => set({ isEditing: id }),

  getEffectiveBinding: (id: string) => {
    const shortcut = get().shortcuts[id];
    if (!shortcut) return "";
    return effectiveBinding(shortcut, detectPlatform().isDesktop);
  },

  findConflict: (binding: string, excludeId?: string) => {
    const { isMac, isDesktop } = detectPlatform();
    const target = parseBinding(binding, isMac);
    const shortcuts = get().shortcuts;
    const subject = excludeId ? shortcuts[excludeId] : undefined;

    return Object.values(shortcuts).find((candidate) => {
      if (candidate.id === excludeId) return false;
      // The same chord in a different context is the point of the design, not
      // a conflict — only flag shortcuts that would actually shadow each other.
      if (subject && candidate.context !== subject.context) return false;
      return parseBinding(effectiveBinding(candidate, isDesktop), isMac) === target;
    });
  },

  getReservationFor: (binding: string) => {
    const { isMac, isDesktop } = detectPlatform();
    // Electron owns the whole keyboard; browser reservations do not apply.
    if (isDesktop) return undefined;
    return getReservation(parseBinding(binding, isMac), isMac);
  },
}));
