/**
 * The browser-reserved table against what Chromium actually withholds.
 *
 * bindings.test.ts refuses a web default the table calls hard-reserved — so a
 * gap in the table is a shortcut that ships dead. That is how Cmd+Shift+[ / ]
 * (next/previous file tab) went out: Chrome on macOS binds them to "select
 * next/previous tab", a reserved command, and the page never sees them.
 *
 * The lists below are Chromium's `kReservedCommandIds`
 * (chrome/browser/ui/accelerator_table.cc) at the chords it puts them on:
 * chrome/browser/global_keyboard_shortcuts_mac.mm and
 * chrome/browser/ui/cocoa/accelerators_cocoa.mm for macOS, accelerator_table.cc
 * elsewhere. Update them from those files, not from memory.
 */

import { describe, expect, it } from "vitest";
import { parseBinding } from "../chord";
import { getReservation, isHardReserved } from "../reserved";

/** Reserved commands on macOS Chrome, as authored bindings. */
const MAC_HARD = [
  "Cmd+T", // IDC_NEW_TAB
  "Cmd+N", // IDC_NEW_WINDOW
  "Cmd+Shift+N", // IDC_NEW_INCOGNITO_WINDOW
  "Cmd+W", // IDC_CLOSE_TAB
  "Cmd+Shift+W", // IDC_CLOSE_WINDOW
  "Cmd+Shift+T", // IDC_RESTORE_TAB
  "Cmd+Q", // IDC_EXIT
  "Cmd+Shift+]", // IDC_SELECT_NEXT_TAB
  "Cmd+Shift+[", // IDC_SELECT_PREVIOUS_TAB
  "Cmd+Alt+Right", // IDC_SELECT_NEXT_TAB
  "Cmd+Alt+Left", // IDC_SELECT_PREVIOUS_TAB
  "Ctrl+PageDown", // IDC_SELECT_NEXT_TAB
  "Ctrl+PageUp", // IDC_SELECT_PREVIOUS_TAB
  "Ctrl+Tab", // IDC_CYCLE_TO_NEXT_TAB
  "Ctrl+Shift+Tab", // IDC_CYCLE_TO_PREV_TAB
];

/** Reserved commands on Windows/Linux Chrome. */
const PC_HARD = [
  "Ctrl+T",
  "Ctrl+N",
  "Ctrl+Shift+N",
  "Ctrl+W",
  "Ctrl+F4",
  "Ctrl+Shift+W",
  "Alt+F4",
  "Ctrl+Shift+T",
  "Ctrl+Tab",
  "Ctrl+Shift+Tab",
  "Ctrl+PageDown",
  "Ctrl+PageUp",
];

/**
 * macOS menu commands that are NOT reserved: Chrome offers them to the page
 * first, so a shortcut there works. Calling one hard would forbid a working
 * binding (Cmd+K M, change model, typed with Cmd held, is Cmd+M).
 */
const MAC_DELIVERED = [
  "Cmd+M", // IDC_MINIMIZE_WINDOW
  "Cmd+L", // IDC_FOCUS_LOCATION
  "Cmd+F", // IDC_FIND
  "Cmd+,", // IDC_OPTIONS
  "Cmd+Alt+N", // IDC_NEW_SPLIT_TAB — Reliant's new chat in a browser
  "Cmd+Alt+B", // IDC_SHOW_BOOKMARK_MANAGER — toggle chat sidebar
  "Cmd+Shift+B", // IDC_SHOW_BOOKMARK_BAR — focus right sidebar
  "Cmd+Shift+J", // IDC_SHOW_DOWNLOADS — new terminal
  "Cmd+[", // IDC_BACK — Cmd+K [ typed with Cmd held
  "Cmd+]", // IDC_FORWARD — Cmd+K ] typed with Cmd held
  "Cmd+Left", // IDC_BACK (delayed) — Cmd+K Left typed with Cmd held
  "Cmd+Right", // IDC_FORWARD (delayed)
];

describe("browser reservations", () => {
  it.each(MAC_HARD)("macOS: %s never reaches the page", (binding) => {
    expect(isHardReserved(parseBinding(binding, true), true)).toBe(true);
  });

  it.each(PC_HARD)("Windows/Linux: %s never reaches the page", (binding) => {
    expect(isHardReserved(parseBinding(binding, false), false)).toBe(true);
  });

  it.each(MAC_DELIVERED)("macOS: %s is offered to the page first", (binding) => {
    expect(isHardReserved(parseBinding(binding, true), true)).toBe(false);
  });

  it("flags a sequence whose held second key is any reserved chord", () => {
    // Cmd+K then Cmd+Shift+] switches the browser tab before the page sees it.
    expect(getReservation(parseBinding("Cmd+K Shift+]", true), true)?.level).toBe("held");
    expect(getReservation(parseBinding("Cmd+K W", true), true)?.level).toBe("held");
    expect(getReservation(parseBinding("Cmd+K ]", true), true)).toBeUndefined();
    expect(getReservation(parseBinding("Cmd+K M", true), true)).toBeUndefined();
  });
});
