/**
 * The shortcut path end to end, on each surface: the real generated bindings,
 * the real store, the real dispatcher, keystrokes dispatched at the document.
 *
 * In a browser tab the keystroke is the only way in, so every shortcut the app
 * leans on has to reach its handler from the keyboard alone. In the desktop app
 * the native menu delivers some of the same shortcuts too, and each must still
 * fire exactly once.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";

const getShortcuts = vi.hoisted(() =>
  vi.fn(async () => ({ shortcuts: "{}" })),
);

vi.mock("../../api/client", () => ({
  api: {
    settings: {
      getShortcuts,
      updateShortcuts: vi.fn(async () => ({ message: "" })),
    },
  },
}));

import { useAppKeyboardShortcuts } from "../useAppKeyboardShortcuts";
import { useShortcutsStore } from "../../store/shortcutsStore";
import { ShortcutRegistry } from "../../lib/keyboard/registry";

type Surface = { os: "mac" | "pc"; electron: boolean };

const HANDLERS = [
  "onCommandPalette",
  "onNewChat",
  "onCloseTab",
  "onToggleSidebar",
  "onToggleTerminal",
  "onNewTerminal",
] as const;

type Handlers = Record<(typeof HANDLERS)[number], ReturnType<typeof vi.fn>>;

function setSurface({ os, electron }: Surface) {
  Object.defineProperty(window.navigator, "platform", {
    value: os === "mac" ? "MacIntel" : "Win32",
    configurable: true,
  });
  Object.defineProperty(window.navigator, "userAgent", {
    value:
      os === "mac"
        ? "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0)"
        : "Mozilla/5.0 (Windows NT 10.0; Win64; x64)",
    configurable: true,
  });
  const w = window as unknown as Record<string, unknown>;
  w.RELIANT_CONFIG = electron ? { isElectron: true } : undefined;
}

/** Desktop-app menu subscribers, capturing what the main process would call. */
function installElectronMenu() {
  const listeners = new Map<string, () => void>();
  const subscriber = (name: string) => (callback: () => void) => {
    listeners.set(name, callback);
    return () => listeners.delete(name);
  };
  (window as unknown as Record<string, unknown>).electronAPI = {
    onCreateNewTab: subscriber("onCreateNewTab"),
    onCloseCurrentTab: subscriber("onCloseCurrentTab"),
    onReopenLastTab: subscriber("onReopenLastTab"),
    onToggleTerminal: subscriber("onToggleTerminal"),
    onNewTerminal: subscriber("onNewTerminal"),
    updateShortcutBindings: vi.fn(async () => ({ success: true })),
  };
  return {
    /** The main process sending a menu item's IPC message. */
    deliver: (name: string) => act(() => listeners.get(name)?.()),
  };
}

async function mount(surface: Surface) {
  setSurface(surface);
  const handlers = Object.fromEntries(
    HANDLERS.map((name) => [name, vi.fn()]),
  ) as Handlers;
  const view = renderHook(() => useAppKeyboardShortcuts(handlers));
  await waitFor(() =>
    expect(useShortcutsStore.getState().registry.size).toBeGreaterThan(0),
  );
  return { handlers, view };
}

/** Press one chord, the way the browser delivers it to the page. */
function press(init: KeyboardEventInit & { key: string }) {
  act(() => {
    document.body.dispatchEvent(
      new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init }),
    );
  });
}

beforeEach(() => {
  useShortcutsStore.setState({
    shortcuts: {},
    registry: new ShortcutRegistry([]),
    isLoading: false,
    _initialized: false,
  });
  getShortcuts.mockImplementation(async () => ({ shortcuts: "{}" }));
});

afterEach(() => {
  const w = window as unknown as Record<string, unknown>;
  delete w.electronAPI;
  delete w.RELIANT_CONFIG;
  vi.restoreAllMocks();
});

