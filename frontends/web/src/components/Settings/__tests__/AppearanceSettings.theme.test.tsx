/**
 * Theme is a radiogroup with a System option, and its selection is exposed
 * to assistive tech (aria-checked), not shown by colour alone.
 */
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  stored: {} as Record<string, string>,
  setSetting: vi.fn(async (key: string, value: string) => {
    mocks.stored[key] = value;
  }),
}));
vi.mock("../../../services/settingsSync", () => ({
  settingsSync: {
    getSetting: (key: string, fallback: string) => mocks.stored[key] ?? fallback,
    setSetting: mocks.setSetting,
    setSettingIfChanged: vi.fn(async () => true),
    isInitialized: () => true,
  },
  SETTINGS_KEYS: {
    THEME: "appearance.theme",
    FONT: "appearance.font",
    CHAT_FONT: "appearance.chatFont",
    EDITOR_FONT: "appearance.editorFont",
    FONT_SIZE: "appearance.fontSize",
    CHAT_TIMELINE_VARIANT: "appearance.chatTimelineVariant",
    COLOR_SCHEME: "appearance.colorScheme",
  },
}));
vi.mock("../ColorSchemeSelector", () => ({ ColorSchemeSelector: () => null }));

const { AppearanceSettings } = await import("../AppearanceSettings");

describe("Appearance → Theme", () => {
  it("offers Light, Dark and System, with System selected when nothing was chosen", async () => {
    render(<AppearanceSettings />);
    const group = await screen.findByRole("radiogroup", { name: "Theme" });
    const radios = Array.from(group.querySelectorAll('[role="radio"]')).map((r) => r.textContent);
    expect(radios).toEqual(["Light", "Dark", "SystemFollows your device"]);
    expect(screen.getByRole("radio", { name: /system/i })).toHaveAttribute("aria-checked", "true");
    expect(mocks.setSetting).not.toHaveBeenCalled();
  });

  it("selects with the arrow keys and saves the choice", async () => {
    render(<AppearanceSettings />);
    const system = await screen.findByRole("radio", { name: /system/i });
    system.focus();
    await userEvent.keyboard("{ArrowLeft}");
    const dark = screen.getByRole("radio", { name: "Dark" });
    expect(dark).toHaveAttribute("aria-checked", "true");
    expect(dark).toHaveFocus();
    expect(mocks.setSetting).toHaveBeenCalledWith("appearance.theme", "dark");
    expect(document.documentElement.classList.contains("dark")).toBe(true);
  });
});
