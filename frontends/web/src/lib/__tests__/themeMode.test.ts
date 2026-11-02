import { afterEach, describe, expect, it, vi } from "vitest";
import { applyThemeMode, followSystemTheme, isDarkMode, parseThemeMode } from "../themeMode";

function mockSystemDark(dark: boolean) {
  const listeners = new Set<() => void>();
  const query = {
    matches: dark,
    addEventListener: (_: string, l: () => void) => listeners.add(l),
    removeEventListener: (_: string, l: () => void) => listeners.delete(l),
  };
  vi.stubGlobal("matchMedia", () => query);
  window.matchMedia = (() => query) as unknown as typeof window.matchMedia;
  return {
    set(next: boolean) {
      query.matches = next;
      listeners.forEach((l) => l());
    },
  };
}

afterEach(() => {
  vi.unstubAllGlobals();
  document.documentElement.classList.remove("dark");
});

describe("theme mode", () => {
  it("reads anything but light or dark as following the system", () => {
    expect(parseThemeMode("light")).toBe("light");
    expect(parseThemeMode("dark")).toBe("dark");
    expect(parseThemeMode("system")).toBe("system");
    // Never chosen: index.html has always followed the system then.
    expect(parseThemeMode("")).toBe("system");
    expect(parseThemeMode(null)).toBe("system");
  });

  it("resolves system from the device preference", () => {
    expect(isDarkMode("system", true)).toBe(true);
    expect(isDarkMode("system", false)).toBe(false);
    expect(isDarkMode("dark", false)).toBe(true);
    expect(isDarkMode("light", true)).toBe(false);
  });

  it("sets the .dark class for the mode and reports a change", () => {
    mockSystemDark(true);
    expect(applyThemeMode("system")).toBe(true);
    expect(document.documentElement.classList.contains("dark")).toBe(true);
    expect(applyThemeMode("dark")).toBe(false);
    expect(applyThemeMode("light")).toBe(true);
    expect(document.documentElement.classList.contains("dark")).toBe(false);
  });

  it("follows the device while the user's mode is system, and only then", () => {
    const system = mockSystemDark(false);
    let mode: "system" | "light" = "system";
    const onChange = vi.fn();
    const stop = followSystemTheme(() => mode, onChange);

    system.set(true);
    expect(document.documentElement.classList.contains("dark")).toBe(true);
    expect(onChange).toHaveBeenCalledTimes(1);

    mode = "light";
    system.set(false);
    expect(document.documentElement.classList.contains("dark")).toBe(true);
    stop();
  });
});
