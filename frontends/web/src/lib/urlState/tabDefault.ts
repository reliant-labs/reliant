// Copyright (c) 2026 Reliant Labs

/**
 * A DEFAULT FOR AN EMPTY URL, per tab (owner, 2026-11-24: the URL is the only
 * source of view state; storage holds only what an empty URL starts from).
 *
 * Read in this order:
 *
 *   1. this tab's own value (sessionStorage): a reload keeps it, and no other
 *      tab can change it;
 *   2. the last value set in ANY tab (localStorage): only to seed a tab that
 *      has none of its own, and copied into the tab the first time it is read,
 *      so the tab keeps it from then on;
 *   3. nothing: the caller's own fallback (a server default, the most recent).
 *
 * `set` writes both. Nothing listens for another tab's writes: a tab that
 * already has a value never moves because another tab chose something else.
 * This is the rule store/activeOrgStore.ts follows for the tab's org.
 *
 * It is never the view state itself. A page reads what it shows from the URL,
 * and only when the URL names nothing does it ask this, then write the answer
 * into the URL (with a replace), so what is on screen is always in the URL.
 *
 * Router-neutral and app-neutral on purpose: it imports nothing from the app,
 * so it can move to @reliantlabs/forge-web-runtime unchanged.
 */

export interface TabDefaultCodec<T> {
  /** The stored string as a value; undefined for anything unreadable (it is ignored, never an error). */
  parse: (raw: string) => T | undefined;
  /** The value as the string stored. */
  serialize: (value: T) => string;
}

export interface TabDefault<T> {
  /** This tab's value, else the last one set in any tab (then kept as this tab's), else undefined. */
  get: () => T | undefined;
  /** Remember `value` for this tab and as the seed of new tabs; undefined forgets both. */
  set: (value: T | undefined) => void;
  /** This tab's own value only (no seed). */
  getOwn: () => T | undefined;
}

export interface TabDefaultOptions<T> extends TabDefaultCodec<T> {
  /**
   * The storage key, or a function of the scope it belongs to (a user, an org)
   * so two people sharing a browser never inherit each other's view. A
   * function returning null means "no scope yet": get answers undefined and
   * set does nothing.
   */
  key: string | (() => string | null);
  /** Injected in tests; the window's storages by default. */
  storage?: { tab: () => Storage; seed: () => Storage };
}

const PREFIX = "reliant.view.";

function readFrom(storage: () => Storage, key: string): string | null {
  try {
    return storage().getItem(key);
  } catch {
    return null;
  }
}

function writeTo(storage: () => Storage, key: string, value: string | null): void {
  try {
    if (value === null) storage().removeItem(key);
    else storage().setItem(key, value);
  } catch {
    // Storage refused (private mode, quota): the URL still holds the view; only the default is lost.
  }
}

export function createTabDefault<T>(options: TabDefaultOptions<T>): TabDefault<T> {
  const tab = options.storage?.tab ?? (() => sessionStorage);
  const seed = options.storage?.seed ?? (() => localStorage);
  const keyOf = (): string | null => {
    const key = typeof options.key === "function" ? options.key() : options.key;
    return key === null ? null : PREFIX + key;
  };
  const decode = (raw: string | null): T | undefined => {
    if (raw === null || raw === "") return undefined;
    try {
      return options.parse(raw);
    } catch {
      return undefined;
    }
  };

  return {
    getOwn: () => {
      const key = keyOf();
      return key ? decode(readFrom(tab, key)) : undefined;
    },
    get: () => {
      const key = keyOf();
      if (!key) return undefined;
      const own = decode(readFrom(tab, key));
      if (own !== undefined) return own;
      const raw = readFrom(seed, key);
      const seeded = decode(raw);
      // Kept as the tab's own: a later choice in another tab no longer reaches it.
      if (seeded !== undefined && raw !== null) writeTo(tab, key, raw);
      return seeded;
    },
    set: (value) => {
      const key = keyOf();
      if (!key) return;
      const raw = value === undefined ? null : options.serialize(value);
      writeTo(tab, key, raw);
      writeTo(seed, key, raw);
    },
  };
}

/** A codec for a plain string value. */
export const stringCodec: TabDefaultCodec<string> = {
  parse: (raw) => raw,
  serialize: (value) => value,
};
