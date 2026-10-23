import { beforeEach, describe, expect, it, vi } from "vitest";

const analytics = vi.hoisted(() => ({
  clickstackDeliveryActive: vi.fn(() => true),
  trackEvent: vi.fn(),
}));
vi.mock("../analytics", () => analytics);

import { reportFirstLaunch } from "../appFirstLaunch";

const info = {
  installId: "install-1",
  installedAt: "2026-10-22T00:00:00.000Z",
  firstLaunchPending: true,
  platform: "darwin",
  arch: "arm64",
  version: "1.2.3",
};

function stubElectron(overrides: Record<string, unknown> = {}) {
  const api = {
    getInstallInfo: vi.fn(async () => info),
    markFirstLaunchReported: vi.fn(async () => {}),
    ...overrides,
  };
  (window as { electronAPI?: unknown }).electronAPI = api;
  return api;
}

beforeEach(() => {
  vi.clearAllMocks();
  analytics.clickstackDeliveryActive.mockReturnValue(true);
  delete (window as { electronAPI?: unknown }).electronAPI;
  vi.spyOn(navigator, "onLine", "get").mockReturnValue(true);
});

describe("reportFirstLaunch", () => {
  it("emits app_first_launch with the install facts, then marks it reported", async () => {
    const api = stubElectron();
    await reportFirstLaunch();
    expect(analytics.trackEvent).toHaveBeenCalledWith("app_first_launch", {
      installId: "install-1",
      installedAt: "2026-10-22T00:00:00.000Z",
      platform: "darwin",
      arch: "arm64",
      version: "1.2.3",
    });
    expect(api.markFirstLaunchReported).toHaveBeenCalledOnce();
  });

  it("does nothing once the install has reported", async () => {
    const api = stubElectron({ getInstallInfo: vi.fn(async () => ({ ...info, firstLaunchPending: false })) });
    await reportFirstLaunch();
    expect(analytics.trackEvent).not.toHaveBeenCalled();
    expect(api.markFirstLaunchReported).not.toHaveBeenCalled();
  });

  it("stays pending when the event cannot leave: offline", async () => {
    const api = stubElectron();
    vi.spyOn(navigator, "onLine", "get").mockReturnValue(false);
    await reportFirstLaunch();
    expect(analytics.trackEvent).not.toHaveBeenCalled();
    expect(api.markFirstLaunchReported).not.toHaveBeenCalled();
  });

  it("stays pending when analytics or the clickstack sink is off", async () => {
    const api = stubElectron();
    analytics.clickstackDeliveryActive.mockReturnValue(false);
    await reportFirstLaunch();
    expect(analytics.trackEvent).not.toHaveBeenCalled();
    expect(api.markFirstLaunchReported).not.toHaveBeenCalled();
  });

  it("is a no-op in a plain browser", async () => {
    await reportFirstLaunch();
    expect(analytics.trackEvent).not.toHaveBeenCalled();
  });
});
