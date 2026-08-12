/**
 * MobileNotificationsScreen renders MobileNotificationsPanel (mobile-native)
 * behind the shared section header.
 */

import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";

vi.mock("../../../store/notificationStore", () => ({
  useNotificationStore: () => ({
    notificationsEnabled: true,
    soundEnabled: true,
    notifyWhenUnfocused: true,
    notifyWhenDifferentChat: true,
    notifyAlways: false,
    permission: "granted",
    isSupported: true,
    initialized: true,
    initialize: vi.fn(),
    setNotificationsEnabled: vi.fn(),
    setSoundEnabled: vi.fn(),
    setNotifyWhenUnfocused: vi.fn(),
    setNotifyWhenDifferentChat: vi.fn(),
    setNotifyAlways: vi.fn(),
    requestPermission: vi.fn(),
    refreshPermission: vi.fn(),
  }),
  getNotificationSoundOptions: () => ({ enabled: true }),
}));

const { MobileNotificationsScreen } = await import("../MobileNotificationsScreen");

describe("MobileNotificationsScreen", () => {
  it("renders the mobile-native notification panel content", async () => {
    render(<MobileNotificationsScreen onBack={vi.fn()} />);
    expect(await screen.findByText(/browser permission/i)).toBeInTheDocument();
    expect(screen.getByText(/desktop notifications/i)).toBeInTheDocument();
  });

  it("calls onBack from the header", async () => {
    const { default: userEvent } = await import("@testing-library/user-event");
    const onBack = vi.fn();
    render(<MobileNotificationsScreen onBack={onBack} />);
    await userEvent.setup().click(
      screen.getByRole("button", { name: /back to settings/i }),
    );
    expect(onBack).toHaveBeenCalled();
  });
});
