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
 * Extracted from main.js so it can be unit-tested without booting Electron.
 */

/**
 * Menu items that carry an accelerator, and their fallback bindings.
 *
 * These MUST mirror the desktop `binding:` values in config/shortcuts.yaml —
 * they are only a stopgap for the moments before the renderer reports in, so a
 * drifted default shows the wrong hint in the menu.
 */
function defaultAccelerators() {
  return {
    newChat: "CmdOrCtrl+T",
    closeTab: "CmdOrCtrl+W",
    reopenLastClosedFile: "CmdOrCtrl+Shift+T",
    toggleTerminal: "CmdOrCtrl+J",
    newTerminal: "CmdOrCtrl+Shift+J",
    nextChat: "CmdOrCtrl+Shift+Down",
    prevChat: "CmdOrCtrl+Shift+Up",
    nextRightSidebarTab: "CmdOrCtrl+Shift+Right",
    prevRightSidebarTab: "CmdOrCtrl+Shift+Left",
  };
}

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
 * Merge renderer-supplied bindings over the defaults.
 *
 * Unknown ids are ignored: the renderer sends every shortcut it knows about,
 * but only a handful appear on the menu.
 */
function resolveMenuAccelerators(bindings, platform = process.platform) {
  const resolved = defaultAccelerators(platform);
  if (!bindings || typeof bindings !== "object") return resolved;

  for (const [id, binding] of Object.entries(bindings)) {
    if (!(id in resolved)) continue;
    resolved[id] = toElectronAccelerator(binding);
  }

  return resolved;
}

module.exports = {
  defaultAccelerators,
  toElectronAccelerator,
  resolveMenuAccelerators,
};