describe("in a browser tab on macOS", () => {
  const surface: Surface = { os: "mac", electron: false };

  it("opens the command palette with Cmd+Shift+P", async () => {
    const { handlers } = await mount(surface);
    press({ key: "p", code: "KeyP", metaKey: true, shiftKey: true });
    expect(handlers.onCommandPalette).toHaveBeenCalledOnce();
  });

  it("starts a new chat with Cmd+Option+N, which the browser releases", async () => {
    const { handlers } = await mount(surface);
    // Option+N is the tilde dead key, so the browser reports "Dead".
    press({ key: "Dead", code: "KeyN", metaKey: true, altKey: true });
    expect(handlers.onNewChat).toHaveBeenCalledOnce();
  });

  it("does not claim Cmd+T, which the browser keeps for itself", async () => {
    const { handlers } = await mount(surface);
    press({ key: "t", code: "KeyT", metaKey: true });
    expect(handlers.onNewChat).not.toHaveBeenCalled();
  });

  it("toggles the chat sidebar with Cmd+Option+B, though Option composes ∫", async () => {
    const { handlers } = await mount(surface);
    press({ key: "∫", code: "KeyB", metaKey: true, altKey: true });
    expect(handlers.onToggleSidebar).toHaveBeenCalledOnce();
  });

  it("toggles the terminal with Cmd+J", async () => {
    const { handlers } = await mount(surface);
    press({ key: "j", code: "KeyJ", metaKey: true });
    expect(handlers.onToggleTerminal).toHaveBeenCalledOnce();
  });

  it("closes a tab with Cmd+K X, typed with Cmd released or held", async () => {
    const { handlers } = await mount(surface);

    press({ key: "k", code: "KeyK", metaKey: true });
    press({ key: "x", code: "KeyX" });
    press({ key: "k", code: "KeyK", metaKey: true });
    press({ key: "x", code: "KeyX", metaKey: true });

    expect(handlers.onCloseTab).toHaveBeenCalledTimes(2);
  });

  it("matches by physical key on a non-Latin layout", async () => {
    const { handlers } = await mount(surface);
    // Russian layout: J types "о", Shift+P types "З".
    press({ key: "о", code: "KeyJ", metaKey: true });
    press({ key: "З", code: "KeyP", metaKey: true, shiftKey: true });
    expect(handlers.onToggleTerminal).toHaveBeenCalledOnce();
    expect(handlers.onCommandPalette).toHaveBeenCalledOnce();
  });
});

describe("in a browser tab on Windows and Linux", () => {
  const surface: Surface = { os: "pc", electron: false };

  it("fires the essentials from Ctrl", async () => {
    const { handlers } = await mount(surface);

    press({ key: "P", code: "KeyP", ctrlKey: true, shiftKey: true });
    press({ key: "n", code: "KeyN", ctrlKey: true, altKey: true });
    press({ key: "b", code: "KeyB", ctrlKey: true, altKey: true });
    press({ key: "j", code: "KeyJ", ctrlKey: true });

    expect(handlers.onCommandPalette).toHaveBeenCalledOnce();
    expect(handlers.onNewChat).toHaveBeenCalledOnce();
    expect(handlers.onToggleSidebar).toHaveBeenCalledOnce();
    expect(handlers.onToggleTerminal).toHaveBeenCalledOnce();
  });

  it("matches Ctrl+letter on a non-Latin layout", async () => {
    const { handlers } = await mount(surface);
    // Russian layout: Ctrl+J arrives as Ctrl+о.
    press({ key: "о", code: "KeyJ", ctrlKey: true });
    expect(handlers.onToggleTerminal).toHaveBeenCalledOnce();
  });

  it("does not treat AltGr text as a shortcut", async () => {
    const { handlers } = await mount(surface);
    // AltGr+N on a US-International layout types ñ, reported as Ctrl+Alt.
    press({ key: "ñ", code: "KeyN", ctrlKey: true, altKey: true });
    expect(handlers.onNewChat).not.toHaveBeenCalled();
  });
});

