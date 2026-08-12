/**
 * MobileNotificationsPanel renders and writes through the same
 * useNotificationStore actions desktop NotificationSettings uses.
 */

import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const MOBILE_DIR = join(dirname(fileURLToPath(import.meta.url)), "..");

const mocks = vi.hoisted(() => ({
  setNotificationsEnabled: vi.fn().mockResolvedValue(undefined),
  setSoundEnabled: vi.fn().mockResolvedValue(undefined),
  setNotifyWhenUnfocused: vi.fn().mockResolvedValue(undefined),
  setNotifyWhenDifferentChat: vi.fn().mockResolvedValue(undefined),
  setNotifyAlways: vi.fn().mockResolvedValue(undefined),
  requestPermission: vi.fn().mockResolvedValue("granted"),
  refreshPermission: vi.fn(),
  initialize: vi.fn(),
}));

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
    initialize: mocks.initialize,
    setNotificationsEnabled: mocks.setNotificationsEnabled,
    setSoundEnabled: mocks.setSoundEnabled,
    setNotifyWhenUnfocused: mocks.setNotifyWhenUnfocused,
    setNotifyWhenDifferentChat: mocks.setNotifyWhenDifferentChat,
    setNotifyAlways: mocks.setNotifyAlways,
    requestPermission: mocks.requestPermission,
    refreshPermission: mocks.refreshPermission,
  }),
  getNotificationSoundOptions: () => ({ enabled: true }),
}));

vi.mock("../../../lib/notifications", () => ({
  showTestNotification: vi.fn(),
}));

const { MobileNotificationsPanel } = await import("../MobileNotificationsPanel");

describe("MobileNotificationsPanel", () => {
  it("renders the same settings desktop NotificationSettings exposes", () => {
    render(<MobileNotificationsPanel />);
    expect(screen.getByText(/browser permission/i)).toBeInTheDocument();
    expect(screen.getByText(/permission granted/i)).toBeInTheDocument();
    expect(screen.getByText(/desktop notifications/i)).toBeInTheDocument();
    expect(screen.getByText(/notification sound/i)).toBeInTheDocument();
  });

  it("writes through the same setSoundEnabled action desktop uses", async () => {
    const { default: userEvent } = await import("@testing-library/user-event");
    render(<MobileNotificationsPanel />);
    await userEvent.setup().click(
      screen.getByRole("switch", { name: /notification sound/i }),
    );
    expect(mocks.setSoundEnabled).toHaveBeenCalledWith(false);
  });

  it("writes through the same setNotifyAlways action desktop uses", async () => {
    const { default: userEvent } = await import("@testing-library/user-event");
    render(<MobileNotificationsPanel />);
    await userEvent.setup().click(
      screen.getByRole("switch", { name: /always notify/i }),
    );
    expect(mocks.setNotifyAlways).toHaveBeenCalledWith(true);
  });

  it("sizes interactive targets at 44px, not rem-based h-9/h-10/h-11", () => {
    const source = readFileSync(join(MOBILE_DIR, "MobileNotificationsPanel.tsx"), "utf8");
    expect(source).not.toMatch(/\bh-(9|10|11)\b/);
  });
});
