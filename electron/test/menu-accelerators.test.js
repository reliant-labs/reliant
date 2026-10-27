const test = require("node:test");
const assert = require("node:assert");
const fs = require("node:fs");
const path = require("node:path");
const {
  MENU_SHORTCUT_IDS,
  defaultAccelerators,
  toElectronAccelerator,
  resolveMenuAccelerators,
} = require("../src/menu-accelerators");

// The desktop binding of every shortcut in config/shortcuts.yaml — the same
// table the renderer pushes through updateShortcutBindings before any remap.
// A line scan rather than a YAML parser: electron/ declares no YAML dependency,
// and the file's `- id:` / `binding:` shape is regular.
function yamlDesktopBindings() {
  const file = path.join(__dirname, "..", "..", "config", "shortcuts.yaml");
  const bindings = {};
  let id = null;
  for (const line of fs.readFileSync(file, "utf8").split("\n")) {
    const idMatch = line.match(/^\s*-\s+id:\s*(\S+)/);
    if (idMatch) {
      id = idMatch[1];
      continue;
    }
    const bindingMatch = line.match(/^\s+binding:\s*(.+?)\s*$/);
    if (bindingMatch && id) {
      bindings[id] = bindingMatch[1].replace(/^["']|["']$/g, "");
    }
  }
  return bindings;
}

// The menu items whose default chords another context also binds:
// Cmd+Shift+Arrow extends the text selection in the composer and the editor,
// and Cmd+Shift+L connects steps on the workflow canvas.
const CONTEXT_RESOLVED = [
  "nextChat",
  "prevChat",
  "nextRightSidebarTab",
  "prevRightSidebarTab",
  "clearTerminal",
  "findInPage",
];

test("toElectronAccelerator maps Cmd to CmdOrCtrl", () => {
  assert.strictEqual(toElectronAccelerator("Cmd+T"), "CmdOrCtrl+T");
  assert.strictEqual(
    toElectronAccelerator("Cmd+Shift+T"),
    "CmdOrCtrl+Shift+T",
  );
});

test("toElectronAccelerator handles literal Ctrl and Alt", () => {
  assert.strictEqual(toElectronAccelerator("Ctrl+Alt+Down"), "Control+Alt+Down");
  assert.strictEqual(toElectronAccelerator("Cmd+Alt+B"), "CmdOrCtrl+Alt+B");
});

test("toElectronAccelerator returns undefined for sequences", () => {
  // Electron accelerators are single chords. Returning undefined leaves the
  // menu item clickable but accelerator-less, which is correct — keeping a
  // stale chord would re-introduce the silent-override bug.
  assert.strictEqual(toElectronAccelerator("Cmd+K T"), undefined);
  assert.strictEqual(toElectronAccelerator("Cmd+K Right"), undefined);
});

test("toElectronAccelerator returns undefined for empty input", () => {
  assert.strictEqual(toElectronAccelerator(""), undefined);
  assert.strictEqual(toElectronAccelerator(null), undefined);
  assert.strictEqual(toElectronAccelerator(undefined), undefined);
});

test("the menu's fallback agrees with what config/shortcuts.yaml resolves to", () => {
  // The fallback only covers the moments before the renderer reports in, but a
  // drifted one claims the wrong chord — or a chord the composer needs.
  const yaml = yamlDesktopBindings();
  for (const id of MENU_SHORTCUT_IDS) {
    assert.ok(id in yaml, `menu shortcut ${id} is not in config/shortcuts.yaml`);
  }

  assert.deepStrictEqual(defaultAccelerators(), resolveMenuAccelerators(yaml));
});

test("defaultAccelerators use CmdOrCtrl so one value serves both platforms", () => {
  const accelerators = defaultAccelerators();

  assert.strictEqual(accelerators.newChat, "CmdOrCtrl+T");
  assert.strictEqual(accelerators.toggleTerminal, "CmdOrCtrl+J");
  for (const value of Object.values(accelerators).filter(Boolean)) {
    assert.ok(!value.includes("Meta+Control"), `${value} should not be platform-specific`);
  }
});

test("a chord the composer and editor use for selection gets no native accelerator", () => {
  // Cmd+Shift+Arrow navigates chats and sidebar tabs globally, but extends the
  // selection in the composer and in Monaco. A native accelerator fires before
  // the renderer can see where focus is, so it would switch chats in the middle
  // of a selection. registerAccelerator: false is not an option: macOS ignores
  // it, so the item must carry no accelerator at all.
  for (const accelerators of [defaultAccelerators(), resolveMenuAccelerators(yamlDesktopBindings())]) {
    for (const id of CONTEXT_RESOLVED) {
      assert.strictEqual(accelerators[id], undefined, `${id} must leave its chord to the renderer`);
    }
    assert.strictEqual(accelerators.newChat, "CmdOrCtrl+T", "a chord nothing else binds is still claimed");
  }
});

test("Clear Terminal leaves Cmd+Shift+L to the workflow canvas, and reclaims it once free", () => {
  // Clear Terminal is a terminal-context shortcut; Connect Selected Steps holds
  // the same chord on the workflow canvas. As a native accelerator it cleared
  // the terminal from the canvas instead of connecting the steps.
  const yaml = yamlDesktopBindings();
  assert.strictEqual(yaml.clearTerminal, "Cmd+Shift+L");
  assert.strictEqual(yaml.connectSelectedSteps, "Cmd+Shift+L");
  assert.strictEqual(resolveMenuAccelerators(yaml).clearTerminal, undefined);

  const freed = resolveMenuAccelerators({ ...yaml, connectSelectedSteps: "Cmd+Alt+L" });
  assert.strictEqual(freed.clearTerminal, "CmdOrCtrl+Shift+L");
});

test("Edit › Find leaves Cmd+F to the conversation search and the editor's find", () => {
  // As a native accelerator, Cmd+F would reach the menu before the renderer
  // could tell a conversation or the editor has focus, and open the page's
  // find bar over both.
  const yaml = yamlDesktopBindings();
  assert.strictEqual(yaml.findInPage, "Cmd+F");
  assert.strictEqual(resolveMenuAccelerators(yaml).findInPage, undefined);
  assert.strictEqual(defaultAccelerators().findInPage, undefined);

  const freed = resolveMenuAccelerators({ ...yaml, chatSearch: "Cmd+Alt+F", editorFind: "" });
  assert.strictEqual(freed.findInPage, "CmdOrCtrl+F");
});

test("re-sending the same chord for a context-resolved item does not reclaim it", () => {
  const resolved = resolveMenuAccelerators({
    ...yamlDesktopBindings(),
    nextChat: "Cmd+Shift+Down",
  });

  assert.strictEqual(resolved.nextChat, undefined);
});

test("remapping a navigation shortcut to a free chord puts it back on the menu", () => {
  const resolved = resolveMenuAccelerators({
    ...yamlDesktopBindings(),
    nextChat: "Cmd+Alt+Down",
  });

  assert.strictEqual(resolved.nextChat, "CmdOrCtrl+Alt+Down");
  assert.strictEqual(resolved.prevChat, undefined, "the others still share their chords");
});

test("remapping a menu shortcut onto a selection chord takes it off the menu", () => {
  // Derived from the bindings, not listed by id: New Terminal on Cmd+Shift+Up
  // must let go of that chord just as Previous Chat does.
  const resolved = resolveMenuAccelerators({
    ...yamlDesktopBindings(),
    newTerminal: "Cmd+Shift+Up",
  });

  assert.strictEqual(resolved.newTerminal, undefined);
});

test("two shortcuts on one chord leave it to the renderer", () => {
  // Modifier order does not make two chords different.
  const resolved = resolveMenuAccelerators({
    ...yamlDesktopBindings(),
    switchChat: "Cmd+J",
    commandPalette: "Shift+Cmd+T",
  });

  assert.strictEqual(resolved.toggleTerminal, undefined);
  assert.strictEqual(resolved.reopenLastClosedFile, undefined);
});

test("resolveMenuAccelerators applies a user remap", () => {
  const resolved = resolveMenuAccelerators({ newChat: "Cmd+Alt+N" });

  assert.strictEqual(resolved.newChat, "CmdOrCtrl+Alt+N");
});

test("resolveMenuAccelerators clears the accelerator when remapped to a sequence", () => {
  // The whole point: if the user moves New Chat to Cmd+K T, the menu must STOP
  // claiming Cmd+T, or the OS keeps firing the old binding.
  const resolved = resolveMenuAccelerators({ newChat: "Cmd+K T" });

  assert.strictEqual(resolved.newChat, undefined);
});

test("resolveMenuAccelerators returns menu ids only", () => {
  const resolved = resolveMenuAccelerators(yamlDesktopBindings());

  assert.deepStrictEqual(Object.keys(resolved), [...MENU_SHORTCUT_IDS]);
});

test("resolveMenuAccelerators falls back to defaults for bad input", () => {
  assert.deepStrictEqual(
    resolveMenuAccelerators(null),
    defaultAccelerators(),
  );
  assert.deepStrictEqual(
    resolveMenuAccelerators("nonsense"),
    defaultAccelerators(),
  );
});

test("no menu accelerator claims the Cmd+K sequence prefix", () => {
  // A menu accelerator on Cmd+K would be handled by the OS before the renderer
  // sees the keydown, swallowing every Cmd+K sequence in the app.
  for (const [id, accelerator] of Object.entries(defaultAccelerators())) {
    assert.notStrictEqual(
      accelerator,
      "CmdOrCtrl+K",
      `${id} must not claim the sequence prefix`,
    );
  }
});

test("a menu shortcut remapped onto a sequence prefix gets no accelerator", () => {
  // The same rule covers sequences: their opening chord must reach the
  // renderer, so a remap onto it cannot become a native accelerator.
  const resolved = resolveMenuAccelerators({
    ...yamlDesktopBindings(),
    newChat: "Cmd+K",
  });

  assert.strictEqual(resolved.newChat, undefined);
});
