/**
 * MobileAppearancePanel reads and writes the same settingsSync keys desktop
 * AppearanceSettings uses, and shows the font-size PREFERENCE STEP rather
 * than a resolved pixel value.
 */

import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import {
  DEFAULT_FONT_SIZE,
  MOBILE_FONT_SIZE_MAP,
} from "../../../lib/rootFontSize";

const MOBILE_DIR = join(dirname(fileURLToPath(import.meta.url)), "..");

const mocks = vi.hoisted(() => ({
  setSetting: vi.fn().mockResolvedValue(undefined),
  // Mirrors the real guard: only writes when the value actually differs, so a
  // panel mount does not re-save preferences the user never touched.
  setSettingIfChanged: vi.fn().mockResolvedValue(true),
  getSetting: vi.fn((_key: string, fallback: unknown) => fallback),
}));

vi.mock("../../../services/settingsSync", () => ({
  settingsSync: {
    getSetting: mocks.getSetting,
    setSetting: mocks.setSetting,
    setSettingIfChanged: mocks.setSettingIfChanged,
    isInitialized: vi.fn().mockReturnValue(true),
  },
  SETTINGS_KEYS: {
    THEME: "appearance.theme",
    FONT: "appearance.font",
    CHAT_FONT: "appearance.chatFont",
    EDITOR_FONT: "appearance.editorFont",
    FONT_SIZE: "appearance.fontSize",
    CHAT_TIMELINE_VARIANT: "appearance.chatTimelineVariant",
    WORKFLOW_VIEWER_DEFAULT_MODE: "appearance.workflowViewerDefaultMode",
    SPAWN_DISPLAY_MODE: "appearance.spawnDisplayMode",
    SHOW_HIDDEN_FILES: "appearance.showHiddenFiles",
    COLOR_SCHEME: "appearance.colorScheme",
  },
}));

vi.mock("../../Settings/ColorSchemeSelector", () => ({
  ColorSchemeSelector: () => <div data-testid="color-scheme-selector" />,
}));

const { MobileAppearancePanel } = await import("../MobileAppearancePanel");

describe("MobileAppearancePanel", () => {
  it("renders the same sections desktop AppearanceSettings exposes", async () => {
    render(<MobileAppearancePanel />);
    expect(await screen.findByText(/^theme$/i)).toBeInTheDocument();
    expect(screen.getByTestId("color-scheme-selector")).toBeInTheDocument();
    expect(screen.getByText(/^app font$/i)).toBeInTheDocument();
    expect(screen.getByText(/font size/i)).toBeInTheDocument();
  });

  it("shows the font-size preference step, not a resolved pixel value", async () => {
    render(<MobileAppearancePanel />);
    await screen.findByText(/^theme$/i);
    // The panel shows the STEP's label ("Large" for the `lg` default), never
    // the resolved pixel value — 17px on mobile, per MOBILE_FONT_SIZE_MAP.
    // Derived from DEFAULT_FONT_SIZE so changing the default updates this in
    // one place instead of failing here.
    const labels: Record<string, string> = {
      xs: "Extra Small",
      sm: "Small",
      md: "Medium",
      lg: "Large",
      xl: "Extra Large",
    };
    expect(screen.getByText(labels[DEFAULT_FONT_SIZE])).toBeInTheDocument();
    expect(
      screen.queryByText(MOBILE_FONT_SIZE_MAP[DEFAULT_FONT_SIZE]),
    ).not.toBeInTheDocument();
  });

  it("writes the theme through the same THEME settingsSync key desktop uses", async () => {
    const { default: userEvent } = await import("@testing-library/user-event");
    render(<MobileAppearancePanel />);
    await screen.findByText(/^theme$/i);
    await userEvent.setup().click(screen.getByRole("button", { name: /^dark$/i }));
    await waitFor(() =>
      expect(mocks.setSetting).toHaveBeenCalledWith("appearance.theme", "dark"),
    );
  });

  it("writes show-hidden-files through useUIStore the same way desktop does", async () => {
    const { default: userEvent } = await import("@testing-library/user-event");
    render(<MobileAppearancePanel />);
    const toggle = await screen.findByRole("switch", { name: /show hidden files/i });
    // useUIStore defaults showHiddenFiles to true, so a click flips it false.
    expect(toggle).toHaveAttribute("aria-checked", "true");
    await userEvent.setup().click(toggle);
    // useUIStore is the real store (not mocked); flipping the switch should
    // not throw and should re-render with the new checked state.
    await waitFor(() => expect(toggle).toHaveAttribute("aria-checked", "false"));
  });

  it("does not eagerly render Monaco editor settings (omitted on mobile)", async () => {
    render(<MobileAppearancePanel />);
    await screen.findByText(/^theme$/i);
    expect(screen.queryByText(/configure monaco editor/i)).not.toBeInTheDocument();
  });

  it("sizes interactive targets at 44px, not rem-based h-9/h-10/h-11", () => {
    const panelSource = readFileSync(join(MOBILE_DIR, "MobileAppearancePanel.tsx"), "utf8");
    const rowSource = readFileSync(join(MOBILE_DIR, "MobileSettingsRow.tsx"), "utf8");
    expect(panelSource).not.toMatch(/\bh-(9|10|11)\b/);
    expect(rowSource).not.toMatch(/\bh-(9|10|11)\b/);
  });
});
