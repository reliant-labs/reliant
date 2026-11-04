/**
 * Browser-reserved chords.
 *
 * Two jobs: the shortcut settings warn a user who rebinds onto a chord the
 * browser takes first, and bindings.test.ts refuses a web default the browser
 * would never deliver. Electron users are unaffected and can bind whatever
 * they like.
 *
 * Three tiers, because they fail differently:
 *   HARD     — the page never receives the event. Overriding is impossible, so
 *              the shortcut would silently do nothing.
 *   HOSTILE  — the page CAN intercept, but doing so breaks an expectation the
 *              user brought with them from every other site.
 *   HELD     — a sequence whose second key, typed with the prefix modifier
 *              still held (Cmd+K then Cmd+W), is a HARD chord. It works when
 *              the modifier is released first; held, the browser acts instead —
 *              and for W that closes Reliant's own tab.
 *
 * Keys are canonical binding strings for the current platform.
 *
 * WHERE HARD COMES FROM. Chromium withholds a keystroke from the page only when
 * it maps to a command in its reserved set — `kReservedCommandIds` in
 * chrome/browser/ui/accelerator_table.cc: new tab/window/incognito window,
 * close tab/window, restore tab, select next/previous tab, cycle tabs, exit.
 * Everything else, menu items included (Cmd+L, Cmd+P, Cmd+M, Cmd+Alt+B...), is
 * offered to the page first and runs only if the page leaves it unclaimed. The
 * chords those commands sit on are in chrome/browser/global_keyboard_shortcuts_mac.mm
 * and chrome/browser/ui/cocoa/accelerators_cocoa.mm (macOS) and accelerator_table.cc
 * (elsewhere); reserved.test.ts pins them. Firefox's `reserved="true"` keys
 * are the same tab/window set. (In fullscreen with the toolbar hidden, Chrome
 * releases all of them to the page.)
 *
 * The HOSTILE entries are the browsers' own menu chords; a gap there costs a
 * missing warning, not a dead shortcut.
 */

import { heldSecondChord } from "./chord";

export type ReservationLevel = "hard" | "hostile" | "held";

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
  "meta+shift+W": { level: "hard", reason: "closes the browser window" },
  // Tab switching. Cmd+Shift+[ and ] look like ordinary punctuation chords and
  // shipped as Reliant's next/previous file tab, which never fired in Chrome.
  "meta+shift+]": { level: "hard", reason: "switches to the next browser tab" },
  "meta+shift+[": { level: "hard", reason: "switches to the previous browser tab" },
  "meta+alt+Right": { level: "hard", reason: "switches to the next browser tab" },
  "meta+alt+Left": { level: "hard", reason: "switches to the previous browser tab" },
  "ctrl+Tab": { level: "hard", reason: "switches to the next browser tab" },
  "ctrl+shift+Tab": { level: "hard", reason: "switches to the previous browser tab" },
  "ctrl+PageDown": { level: "hard", reason: "switches to the next browser tab" },
  "ctrl+PageUp": { level: "hard", reason: "switches to the previous browser tab" },

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

  "meta+M": { level: "hostile", reason: "minimizes the window" },
  "meta+P": { level: "hostile", reason: "opens the print dialog" },
  "meta+S": { level: "hostile", reason: "saves the page" },
  "meta+O": { level: "hostile", reason: "opens a file" },
  "meta+F": { level: "hostile", reason: "opens find-in-page" },
  "meta+L": { level: "hostile", reason: "focuses the address bar" },
  "meta+D": { level: "hostile", reason: "bookmarks the page" },
  "meta+R": { level: "hostile", reason: "reloads the page" },
  "meta+Y": { level: "hostile", reason: "opens history" },
  "meta+,": { level: "hostile", reason: "opens the browser's settings" },
  "meta+[": { level: "hostile", reason: "goes back a page" },
  "meta+]": { level: "hostile", reason: "goes forward a page" },
  // DevTools — interceptable, but blocking them is hostile to developers.
  "meta+alt+I": { level: "hostile", reason: "opens DevTools" },
  "meta+alt+J": { level: "hostile", reason: "opens the console" },
  "meta+alt+C": { level: "hostile", reason: "opens the element inspector" },
  "meta+shift+C": { level: "hostile", reason: "opens the element inspector" },
  "meta+alt+U": { level: "hostile", reason: "views the page source" },
  "meta+shift+J": { level: "hostile", reason: "opens downloads (Chrome)" },
  "meta+alt+L": { level: "hostile", reason: "opens downloads (Chrome)" },
  "meta+shift+B": { level: "hostile", reason: "toggles the bookmarks bar" },
  "meta+alt+B": { level: "hostile", reason: "opens the bookmark manager (Chrome)" },
  "meta+alt+N": { level: "hostile", reason: "opens a split view (Chrome)" },
  "meta+shift+P": { level: "hostile", reason: "opens a private window (Firefox)" },
};

/** Windows/Linux chords (ctrl = the primary modifier). */
const PC_RESERVED: Record<string, Reservation> = {
  "ctrl+T": { level: "hard", reason: "opens a new browser tab" },
  "ctrl+N": { level: "hard", reason: "opens a new browser window" },
  "ctrl+W": { level: "hard", reason: "closes the browser tab" },
  "ctrl+F4": { level: "hard", reason: "closes the browser tab" },
  "alt+F4": { level: "hard", reason: "closes the browser window" },
  "ctrl+shift+T": { level: "hard", reason: "reopens the last closed tab" },
  "ctrl+shift+N": { level: "hard", reason: "opens a private window" },
  "ctrl+shift+W": { level: "hard", reason: "closes the browser window" },
  "ctrl+shift+Q": { level: "hard", reason: "quits the browser" },
  "ctrl+Tab": { level: "hard", reason: "switches to the next browser tab" },
  "ctrl+shift+Tab": { level: "hard", reason: "switches to the previous browser tab" },
  "ctrl+PageDown": { level: "hard", reason: "switches to the next browser tab" },
  "ctrl+PageUp": { level: "hard", reason: "switches to the previous browser tab" },

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
 * A sequence's prefix is captured by the app, so its keystrokes are the app's
 * — with one exception. The second key is commonly typed with the prefix
 * modifier still held (Cmd+K then Cmd+W: VS Code muscle memory, which the
 * dispatcher accepts on purpose), and the browser judges THAT chord before the
 * page sees it. A held form the browser withholds is reported at the `held`
 * level.
 */
export function getReservation(
  binding: string,
  isMac: boolean,
): Reservation | undefined {
  if (!binding) return undefined;
  const table = isMac ? MAC_RESERVED : PC_RESERVED;

  const held = heldSecondChord(binding);
  if (held === undefined) return table[binding];

  const reservation = table[held];
  if (reservation?.level !== "hard") return undefined;
  return { level: "held", reason: reservation.reason };
}

/** True when the browser will never deliver this chord to the page. */
export function isHardReserved(binding: string, isMac: boolean): boolean {
  return getReservation(binding, isMac)?.level === "hard";
}
