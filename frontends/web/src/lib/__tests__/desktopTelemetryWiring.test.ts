/**
 * The packaged desktop renderer can report errors.
 *
 * Web Sentry was the desktop window's other error sink; with it gone, ClickStack
 * is the only one, so this pins the whole path end to end with nothing between
 * the app and the SDK mocked except the SDK itself:
 *
 *   electron/release.config.json's `renderer` block (the config.js the
 *   packaged app's app:// handler serves, projected from control-plane's KCL)
 *   → this app's initTelemetry() → forge's REAL initBrowserTelemetry
 *   → HyperDX init pointed at the keyed ingest, with the error
 *     instrumentation on and reportException wired to recordException
 *   → and the Content-Security-Policy lets the renderer reach that ingest.
 *
 * In prod (2026-10-29) desktop 1.8.7 sessions did reach ClickStack through
 * this path: their `errors`-scope spans arrived with forge.frontend=desktop,
 * and the only errors they recorded were ResizeObserver notices (a String,
 * not an Error, so not an exception and rightly not in error_events).
 */

import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { setRuntimeConfig } from "@/test/runtimeConfig";
import { runtimeConnectScript } from "../../../vite-plugin-csp";

vi.mock("../../hooks/privacy-queries", () => ({
  privacyConsent: () => ({ crashReportingEnabled: true, analyticsEnabled: true }),
  subscribePrivacyConsent: () => () => {},
}));
vi.mock("../constants", () => ({ isDev: false }));
vi.mock("../appFirstLaunch", () => ({ reportFirstLaunch: async () => {} }));

import HyperDX from "@hyperdx/browser";
import { initTelemetry, reportErrorToTelemetry } from "../telemetry";

// forge's runtime is a prebuilt dependency, so vitest does not rewrite its
// imports and a vi.mock of the SDK would not reach it. Both load the same
// @hyperdx/browser instance, so the SDK is stubbed in place instead: the
// runtime itself runs for real.
type Client = typeof HyperDX;
function findClient(mod: unknown): Client {
  let cur = mod as { default?: unknown; init?: unknown };
  while (typeof cur.init !== "function" && cur.default) cur = cur.default as typeof cur;
  return cur as unknown as Client;
}
const client = findClient(HyperDX);
const sdk = {
  init: vi.spyOn(client, "init").mockImplementation(() => {}),
  recordException: vi.spyOn(client, "recordException").mockImplementation(() => {}),
};
vi.spyOn(client, "setGlobalAttributes").mockImplementation(() => {});
vi.spyOn(client, "addAction").mockImplementation(() => {});

const RELEASE = JSON.parse(
  readFileSync(resolve(__dirname, "../../../../../electron/release.config.json"), "utf8"),
) as { renderer: Record<string, string> };

/** The connect-src the packaged renderer's CSP bootstrap appends for a document. */
function connectSrc(config: Record<string, unknown>): string {
  window.__FORGE_CONFIG__ = config;
  new Function(runtimeConnectScript())();
  const metas = document.head.querySelectorAll('meta[http-equiv="Content-Security-Policy"]');
  const policy = metas[metas.length - 1]?.getAttribute("content") ?? "";
  metas.forEach((m) => m.remove());
  return policy;
}

beforeEach(() => {
  setRuntimeConfig(RELEASE.renderer);
  (window as { electronAPI?: unknown }).electronAPI = { getVersion: async () => "1.8.7" };
});

describe("the packaged desktop renderer's error path", () => {
  it("ships a keyed ingest endpoint in its renderer document", () => {
    expect(RELEASE.renderer.OTEL_ENDPOINT).toMatch(/^https:\/\/[^/]+\/k\/rbk_[a-z2-7]+$/);
  });

  it("lets the renderer's CSP reach that ingest", () => {
    const origin = new URL(RELEASE.renderer.OTEL_ENDPOINT).origin;
    expect(connectSrc(RELEASE.renderer)).toContain(origin);
  });

  it("starts the SDK on the keyed ingest as the desktop frontend, error capture on, and reports a caught error to it", async () => {
    expect(await initTelemetry()).toBe(true);

    expect(sdk.init).toHaveBeenCalledTimes(1);
    const options = sdk.init.mock.calls[0][0] as {
      url: string;
      otelResourceAttributes: Record<string, string>;
      instrumentations?: { errors?: boolean };
    };
    expect(options.url).toBe(RELEASE.renderer.OTEL_ENDPOINT);
    expect(options.otelResourceAttributes["forge.frontend"]).toBe("desktop");
    // The SDK's own uncaught-error / rejection / console.error capture stays on.
    expect(options.instrumentations?.errors).not.toBe(false);

    const err = new Error("boom");
    reportErrorToTelemetry(err, "\n    at Foo");
    expect(sdk.recordException).toHaveBeenCalledWith(err, expect.objectContaining({ componentStack: "\n    at Foo" }));
  });
});
