/**
 * Menu accelerator translation.
 *
 * The renderer owns the keyboard: config/shortcuts.yaml is the source of truth
 * and users can remap any binding in Settings. Native menu accelerators are
 * handled by the OS BEFORE the renderer sees a keydown, so a hardcoded
 * accelerator silently wins over a user's remap — Settings would appear to save
 * while the old chord kept firing.
 *
 * This module converts the renderer's authored bindings into Electron
 * accelerators so the menu tracks the user's configuration.
 *
 * A native accelerator also fires before the renderer can see where focus is,
 * so it can only ever mean one thing. That is wrong for a chord whose meaning
 * depends on focus: Cmd+Shift+Down is Next Chat in most of the app but "extend
 * the selection to the end" in the composer and the editor, and as a menu
 * accelerator it switched chats mid-selection. So the menu claims a chord only
 * when its item is the chord's SOLE owner in the renderer's table. A chord that
 * any other shortcut binds — in another context, or as the opening chord of a
 * sequence — is left to the renderer's dispatcher, which resolves focus
 * context. That item carries no accelerator at all: `registerAccelerator:
 * false` would keep the hint while leaving the key to the renderer, but
 * Electron honors it only on Linux and Windows, never on macOS.
 *
 * Ownership is derived from the bindings rather than listed by id, so it stays
 * right when the user remaps: move Next Chat to a free chord and the menu
 * claims it again; move New Terminal onto Cmd+Shift+Up and the menu lets go.
 *
 * Extracted from main.js so it can be unit-tested without booting Electron.
 */

/**
 * The menu items that are shortcuts, with their desktop `binding:` values from
 * config/shortcuts.yaml.
 *
 * Only a stopgap for the moments before the renderer reports the user's real
 * table; test/menu-accelerators.test.js fails if these drift from the YAML.
 */
const MENU_SHORTCUT_BINDINGS = Object.freeze({
  newChat: "Cmd+T",
  closeTab: "Cmd+W",
  reopenLastClosedFile: "Cmd+Shift+T",
  toggleTerminal: "Cmd+J",
  newTerminal: "Cmd+Shift+J",
  clearTerminal: "Cmd+Shift+L",
  nextChat: "Cmd+Shift+Down",
  prevChat: "Cmd+Shift+Up",
  nextRightSidebarTab: "Cmd+Shift+Right",
  prevRightSidebarTab: "Cmd+Shift+Left",
  findInPage: "Cmd+F",
});

/**
 * The other shortcuts in config/shortcuts.yaml bound to a menu item's chord,
 * for the same stopgap: the composer and the editor keep Cmd+Shift+Arrow for
 * text selection, the workflow canvas keeps Cmd+Shift+L, which clears the
 * terminal only while the terminal has focus, and Cmd+F searches the
 * conversation on screen, or the file in the editor, before it finds in the
 * page. Once the renderer reports in, its full table replaces this.
 */
const SHARED_CHORD_BINDINGS = Object.freeze({
  chatSearch: "Cmd+F",
  editorFind: "Cmd+F",
  connectSelectedSteps: "Cmd+Shift+L",
  selectionUpMonaco: "Cmd+Shift+Up",
  selectionDownMonaco: "Cmd+Shift+Down",
  selectionLeftMonaco: "Cmd+Shift+Left",
  selectionRightMonaco: "Cmd+Shift+Right",
  selectionUpComposer: "Cmd+Shift+Up",
  selectionDownComposer: "Cmd+Shift+Down",
  selectionLeftComposer: "Cmd+Shift+Left",
  selectionRightComposer: "Cmd+Shift+Right",
});

const MENU_SHORTCUT_IDS = Object.freeze(Object.keys(MENU_SHORTCUT_BINDINGS));

const MODIFIERS = Object.freeze({
  cmd: "cmd",
  mod: "cmd",
  cmdorctrl: "cmd",
  ctrl: "ctrl",
  control: "ctrl",
  shift: "shift",
  alt: "alt",
  option: "alt",
});

/**
 * Translate an authored binding ("Cmd+Shift+T") into an Electron accelerator.
 *
 * Returns undefined for sequences ("Cmd+K T"): Electron accelerators are single
 * chords, and the renderer handles sequences itself. Undefined is the right
 * answer rather than a fallback — leaving the menu item accelerator-less (still
 * clickable) is correct, whereas keeping a stale chord would re-introduce the
 * silent-override bug this module exists to fix.
 */
function toElectronAccelerator(binding) {
  if (!binding || typeof binding !== "string") return undefined;
  if (binding.trim().includes(" ")) return undefined;

  const parts = binding.split("+");
  const key = parts.pop();
  if (!key) return undefined;

  const modifiers = [];
  for (const part of parts) {
    switch (part.trim().toLowerCase()) {
      case "cmd":
      case "mod":
        modifiers.push("CmdOrCtrl");
        break;
      case "ctrl":
      case "control":
        modifiers.push("Control");
        break;
      case "shift":
        modifiers.push("Shift");
        break;
      case "alt":
      case "option":
        modifiers.push("Alt");
        break;
    }
  }

  return [...modifiers, key].join("+");
}

/**
 * The chord a binding makes the renderer listen for first — the binding itself,
 * or a sequence's opening chord — in a canonical form, so "Shift+Cmd+Up" and
 * "Cmd+Shift+Up" compare equal. Undefined for an empty or unbound binding.
 */
function leadingChord(binding) {
  if (!binding || typeof binding !== "string") return undefined;
  const [first] = binding.trim().split(/\s+/);
  const parts = first.split("+");
  const key = parts.pop();
  if (!key) return undefined;

  const modifiers = new Set();
  for (const part of parts) {
    const modifier = MODIFIERS[part.trim().toLowerCase()];
    if (modifier) modifiers.add(modifier);
  }
  return [...[...modifiers].sort(), key.toLowerCase()].join("+");
}

/** Whether no shortcut but `id` needs the renderer to see `id`'s chord. */
function ownsChord(id, table) {
  const chord = leadingChord(table[id]);
  if (!chord) return false;
  return !Object.entries(table).some(
    ([otherId, binding]) => otherId !== id && leadingChord(binding) === chord,
  );
}

/**
 * The accelerator for each menu shortcut, from the renderer's table of
 * effective bindings (every shortcut id → its binding).
 *
 * An item gets its chord only when it owns it (see the header); otherwise, or
 * when its binding is a sequence or unbound, it gets undefined — still
 * clickable, with no native accelerator. Without a table, the YAML defaults
 * above stand in for it.
 */
function resolveMenuAccelerators(bindings) {
  const reported = bindings && typeof bindings === "object";
  const table = reported
    ? { ...MENU_SHORTCUT_BINDINGS, ...bindings }
    : { ...MENU_SHORTCUT_BINDINGS, ...SHARED_CHORD_BINDINGS };

  const resolved = {};
  for (const id of MENU_SHORTCUT_IDS) {
    resolved[id] = ownsChord(id, table) ? toElectronAccelerator(table[id]) : undefined;
  }
  return resolved;
}

/** The accelerators the menu starts with, before the renderer reports in. */
function defaultAccelerators() {
  return resolveMenuAccelerators(null);
}

module.exports = {
  MENU_SHORTCUT_IDS,
  defaultAccelerators,
  toElectronAccelerator,
  resolveMenuAccelerators,
};
