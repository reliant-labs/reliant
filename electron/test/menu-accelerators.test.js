const test = require("node:test");
const assert = require("node:assert");
const {
  defaultAccelerators,
  toElectronAccelerator,
  resolveMenuAccelerators,
} = require("../src/menu-accelerators");

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

test("defaultAccelerators use CmdOrCtrl so one value serves both platforms", () => {
  const accelerators = defaultAccelerators();

  // Cmd+Shift+Arrow replaced the old Cmd+Ctrl navigation chords, which needed
  // per-platform spellings; CmdOrCtrl folds correctly on its own.
  assert.strictEqual(accelerators.nextChat, "CmdOrCtrl+Shift+Down");
  assert.strictEqual(accelerators.prevChat, "CmdOrCtrl+Shift+Up");
  for (const value of Object.values(accelerators)) {
    assert.ok(!value.includes("Meta+Control"), `${value} should not be platform-specific`);
  }
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

test("resolveMenuAccelerators ignores ids that are not on the menu", () => {
  const resolved = resolveMenuAccelerators({
    switchChat: "Cmd+E",
    commandPalette: "Cmd+Shift+P",
  });

  assert.ok(!("switchChat" in resolved));
  assert.ok(!("commandPalette" in resolved));
  // Untouched menu entries keep their defaults.
  assert.strictEqual(resolved.newChat, "CmdOrCtrl+T");
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
