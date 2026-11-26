/**
 * Opening the colour-scheme picker applies the stored scheme but never saves
 * it: it used to write the value it had just read back on every open
 * (BatchUpsertSettings with no user intent, reliant #3458). A pick saves.
 */
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

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
    isInitialized: () => true,
  },
  SETTINGS_KEYS: { COLOR_SCHEME: "appearance.colorScheme" },
}));

const { ColorSchemeSelector } = await import("../ColorSchemeSelector");

describe("ColorSchemeSelector", () => {
  beforeEach(() => {
    mocks.stored = { "appearance.colorScheme": "teal" };
    mocks.setSetting.mockClear();
  });

  it("applies the stored scheme on open without saving it", async () => {
    render(<ColorSchemeSelector />);
    expect(await screen.findByRole("radio", { name: /teal/i })).toHaveAttribute("aria-checked", "true");
    expect(document.documentElement.getAttribute("data-color-scheme")).toBeTruthy();
    expect(mocks.setSetting).not.toHaveBeenCalled();
  });

  it("saves the scheme the user picks", async () => {
    render(<ColorSchemeSelector />);
    await userEvent.click(await screen.findByRole("radio", { name: /slate/i }));
    expect(screen.getByRole("radio", { name: /slate/i })).toHaveAttribute("aria-checked", "true");
    expect(mocks.setSetting).toHaveBeenCalledTimes(1);
    expect(mocks.setSetting).toHaveBeenCalledWith("appearance.colorScheme", "slate");
  });

  it("saves a scheme picked with the arrow keys", async () => {
    render(<ColorSchemeSelector />);
    const teal = await screen.findByRole("radio", { name: /teal/i });
    teal.focus();
    await userEvent.keyboard("{ArrowRight}");
    expect(mocks.setSetting).toHaveBeenCalledTimes(1);
    expect(mocks.setSetting).toHaveBeenCalledWith("appearance.colorScheme", "purple");
  });
});
