/**
 * The stores hydrate from database settings once settingsSync has loaded them.
 *
 * THE BUG THIS CLOSES: settingsSync initializes after sign-in, in
 * AuthInitializer — a child of App, so after App's mount. The hook checked
 * isInitialized() on mount, found it false on every startup, logged
 * "[SettingsHydration] settingsSync not initialized (unexpected)" and returned:
 * database values for hidden files and editor settings never reached the
 * stores.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";
import { renderHook } from "@testing-library/react";

const db = vi.hoisted(() => ({
  settings: [] as Array<{ key: string; value: string }>,
}));

vi.mock("../settings-queries", () => ({
  fetchSettings: vi.fn(async () => ({ settings: db.settings })),
  batchUpsertSettings: vi.fn(async () => []),
  deleteSetting: vi.fn(async () => ({})),
}));

vi.mock("../../lib/logger", () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() },
}));

beforeEach(() => {
  vi.resetModules();
  localStorage.clear();
  db.settings = [];
});

async function load() {
  const [{ useSettingsHydration }, { settingsSync, SETTINGS_KEYS }, { useUIStore }, { useEditorStore }] =
    await Promise.all([
      import("../useSettingsHydration"),
      import("../../services/settingsSync"),
      import("../../store/uiStore"),
      import("../../store/editorStore"),
    ]);
  return { useSettingsHydration, settingsSync, SETTINGS_KEYS, useUIStore, useEditorStore };
}

describe("useSettingsHydration", () => {
  it("hydrates the stores when settingsSync finishes initializing after mount", async () => {
    const { useSettingsHydration, settingsSync, SETTINGS_KEYS, useUIStore, useEditorStore } = await load();
    const editorSettings = { ...useEditorStore.getState().settings, minimap: false, tabSize: 7 };
    db.settings = [
      { key: SETTINGS_KEYS.SHOW_HIDDEN_FILES, value: String(!useUIStore.getState().showHiddenFiles) },
      { key: SETTINGS_KEYS.EDITOR_SETTINGS, value: JSON.stringify(editorSettings) },
    ];
    const before = useUIStore.getState().showHiddenFiles;

    renderHook(() => useSettingsHydration());
    expect(useUIStore.getState().showHiddenFiles).toBe(before);

    await settingsSync.initialize();

    expect(useUIStore.getState().showHiddenFiles).toBe(!before);
    expect(useEditorStore.getState().settings).toEqual(editorSettings);
  });

  it("hydrates at once when settingsSync was already initialized", async () => {
    const { useSettingsHydration, settingsSync, SETTINGS_KEYS, useEditorStore } = await load();
    const editorSettings = { ...useEditorStore.getState().settings, tabSize: 3 };
    db.settings = [{ key: SETTINGS_KEYS.EDITOR_SETTINGS, value: JSON.stringify(editorSettings) }];
    await settingsSync.initialize();

    renderHook(() => useSettingsHydration());

    expect(useEditorStore.getState().settings).toEqual(editorSettings);
  });

  it("does not hydrate after unmount", async () => {
    const { useSettingsHydration, settingsSync, SETTINGS_KEYS, useEditorStore } = await load();
    const original = useEditorStore.getState().settings;
    db.settings = [
      { key: SETTINGS_KEYS.EDITOR_SETTINGS, value: JSON.stringify({ ...original, tabSize: 9 }) },
    ];

    const { unmount } = renderHook(() => useSettingsHydration());
    unmount();
    await settingsSync.initialize();

    expect(useEditorStore.getState().settings).toEqual(original);
  });
});
