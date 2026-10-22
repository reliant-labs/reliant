/**
 * MobileAppearanceScreen renders MobileAppearancePanel (mobile-native)
 * behind the shared section header.
 */

import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

vi.mock("../../../services/settingsSync", () => ({
  settingsSync: {
    getSetting: vi.fn().mockReturnValue(""),
    getJSONSetting: vi.fn((_key, defaultValue) => defaultValue),
    setSetting: vi.fn().mockResolvedValue(undefined),
    isInitialized: vi.fn().mockReturnValue(true),
  },
  SETTINGS_KEYS: {
    THEME: "appearance.theme",
    FONT: "appearance.font",
    CHAT_FONT: "appearance.chat_font",
    EDITOR_FONT: "appearance.editor_font",
    FONT_SIZE: "appearance.font_size",
    CHAT_TIMELINE_VARIANT: "appearance.chat_timeline_variant",
    WORKFLOW_VIEWER_DEFAULT_MODE: "appearance.workflow_viewer_default_mode",
    SPAWN_DISPLAY_MODE: "appearance.spawn_display_mode",
    SHOW_HIDDEN_FILES: "appearance.show_hidden_files",
    COLOR_SCHEME: "appearance.color_scheme",
    EDITOR_SETTINGS: "appearance.editor_settings",
  },
}));

const { MobileAppearanceScreen } = await import("../MobileAppearanceScreen");

describe("MobileAppearanceScreen", () => {
  it("renders the mobile-native appearance panel content", async () => {
    render(<MobileAppearanceScreen onBack={vi.fn()} />);
    expect(await screen.findByText(/^theme$/i)).toBeInTheDocument();
    expect(screen.getByText(/^app font$/i)).toBeInTheDocument();
  });

  it("does not eagerly render Monaco editor settings (omitted on mobile)", async () => {
    render(<MobileAppearanceScreen onBack={vi.fn()} />);
    await screen.findByText(/^theme$/i);
    expect(screen.queryByText(/configure monaco editor/i)).not.toBeInTheDocument();
  });

  it("calls onBack from the header", async () => {
    const { default: userEvent } = await import("@testing-library/user-event");
    const onBack = vi.fn();
    render(<MobileAppearanceScreen onBack={onBack} />);
    await screen.findByText(/^theme$/i);
    await userEvent.setup().click(
      screen.getByRole("button", { name: /back to settings/i }),
    );
    expect(onBack).toHaveBeenCalled();
  });
});
