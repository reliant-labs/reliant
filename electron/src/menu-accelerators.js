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
 * A shortcut's binding is the same on desktop and in a browser tab, so some
 * are sequences (Close Tab is Cmd+K X) that no accelerator can express. Those
 * keep their old chord as a desktop alias (Cmd+W), and the menu falls back to
 * it: the item still shows the familiar hint, and — on Windows and Linux,
 * where the Window menu's Close role holds Ctrl+W too — the chord still closes
 * the tab, not the window. An alias yields to any binding on its chord, as it
 * does in the renderer, and is otherwise owned under the same rule.
 *
 * On Windows, Ctrl+Alt is AltGr, which TYPES characters (AltGr+N is "ñ" on
 * US-International). A native Ctrl+Alt accelerator would fire on that typing;
 * the renderer's dispatcher tells the two apart by the key produced (see
 * eventKey in frontends/web/src/lib/keyboard/chord.ts). So on Windows a
 * Ctrl+Alt chord is never an accelerator, and the item falls back to its alias
 * (New Chat shows Ctrl+T there) or to none.
 *
 * Extracted from main.js so it can be unit-tested without booting Electron.
 */

/**
 * The menu items that are shortcuts, with their `binding:` values from
 * config/shortcuts.yaml.
 *
 * Only a stopgap for the moments before the renderer reports the user's real
 * table; test/menu-accelerators.test.js fails if these drift from the YAML.
 */
const MENU_SHORTCUT_BINDINGS = Object.freeze({
  newChat: "Cmd+Alt+N",
  closeTab: "Cmd+K X",
  reopenLastClosedFile: "Cmd+K Z",
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

/**
 * The `desktop_aliases:` of config/shortcuts.yaml, for the same stopgap, and
 * held to the YAML by the same test. Every shortcut's, not only the menu's: an
 * alias elsewhere takes its chord from the menu as surely as a binding does.
 */
const DEFAULT_ALIASES = Object.freeze({
  newChat: Object.freeze(["Cmd+T"]),
  closeTab: Object.freeze(["Cmd+W"]),
  reopenLastClosedFile: Object.freeze(["Cmd+Shift+T"]),
  nextFileTab: Object.freeze(["Cmd+Shift+]"]),
  prevFileTab: Object.freeze(["Cmd+Shift+["]),
  runPackageCommand: Object.freeze(["Cmd+K N"]),
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

/**
 * Whether no shortcut but `id` needs the renderer to see `chord` — as its
 * binding, as one of its aliases, or as a sequence's opening chord.
 */
function ownsChord(id, chord, table, aliases) {
  if (!chord) return false;
  for (const [otherId, binding] of Object.entries(table)) {
    if (otherId !== id && leadingChord(binding) === chord) return false;
  }
  for (const [otherId, list] of Object.entries(aliases)) {
    if (otherId === id) continue;
    if (list.some((alias) => leadingChord(alias) === chord)) return false;
  }
  return true;
}

/** Whether a chord is Ctrl+Alt (Cmd folds to Ctrl off macOS) without Meta. */
function isAltGrChord(chord) {
  const modifiers = new Set(chord.split("+").slice(0, -1));
  return modifiers.has("alt") && (modifiers.has("cmd") || modifiers.has("ctrl"));
}

/**
 * The aliases that are live: well-formed, and on a chord no binding leads
 * with. The renderer's registry drops an alias a binding claims (a binding is
 * what someone chose; an alias is a courtesy), and the menu must agree, or a
 * user's remap onto Cmd+W would lose its accelerator to Close Tab's alias.
 */
function liveAliases(reported, table) {
  const out = {};
  if (!reported || typeof reported !== "object") return out;
  const bound = new Set(Object.values(table).map(leadingChord).filter(Boolean));
  for (const [id, list] of Object.entries(reported)) {
    if (!Array.isArray(list)) continue;
    const live = list.filter(
      (alias) => typeof alias === "string" && alias.trim() && !bound.has(leadingChord(alias)),
    );
    if (live.length > 0) out[id] = live;
  }
  return out;
}

/**
 * The accelerator for each menu shortcut, from the renderer's table of
 * effective bindings (every shortcut id → its binding) and of live desktop
 * aliases (id → chords).
 *
 * An item gets the first of its binding and its aliases that is a single
 * chord it owns (see the header), and on Windows not a Ctrl+Alt chord.
 * Otherwise it gets undefined — still clickable, with no native accelerator.
 * Without a table, the YAML defaults above stand in for both; a table with no
 * aliases (a renderer that predates them) means none.
 *
 * `platform` is process.platform's vocabulary; tests pass it explicitly.
 */
function resolveMenuAccelerators(bindings, aliases, { platform = process.platform } = {}) {
  const reported = bindings && typeof bindings === "object";
  const table = reported
    ? { ...MENU_SHORTCUT_BINDINGS, ...bindings }
    : { ...MENU_SHORTCUT_BINDINGS, ...SHARED_CHORD_BINDINGS };
  const aliasesById = liveAliases(reported ? aliases : DEFAULT_ALIASES, table);

  const resolved = {};
  for (const id of MENU_SHORTCUT_IDS) {
    resolved[id] = undefined;
    for (const candidate of [table[id], ...(aliasesById[id] ?? [])]) {
      const accelerator = toElectronAccelerator(candidate);
      if (!accelerator) continue;
      const chord = leadingChord(candidate);
      if (platform === "win32" && isAltGrChord(chord)) continue;
      if (!ownsChord(id, chord, table, aliasesById)) continue;
      resolved[id] = accelerator;
      break;
    }
  }
  return resolved;
}

/** The accelerators the menu starts with, before the renderer reports in. */
function defaultAccelerators(options) {
  return resolveMenuAccelerators(null, null, options);
}

module.exports = {
  MENU_SHORTCUT_IDS,
  DEFAULT_ALIASES,
  defaultAccelerators,
  toElectronAccelerator,
  resolveMenuAccelerators,
};
