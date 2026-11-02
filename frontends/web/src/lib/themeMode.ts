/**
 * Light / dark / system, as the user chooses it (`appearance.theme`).
 *
 * "system" follows the operating system's `prefers-color-scheme`, at load and
 * whenever it changes. A missing value means the same thing — index.html has
 * always fallen back to the system preference when nothing was saved — so a
 * user who never chose sees "System" selected rather than a guess read back
 * from the DOM.
 *
 * The `.dark` class on <html> is the one switch every stylesheet keys on;
 * this module only decides whether it is set.
 */

export type ThemeMode = "light" | "dark" | "system";

export const THEME_MODES: readonly ThemeMode[] = ["light", "dark", "system"];

const DARK_QUERY = "(prefers-color-scheme: dark)";

/** The stored value as a mode: anything but light or dark follows the system. */
export function parseThemeMode(value: string | null | undefined): ThemeMode {
  return value === "light" || value === "dark" ? value : "system";
}

export function systemPrefersDark(): boolean {
  return typeof window !== "undefined" && typeof window.matchMedia === "function"
    ? window.matchMedia(DARK_QUERY).matches
    : false;
}

/** Whether `mode` renders dark, given what the system prefers. */
export function isDarkMode(mode: ThemeMode, prefersDark: boolean = systemPrefersDark()): boolean {
  return mode === "system" ? prefersDark : mode === "dark";
}

/** Set or clear `.dark` for `mode`. Returns whether the class changed. */
export function applyThemeMode(mode: ThemeMode): boolean {
  const root = document.documentElement;
  const dark = isDarkMode(mode);
  if (root.classList.contains("dark") === dark) return false;
  root.classList.toggle("dark", dark);
  return true;
}

/**
 * Re-apply when the system preference changes, for as long as `readMode()`
 * says the user follows it. Returns the unsubscribe.
 */
export function followSystemTheme(readMode: () => ThemeMode, onChange?: () => void): () => void {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") return () => {};
  const query = window.matchMedia(DARK_QUERY);
  const listener = () => {
    if (readMode() !== "system") return;
    if (applyThemeMode("system")) onChange?.();
  };
  query.addEventListener("change", listener);
  return () => query.removeEventListener("change", listener);
}
