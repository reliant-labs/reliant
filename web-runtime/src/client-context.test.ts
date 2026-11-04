// The contract of what a page reports about its client: client hints and the
// desktop bridge sanitized into resource attributes, the viewport and screen
// into span attributes, and nothing malformed passed through.
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  clientResourceAttributes,
  viewportAttributes,
  VIEWPORT_DEBOUNCE_MS,
  watchViewport,
} from "./client-context.js";

describe("clientResourceAttributes", () => {
  it("reports Chromium's low-entropy client hints without the GREASE brand", () => {
    expect(
      clientResourceAttributes({
        userAgentData: {
          brands: [
            { brand: "Google Chrome", version: "131" },
            { brand: "Chromium", version: "131" },
            { brand: "Not_A Brand", version: "24" },
          ],
          mobile: false,
          platform: "macOS",
        },
      }),
    ).toEqual({
      "browser.brands": "Google Chrome 131, Chromium 131",
      "browser.platform": "macOS",
      "browser.mobile": "false",
    });
  });

  it("drops GREASE in every spelling Chromium has used", () => {
    for (const grease of [
      "Not A;Brand",
      "Not)A;Brand",
      "Not.A/Brand",
      "Not_A Brand",
    ]) {
      expect(
        clientResourceAttributes({
          userAgentData: { brands: [{ brand: grease, version: "99" }] },
        }),
      ).toEqual({});
    }
  });

  it("drops anything malformed instead of passing it on", () => {
    expect(
      clientResourceAttributes({
        userAgentData: {
          brands: [
            { brand: "Evil, Inc", version: "1" },
            { brand: "x".repeat(200), version: "1" },
            { brand: "Ok", version: "1; DROP" },
            { brand: 42, version: "1" },
            "not an object",
          ],
          mobile: "yes",
          platform: "Linux\u0000\n",
        },
        desktop: {
          appVersion: "1.8.16\n<script>",
          electronVersion: 33,
          chromiumVersion: "",
        },
      }),
    ).toEqual({});
  });

  it("reports a desktop app's versions from the preload bridge", () => {
    expect(
      clientResourceAttributes({
        desktop: {
          appVersion: "1.8.16-dev+7d787d80",
          electronVersion: "33.2.1",
          chromiumVersion: "130.0.6723.137",
        },
      }),
    ).toEqual({
      "app.desktop.version": "1.8.16-dev+7d787d80",
      "app.desktop.electron.version": "33.2.1",
      "app.desktop.chromium.version": "130.0.6723.137",
    });
  });

  it("is empty outside Chromium and outside a desktop app", () => {
    expect(clientResourceAttributes({})).toEqual({});
    expect(
      clientResourceAttributes({ userAgentData: null, desktop: "1.0" }),
    ).toEqual({});
  });

  it("never reports a structured device claim: those are derived at ingest", () => {
    const attrs = clientResourceAttributes({
      userAgentData: {
        brands: [{ brand: "Chromium", version: "131" }],
        mobile: true,
        platform: "Android",
      },
    });
    for (const key of Object.keys(attrs)) {
      expect(key).not.toMatch(/^(user_agent\.|os\.|device\.|reliant\.)/);
    }
  });
});

describe("viewportAttributes", () => {
  it("reports the viewport and the screen in CSS pixels", () => {
    expect(
      viewportAttributes({
        innerWidth: 1440,
        innerHeight: 900.4,
        screen: { width: 2560, height: 1440 },
      }),
    ).toEqual({
      "app.viewport.width": "1440",
      "app.viewport.height": "900",
      "app.screen.width": "2560",
      "app.screen.height": "1440",
    });
  });

  it("keeps a dimension only with its pair, and only when it is plausible", () => {
    expect(
      viewportAttributes({
        innerWidth: 1440,
        innerHeight: Number.NaN,
        screen: { width: 1e9, height: 1080 },
      }),
    ).toEqual({});
    expect(viewportAttributes({ innerWidth: 0, innerHeight: 0 })).toEqual({});
  });
});

describe("watchViewport", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  function fakeWindow() {
    const target = new EventTarget();
    return Object.assign(target, {
      innerWidth: 1200,
      innerHeight: 800,
      screen: { width: 1920, height: 1080 },
    }) as unknown as Window & { innerWidth: number; innerHeight: number };
  }

  it("stamps the size at once, then once per settled resize to a new size", () => {
    vi.useFakeTimers();
    const win = fakeWindow();
    const stamp = vi.fn();
    const stop = watchViewport(stamp, win);
    expect(stamp).toHaveBeenCalledTimes(1);
    expect(stamp).toHaveBeenLastCalledWith(
      expect.objectContaining({ "app.viewport.width": "1200" }),
    );

    // A drag fires many resizes; only the settled size is stamped.
    for (const w of [1100, 1000, 900]) {
      (win as { innerWidth: number }).innerWidth = w;
      win.dispatchEvent(new Event("resize"));
      vi.advanceTimersByTime(VIEWPORT_DEBOUNCE_MS / 4);
    }
    vi.advanceTimersByTime(VIEWPORT_DEBOUNCE_MS);
    expect(stamp).toHaveBeenCalledTimes(2);
    expect(stamp).toHaveBeenLastCalledWith(
      expect.objectContaining({ "app.viewport.width": "900" }),
    );

    // A resize back to the same size stamps nothing new.
    win.dispatchEvent(new Event("resize"));
    vi.advanceTimersByTime(VIEWPORT_DEBOUNCE_MS * 2);
    expect(stamp).toHaveBeenCalledTimes(2);

    stop();
    (win as { innerWidth: number }).innerWidth = 700;
    win.dispatchEvent(new Event("resize"));
    vi.advanceTimersByTime(VIEWPORT_DEBOUNCE_MS * 2);
    expect(stamp).toHaveBeenCalledTimes(2);
  });

  it("never lets a throwing sink break the page", () => {
    const win = fakeWindow();
    expect(() =>
      watchViewport(() => {
        throw new Error("sdk");
      }, win),
    ).not.toThrow();
  });
});
