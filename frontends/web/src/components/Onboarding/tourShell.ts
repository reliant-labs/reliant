/**
 * Which app shell the tour runs in, and where its progress is stored.
 *
 * The same journeys run in two shells: Reliant (app.reliantlabs.io: signed
 * in, a daemon, settings RPCs) and the forge shell (forge.reliantlabs.io:
 * possibly signed out, no daemon, no settings service). A journey or step
 * declares which shells it belongs to (`shells` / `shell`), and per-shell
 * copy (`copyByShell`).
 *
 * The forge shell calls, once at boot, before anything renders the tour:
 *
 *   setTourShell("forge");
 *   setTourStorage(localStorageTourStorage("forge.tour.v1"));
 *
 * Without that the hostname decides the shell, and storage stays the
 * settings RPCs. Module state rather than React context, because the
 * registry and the store read it outside React.
 */

import { safeGetSetting, upsertStringSetting, deleteSettingIfExists } from "../../lib/settingsPersistence";

export type TourShell = "reliant" | "forge";

/** The forge shell's hostname (owner decision, 2026-11-05). */
export const FORGE_SHELL_HOST = "forge.reliantlabs.io";

let shellOverride: TourShell | null = null;

export function setTourShell(shell: TourShell | null): void {
  shellOverride = shell;
}

export function getTourShell(): TourShell {
  if (shellOverride) return shellOverride;
  try {
    if (typeof window !== "undefined" && window.location.hostname === FORGE_SHELL_HOST) return "forge";
  } catch {
    // fall through
  }
  return "reliant";
}

/** Whether something declared for `shells` belongs in the current shell. Omitted = every shell. */
export function inShell(shells: readonly TourShell[] | TourShell | "both" | undefined): boolean {
  if (!shells || shells === "both") return true;
  const shell = getTourShell();
  return typeof shells === "string" ? shells === shell : shells.includes(shell);
}

// ─── Storage ─────────────────────────────────────────────────────────────────

export interface TourStorage {
  /**
   * "settings" is Reliant's per-user settings rows; only it may drive the
   * Reliant-only side effects (the onboarding checklist).
   */
  kind: "settings" | "local";
  get: (key: string) => Promise<string | undefined>;
  set: (key: string, value: string) => Promise<void>;
  delete: (key: string) => Promise<void>;
}

export const settingsTourStorage: TourStorage = {
  kind: "settings",
  get: async (key) => (await safeGetSetting(key))?.value,
  set: (key, value) => upsertStringSetting(key, value),
  delete: (key) => deleteSettingIfExists(key),
};

/** All keys in ONE localStorage entry (`storageKey`), as a JSON object. */
export function localStorageTourStorage(storageKey: string): TourStorage {
  const read = (): Record<string, string> => {
    try {
      const raw = window.localStorage.getItem(storageKey);
      const parsed: unknown = raw ? JSON.parse(raw) : {};
      return parsed && typeof parsed === "object" ? (parsed as Record<string, string>) : {};
    } catch {
      return {};
    }
  };
  const write = (data: Record<string, string>) => {
    try {
      window.localStorage.setItem(storageKey, JSON.stringify(data));
    } catch {
      // Storage blocked or full: progress is per page load, as if signed out.
    }
  };
  return {
    kind: "local",
    get: async (key) => read()[key],
    set: async (key, value) => write({ ...read(), [key]: value }),
    delete: async (key) => {
      const data = read();
      delete data[key];
      write(data);
    },
  };
}

/**
 * Progress for this page load only, in memory: the public demo starts the tour
 * on every visit and never remembers a finish. `kind: "local"`, so Reliant's
 * settings-only side effects stay off.
 */
export function memoryTourStorage(): TourStorage {
  const data = new Map<string, string>();
  return {
    kind: "local",
    get: async (key) => data.get(key),
    set: async (key, value) => void data.set(key, value),
    delete: async (key) => void data.delete(key),
  };
}

let storage: TourStorage = settingsTourStorage;

export function setTourStorage(next: TourStorage): void {
  storage = next;
}

export function getTourStorage(): TourStorage {
  return storage;
}

/**
 * What only the Reliant shell does when a tour ends: land on the chat, and
 * ask for an API key if none is set. Installed by journeys/catalog.ts, so the
 * engine imports no chat or settings code (the forge demo's bundle carries
 * the engine, and may not carry those). Unset, a tour just closes.
 */
export interface ReliantTourHooks {
  /** A journey with no `finish` target ended in the Reliant shell. */
  landOnChat: (navigate: (options: never) => Promise<void>) => Promise<void>;
  /** After a tour ends or the picker is dismissed from onboarding. */
  afterTour: () => Promise<void>;
}

let reliantHooks: ReliantTourHooks | null = null;

export function setReliantTourHooks(hooks: ReliantTourHooks | null): void {
  reliantHooks = hooks;
}

export function getReliantTourHooks(): ReliantTourHooks | null {
  return reliantHooks;
}