describe("a remap made in the desktop app, seen in a browser", () => {
  it("falls back to the web default when the browser would keep the chord", async () => {
    // Overrides are per user, not per surface. A desktop remap onto Cmd+Shift+T
    // would be dead in a browser and would shadow the web binding that works.
    getShortcuts.mockImplementation(async () => ({
      shortcuts: JSON.stringify({ newChat: { currentBinding: "Cmd+Shift+T" } }),
    }));
    const { handlers } = await mount({ os: "mac", electron: false });

    press({ key: "Dead", code: "KeyN", metaKey: true, altKey: true });

    expect(handlers.onNewChat).toHaveBeenCalledOnce();
    const store = useShortcutsStore.getState();
    expect(store.getEffectiveBinding("newChat")).toBe("Cmd+Alt+N");
    // ...and the settings UI is told why its binding is not the one in effect.
    expect(store.getUnavailableOverride("newChat")).toBe("Cmd+Shift+T");
  });

  it("keeps the remap in the desktop app", async () => {
    getShortcuts.mockImplementation(async () => ({
      shortcuts: JSON.stringify({ newChat: { currentBinding: "Cmd+Shift+T" } }),
    }));
    installElectronMenu();
    await mount({ os: "mac", electron: true });

    const store = useShortcutsStore.getState();
    expect(store.getEffectiveBinding("newChat")).toBe("Cmd+Shift+T");
    expect(store.getUnavailableOverride("newChat")).toBeUndefined();
  });
});

describe("in the desktop app", () => {
  const surface: Surface = { os: "mac", electron: true };

  it("uses the desktop chords the browser would have kept", async () => {
    installElectronMenu();
    const { handlers } = await mount(surface);

    press({ key: "t", code: "KeyT", metaKey: true });
    press({ key: "w", code: "KeyW", metaKey: true });

    expect(handlers.onNewChat).toHaveBeenCalledOnce();
    expect(handlers.onCloseTab).toHaveBeenCalledOnce();
  });

  it("runs a menu-delivered shortcut once, through the same handler", async () => {
    // Clicking Terminal > Toggle Terminal (or a Windows/Linux accelerator the
    // OS resolves before the page sees a key).
    const menu = installElectronMenu();
    const { handlers } = await mount(surface);

    menu.deliver("onToggleTerminal");

    expect(handlers.onToggleTerminal).toHaveBeenCalledOnce();
  });

  it("fires once when both the keystroke and the menu deliver it", async () => {
    // macOS offers Cmd+J to the page and can still fire the menu's accelerator.
    const menu = installElectronMenu();
    const { handlers } = await mount(surface);

    press({ key: "j", code: "KeyJ", metaKey: true });
    menu.deliver("onToggleTerminal");
    press({ key: "t", code: "KeyT", metaKey: true });
    menu.deliver("onCreateNewTab");

    expect(handlers.onToggleTerminal).toHaveBeenCalledOnce();
    expect(handlers.onNewChat).toHaveBeenCalledOnce();
  });

  it("routes every shortcut menu item to its shortcut", async () => {
    const menu = installElectronMenu();
    const { handlers } = await mount(surface);

    menu.deliver("onCreateNewTab");
    menu.deliver("onCloseCurrentTab");
    menu.deliver("onNewTerminal");

    expect(handlers.onNewChat).toHaveBeenCalledOnce();
    expect(handlers.onCloseTab).toHaveBeenCalledOnce();
    expect(handlers.onNewTerminal).toHaveBeenCalledOnce();
  });

  it("runs a menu item that arrives before shortcuts have loaded", async () => {
    // The tray's "New Chat" can land in the first moments after launch.
    let release: (value: { shortcuts: string }) => void = () => {};
    getShortcuts.mockImplementation(
      () => new Promise((resolve) => (release = resolve)),
    );
    const menu = installElectronMenu();
    setSurface(surface);
    const handlers = Object.fromEntries(
      HANDLERS.map((name) => [name, vi.fn()]),
    ) as Handlers;
    renderHook(() => useAppKeyboardShortcuts(handlers));

    menu.deliver("onCreateNewTab");
    expect(handlers.onNewChat).toHaveBeenCalledOnce();

    await act(async () => release({ shortcuts: "{}" }));
  });
});
