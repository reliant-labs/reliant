/**
 * Browser-reserved chords.
 *
 * Used to warn users when they rebind a shortcut to something the browser will
 * take first. This is advisory, not enforcement — Electron users are unaffected
 * and can bind whatever they like.
 *
 * Two tiers, because they fail differently:
 *   HARD     — the page never receives the event. Overriding is impossible, so
 *              the shortcut would silently do nothing.
 *   HOSTILE  — the page CAN intercept, but doing so breaks an expectation the
 *              user brought with them from every other site.
 *
 * Keys are canonical binding strings for the current platform.
 *
 * NOTE: This table is compiled from browser documentation and needs a manual
 * pass in real Chrome/Firefox/Safari before we lean on it hard. Entries marked
 * UNVERIFIED are the ones most likely to be wrong.
 */

export type ReservationLevel = "hard" | "hostile";

export interface Reservation {
  level: ReservationLevel;
  /** What the browser does with it, shown to the user. */
  reason: string;
}

/** macOS chords, expressed in canonical form (meta = Cmd). */
const MAC_RESERVED: Record<string, Reservation> = {
  "meta+T": { level: "hard", reason: "opens a new browser tab" },
  "meta+N": { level: "hard", reason: "opens a new browser window" },
  "meta+W": { level: "hard", reason: "closes the browser tab" },
  "meta+Q": { level: "hard", reason: "quits the browser" },
  "meta+shift+T": { level: "hard", reason: "reopens the last closed tab" },
  "meta+shift+N": { level: "hard", reason: "opens a private window" },
  "meta+M": { level: "hard", reason: "minimizes the window" },
  "meta+shift+W": { level: "hard", reason: "closes the browser window" },

  // Cmd+1..8 jump to the Nth tab and Cmd+9 to the last one, in every major
  // browser. Interceptable, but taking them breaks tab switching everywhere.
  "meta+1": { level: "hostile", reason: "switches to browser tab 1" },
  "meta+2": { level: "hostile", reason: "switches to browser tab 2" },
  "meta+3": { level: "hostile", reason: "switches to browser tab 3" },
  "meta+4": { level: "hostile", reason: "switches to browser tab 4" },
  "meta+5": { level: "hostile", reason: "switches to browser tab 5" },
  "meta+6": { level: "hostile", reason: "switches to browser tab 6" },
  "meta+7": { level: "hostile", reason: "switches to browser tab 7" },
  "meta+8": { level: "hostile", reason: "switches to browser tab 8" },
  "meta+9": { level: "hostile", reason: "switches to the last browser tab" },

  "meta+P": { level: "hostile", reason: "opens the print dialog" },
  "meta+S": { level: "hostile", reason: "saves the page" },
  "meta+O": { level: "hostile", reason: "opens a file" },
  "meta+F": { level: "hostile", reason: "opens find-in-page" },
  "meta+L": { level: "hostile", reason: "focuses the address bar" },
  "meta+D": { level: "hostile", reason: "bookmarks the page" },
  "meta+R": { level: "hostile", reason: "reloads the page" },
  // DevTools — interceptable, but blocking them is hostile to developers.
  "meta+alt+I": { level: "hostile", reason: "opens DevTools" },
  "meta+alt+J": { level: "hostile", reason: "opens the console" },
  "meta+alt+C": { level: "hostile", reason: "opens the element inspector" },
  "meta+shift+C": { level: "hostile", reason: "opens the element inspector" },
  "meta+shift+J": { level: "hostile", reason: "opens downloads (Chrome)" },
  "meta+shift+B": { level: "hostile", reason: "toggles the bookmarks bar" },
  // UNVERIFIED: Firefox-only; harmless to warn about on other browsers.
  "meta+shift+P": { level: "hostile", reason: "opens a private window (Firefox)" },
};

/** Windows/Linux chords (ctrl = the primary modifier). */
const PC_RESERVED: Record<string, Reservation> = {
  "ctrl+T": { level: "hard", reason: "opens a new browser tab" },
  "ctrl+N": { level: "hard", reason: "opens a new browser window" },
  "ctrl+W": { level: "hard", reason: "closes the browser tab" },
  "ctrl+shift+T": { level: "hard", reason: "reopens the last closed tab" },
  "ctrl+shift+N": { level: "hard", reason: "opens a private window" },
  "ctrl+shift+W": { level: "hard", reason: "closes the browser window" },

  "ctrl+1": { level: "hostile", reason: "switches to browser tab 1" },
  "ctrl+2": { level: "hostile", reason: "switches to browser tab 2" },
  "ctrl+3": { level: "hostile", reason: "switches to browser tab 3" },
  "ctrl+4": { level: "hostile", reason: "switches to browser tab 4" },
  "ctrl+5": { level: "hostile", reason: "switches to browser tab 5" },
  "ctrl+6": { level: "hostile", reason: "switches to browser tab 6" },
  "ctrl+7": { level: "hostile", reason: "switches to browser tab 7" },
  "ctrl+8": { level: "hostile", reason: "switches to browser tab 8" },
  "ctrl+9": { level: "hostile", reason: "switches to the last browser tab" },

  "ctrl+P": { level: "hostile", reason: "opens the print dialog" },
  "ctrl+S": { level: "hostile", reason: "saves the page" },
  "ctrl+O": { level: "hostile", reason: "opens a file" },
  "ctrl+F": { level: "hostile", reason: "opens find-in-page" },
  "ctrl+L": { level: "hostile", reason: "focuses the address bar" },
  "ctrl+D": { level: "hostile", reason: "bookmarks the page" },
  "ctrl+R": { level: "hostile", reason: "reloads the page" },
  "ctrl+H": { level: "hostile", reason: "opens history" },
  "ctrl+J": { level: "hostile", reason: "opens downloads (Chrome)" },
  "ctrl+shift+I": { level: "hostile", reason: "opens DevTools" },
  "ctrl+shift+J": { level: "hostile", reason: "opens the console" },
  "ctrl+shift+C": { level: "hostile", reason: "opens the element inspector" },
  "ctrl+shift+B": { level: "hostile", reason: "toggles the bookmarks bar" },
  F12: { level: "hostile", reason: "opens DevTools" },
};

/**
 * Look up a reservation for a canonical binding.
 *
 * Only the FIRST chord of a sequence is checked: once a prefix is captured the
 * remaining keystrokes belong to the app, which is exactly why sequences are
 * the escape hatch for reserved chords.
 */
export function getReservation(
  binding: string,
  isMac: boolean,
): Reservation | undefined {
  if (!binding) return undefined;
  const chords = binding.split(" ");
  // A sequence's prefix is captured by the app, so only a single-chord binding
  // can actually be lost to the browser.
  if (chords.length > 1) return undefined;
  return (isMac ? MAC_RESERVED : PC_RESERVED)[chords[0]];
}

/** True when the browser will never deliver this chord to the page. */
export function isHardReserved(binding: string, isMac: boolean): boolean {
  return getReservation(binding, isMac)?.level === "hard";
}
