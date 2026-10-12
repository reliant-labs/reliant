// The application menu's keyboard-shortcut items, and how one reaches the
// renderer.
//
// The renderer's keystroke dispatcher is the single source of truth for what a
// shortcut does (config/shortcuts.yaml). A menu item that is a shortcut is just
// another way to invoke it: main sends the shortcut's id on ONE channel,
// "menu-shortcut", and the renderer runs that id through its dispatcher — which
// also drops the copy that arrives right after the same keystroke. It used to
// be one channel per shortcut, each needing its own preload subscriber, and the
// four Navigate items sent channels nothing listened for, so clicking them did
// nothing.
//
// Which window: the one the user is in. With no Reliant window in front —
// closed to the tray, minimized, or the app active with no window — the
// shortcut brings one back first through the same surfacer as the tray and the
// dock (window-surfacing.js), which creates one only if none exists. File ›
// New Tab used to open a second window here even while one sat in the tray.
//
// Close Tab and Clear Terminal are the exceptions: they act on something the
// user is looking at. With no Reliant window focused there is nothing on screen
// to close or clear, and bringing a window back just to close part of it, or
// to wipe a terminal the user has not seen, is not what the keystroke asks for.
//
// Kept free of Electron imports so the menu's wiring is unit-testable.

const MENU_SHORTCUT_CHANNEL = "menu-shortcut";

// Every menu item that is a shortcut, by shortcut id. main.js places each one
// in its submenu; the ids match menu-accelerators.js.
const MENU_SHORTCUT_LABELS = Object.freeze({
  newChat: "New Tab",
  closeTab: "Close Tab",
  reopenLastClosedFile: "Reopen Last Tab",
  newTerminal: "New Terminal",
  toggleTerminal: "Toggle Terminal",
  clearTerminal: "Clear Terminal",
  nextChat: "Next Chat",
  prevChat: "Previous Chat",
  nextRightSidebarTab: "Next Sidebar Tab",
  prevRightSidebarTab: "Previous Sidebar Tab",
});

// Shortcuts that act only on a window the user is already in.
const NEEDS_FOCUSED_WINDOW = new Set(["closeTab", "clearTerminal"]);

// Returns deliver(shortcutId), true when it sent.
//   send(channel, payload) — surfaces the window the user is in and sends to
//                            it once loaded (the surfacer's surfaceAndSend).
//   hasFocusedWindow()     — whether a Reliant window has keyboard focus.
function createMenuShortcutDelivery({ send, hasFocusedWindow }) {
  return function deliverMenuShortcut(shortcutId) {
    if (NEEDS_FOCUSED_WINDOW.has(shortcutId) && !hasFocusedWindow()) {
      return false;
    }
    send(MENU_SHORTCUT_CHANNEL, shortcutId);
    return true;
  };
}

// The menu items, by shortcut id: each carries the accelerator
// menu-accelerators.js resolved for it (undefined when the renderer must see
// the chord) and delivers its id when clicked.
function menuShortcutItems({ accelerators, deliver }) {
  const items = {};
  for (const [id, label] of Object.entries(MENU_SHORTCUT_LABELS)) {
    items[id] = {
      label,
      accelerator: accelerators[id],
      click: () => deliver(id),
    };
  }
  return items;
}

module.exports = {
  MENU_SHORTCUT_CHANNEL,
  createMenuShortcutDelivery,
  menuShortcutItems,
};
