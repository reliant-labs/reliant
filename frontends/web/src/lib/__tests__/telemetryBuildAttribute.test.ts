/**
 * `reliant.build` reaches the SDK through forge's web runtime.
 *
 * The app stamps its build with the runtime's public setTelemetryAttributes,
 * never by importing the SDK itself: the runtime owns the SDK client, and an
 * SDK instance the app imports on its own is not the one running, so what is
 * set on it goes nowhere (forge #2310). This runs the REAL runtime with only
 * the SDK underneath it stubbed, and checks the build lands on the client the
 * runtime started, in the same call as the runtime's own app.client, so the
 * page load's first spans carry it too.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { setRuntimeConfig } from "@/test/runtimeConfig";

vi.mock("../../hooks/privacy-queries", () => ({
  privacyConsent: () => ({ crashReportingEnabled: true, analyticsEnabled: true }),
  privacyWritesSettled: async () => {},
  subscribePrivacyConsent: () => () => {},
}));
vi.mock("../constants", () => ({ isDev: false }));
vi.mock("../appBuild", () => ({
  appBuild: () => ({ version: "1.8.1-build.7", commit: "c", ref: "r", built: "t" }),
}));

import { Rum } from "@hyperdx/otel-web";
import { initTelemetry } from "../telemetry";

// forge's runtime is a prebuilt dependency, so vitest does not rewrite its
// imports and a vi.mock of the SDK would not reach it. The runtime drives
// @hyperdx/otel-web's Rum, and this app's devDependency on it is the same
// version, so both load the same instance: it is stubbed in place.
const sdk = {
  init: vi.spyOn(Rum, "init").mockImplementation(() => {}),
  setGlobalAttributes: vi.spyOn(Rum, "setGlobalAttributes").mockImplementation(() => {}),
};
vi.spyOn(Rum, "addAction").mockImplementation(() => {});
vi.spyOn(Rum, "recordException").mockImplementation(() => {});

beforeEach(() => {
  setRuntimeConfig({ OTEL_ENDPOINT: "https://ingest.example.dev/k/rbk_x" });
});

describe("reliant.build on browser telemetry", () => {
  it("lands on the SDK the runtime started, with the runtime's app.client", async () => {
    expect(await initTelemetry()).toBe(true);
    expect(sdk.init).toHaveBeenCalledTimes(1);

    const stamped = sdk.setGlobalAttributes.mock.calls.map(([attrs]) => attrs as Record<string, string>);
    const withClient = stamped.find((attrs) => "app.client" in attrs);
    expect(withClient).toMatchObject({ "app.client": "web", "reliant.build": "1.8.1-build.7" });
  });
});
