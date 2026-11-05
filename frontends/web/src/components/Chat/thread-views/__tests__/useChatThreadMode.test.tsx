import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { SurfaceProvider } from "../../../../lib/surfaceContext";
import { SETTINGS_KEYS } from "../../../../services/settingsSync";
import {
  getStoredChatThreadMode,
  resolveChatThreadMode,
  setChatThreadMode,
  useChatThreadMode,
} from "../useChatThreadMode";

const store = new Map<string, string>();
const setSettingIfChanged = vi.fn(async (key: string, value: string) => {
  store.set(key, value);
  return true;
});

vi.mock("../../../../services/settingsSync", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../../../../services/settingsSync")>();
  return {
    ...actual,
    settingsSync: {
      getSetting: (key: string, fallback = "") => store.get(key) || fallback,
      setSettingIfChanged: (key: string, value: string) => setSettingIfChanged(key, value),
    },
  };
});

type Listener = () => void;
let matches = false;
let listeners = new Set<Listener>();

function installMatchMedia() {
  window.matchMedia = vi.fn().mockImplementation((query: string) => ({
    get matches() {
      return matches;
    },
    media: query,
    addEventListener: (_: string, fn: Listener) => listeners.add(fn),
    removeEventListener: (_: string, fn: Listener) => listeners.delete(fn),
  })) as unknown as typeof window.matchMedia;
}

function setNarrow(next: boolean) {
  matches = next;
  act(() => listeners.forEach((fn) => fn()));
}

const wrapperFor =
  (surface: "desktop" | "mobile" | "embed") =>
  ({ children }: { children: ReactNode }) => <SurfaceProvider surface={surface}>{children}</SurfaceProvider>;

beforeEach(() => {
  store.clear();
  setSettingIfChanged.mockClear();
  matches = false;
  listeners = new Set();
  installMatchMedia();
});

afterEach(() => vi.restoreAllMocks());

describe("resolveChatThreadMode", () => {
  it("honours an explicit preference regardless of environment", () => {
    expect(resolveChatThreadMode("simplified", { surface: "desktop", narrowViewport: false })).toBe("simplified");
    expect(resolveChatThreadMode("detailed", { surface: "mobile", narrowViewport: true })).toBe("detailed");
  });

  it("auto is detailed only on a wide desktop", () => {
    expect(resolveChatThreadMode("auto", { surface: "desktop", narrowViewport: false })).toBe("detailed");
    expect(resolveChatThreadMode("auto", { surface: "desktop", narrowViewport: true })).toBe("simplified");
    expect(resolveChatThreadMode("auto", { surface: "mobile", narrowViewport: false })).toBe("simplified");
    expect(resolveChatThreadMode("auto", { surface: "embed", narrowViewport: false })).toBe("simplified");
  });
});

describe("getStoredChatThreadMode", () => {
  it("defaults to simplified when unset", () => {
    expect(getStoredChatThreadMode()).toBe("simplified");
  });

  it.each(["auto", "simplified", "detailed"] as const)("returns stored %s", (value) => {
    store.set(SETTINGS_KEYS.CHAT_THREAD_MODE, value);
    expect(getStoredChatThreadMode()).toBe(value);
  });

  it("falls back to the simplified default for an unknown stored value", () => {
    store.set(SETTINGS_KEYS.CHAT_THREAD_MODE, "bogus");
    expect(getStoredChatThreadMode()).toBe("simplified");
  });
});

describe("setChatThreadMode", () => {
  it("persists and dispatches appearance-updated", () => {
    const onUpdated = vi.fn();
    window.addEventListener("appearance-updated", onUpdated);
    setChatThreadMode("detailed");
    window.removeEventListener("appearance-updated", onUpdated);
    expect(setSettingIfChanged).toHaveBeenCalledWith(SETTINGS_KEYS.CHAT_THREAD_MODE, "detailed");
    expect(onUpdated).toHaveBeenCalledTimes(1);
  });
});

describe("useChatThreadMode", () => {
  it("is simplified by default, even on a wide desktop", () => {
    const { result } = renderHook(() => useChatThreadMode(), { wrapper: wrapperFor("desktop") });
    expect(result.current).toEqual({ mode: "simplified", preference: "simplified" });
  });

  it("resolves auto from the surface", () => {
    store.set(SETTINGS_KEYS.CHAT_THREAD_MODE, "auto");
    const { result } = renderHook(() => useChatThreadMode(), { wrapper: wrapperFor("mobile") });
    expect(result.current).toEqual({ mode: "simplified", preference: "auto" });
  });

  it("reacts to media query changes under auto", () => {
    store.set(SETTINGS_KEYS.CHAT_THREAD_MODE, "auto");
    const { result } = renderHook(() => useChatThreadMode(), { wrapper: wrapperFor("desktop") });
    expect(result.current.mode).toBe("detailed");
    setNarrow(true);
    expect(result.current.mode).toBe("simplified");
    setNarrow(false);
    expect(result.current.mode).toBe("detailed");
  });

  it("re-reads the preference on appearance-updated", () => {
    const { result } = renderHook(() => useChatThreadMode(), { wrapper: wrapperFor("desktop") });
    store.set(SETTINGS_KEYS.CHAT_THREAD_MODE, "detailed");
    act(() => {
      window.dispatchEvent(new CustomEvent("appearance-updated"));
    });
    expect(result.current).toEqual({ mode: "detailed", preference: "detailed" });
  });

  it("is safe without matchMedia", () => {
    // @ts-expect-error simulate an environment lacking matchMedia
    window.matchMedia = undefined;
    store.set(SETTINGS_KEYS.CHAT_THREAD_MODE, "auto");
    const { result } = renderHook(() => useChatThreadMode(), { wrapper: wrapperFor("desktop") });
    expect(result.current.mode).toBe("detailed");
  });
});
