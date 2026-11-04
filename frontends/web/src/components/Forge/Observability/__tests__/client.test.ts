import { describe, expect, it } from "vitest";

import { NO_CLIENT } from "@/services/controlPlane/observability/client";

import { clientLabel, clientTitle, osLabel, shortVersion } from "../Client";

describe("client labels", () => {
  it("names a browser by its major version and an OS by major.minor", () => {
    const chrome = { ...NO_CLIENT, browserName: "Chrome", browserVersion: "156.0.0", osName: "macOS", osVersion: "15.1.0", deviceType: "desktop" as const };
    expect(clientLabel(chrome)).toBe("Chrome 156");
    expect(osLabel(chrome)).toBe("macOS 15.1");
    expect(shortVersion("26.6.1", 2)).toBe("26.6");
  });

  it("names a desktop renderer by the app's version, with its Electron in the title", () => {
    const desktop = { ...NO_CLIENT, browserName: "Electron", browserVersion: "41.10.7", osName: "macOS", desktopVersion: "1.8.16", chromiumVersion: "146.0.7680.216" };
    expect(clientLabel(desktop)).toBe("Desktop app 1.8.16");
    const title = clientTitle(desktop);
    expect(title).toContain("Desktop app 1.8.16 · Electron 41.10.7");
    expect(title).toContain("Chromium 146.0.7680.216");
    expect(title).toContain("derived by the platform from the request's User-Agent");
  });

  it("is empty for an unknown client, and says the viewport when the page reported it", () => {
    expect(clientLabel(NO_CLIENT)).toBe("");
    expect(osLabel(NO_CLIENT)).toBe("");
    expect(clientTitle({ ...NO_CLIENT, browserName: "Firefox", viewportWidth: 1440, viewportHeight: 900, screenWidth: 2560, screenHeight: 1440 })).toContain(
      "Viewport 1440×900 · screen 2560×1440"
    );
  });
});
