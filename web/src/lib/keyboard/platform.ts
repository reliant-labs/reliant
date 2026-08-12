/**
 * Platform and runtime detection for the keyboard system.
 *
 * Two independent axes, and conflating them is a bug:
 *   isMac    — which physical modifier "Cmd" means (Meta vs Control).
 *   isDesktop — whether we are in Electron, where no browser owns any chord.
 *
 * A Mac user in Chrome is isMac + !isDesktop and needs the web bindings.
 */

// `Window.RELIANT_CONFIG` is declared in src/types/electron.d.ts — the single
// source of truth. Do not redeclare it here.

/**
 * Whether "Cmd" means Meta (macOS) or Control (everywhere else).
 *
 * Checks several signals because none is reliable alone: `navigator.platform`
 * is deprecated and returns "" in some environments, and `userAgentData` is
 * Chromium-only. Getting this wrong silently breaks every shortcut — the whole
 * registry is keyed on the resolved modifier — so it is worth the redundancy.
 */
export function detectIsMac(): boolean {
  if (typeof window === "undefined" || !window.navigator) return false;

  const nav = window.navigator as Navigator & {
    userAgentData?: { platform?: string };
  };

  const candidates = [
    nav.userAgentData?.platform,
    nav.platform,
    nav.userAgent,
  ];

  for (const candidate of candidates) {
    if (!candidate) continue;
    const value = candidate.toUpperCase();
    // "MACINTOSH" in a UA string, "MACOS" from userAgentData, "MACINTEL" from
    // the legacy platform field — all contain "MAC".
    if (value.includes("MAC")) return true;
  }

  return false;
}

/** True in the Electron shell, where browser chord reservations do not apply. */
export function detectIsDesktop(): boolean {
  if (typeof window === "undefined") return false;
  return Boolean(window.RELIANT_CONFIG?.isElectron);
}

export interface KeyboardPlatform {
  isMac: boolean;
  isDesktop: boolean;
}

export function detectPlatform(): KeyboardPlatform {
  return { isMac: detectIsMac(), isDesktop: detectIsDesktop() };
}

/** Render a canonical binding for display: "meta+shift+P" to "⌘⇧P". */
export function formatBinding(binding: string, isMac: boolean): string {
  if (!binding) return "";
  return binding
    .split(" ")
    .map((chord) => formatChord(chord, isMac))
    .join(" then ");
}

function formatChord(chord: string, isMac: boolean): string {
  const parts = chord.split("+");
  const key = parts.pop() ?? "";
  const out: string[] = [];

  for (const part of parts) {
    switch (part) {
      case "ctrl":
        out.push(isMac ? "⌃" : "Ctrl");
        break;
      case "meta":
        out.push(isMac ? "⌘" : "Win");
        break;
      case "shift":
        out.push(isMac ? "⇧" : "Shift");
        break;
      case "alt":
        out.push(isMac ? "⌥" : "Alt");
        break;
    }
  }

  out.push(formatKeyName(key));
  return isMac ? out.join("") : out.join("+");
}

const KEY_DISPLAY: Record<string, string> = {
  Up: "↑",
  Down: "↓",
  Left: "←",
  Right: "→",
  Enter: "↵",
  Escape: "Esc",
  Space: "Space",
};

function formatKeyName(key: string): string {
  return KEY_DISPLAY[key] ?? key;
}
