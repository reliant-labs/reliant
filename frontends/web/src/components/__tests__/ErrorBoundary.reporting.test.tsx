import { beforeEach, describe, expect, it, vi } from "vitest";
import { setRuntimeConfig } from "@/test/runtimeConfig"

// A boundary-caught error goes to browser telemetry. The runtime skips an
// error its SDK already recorded, so it is one record.

const mocks = vi.hoisted(() => ({
  reportErrorToTelemetry: vi.fn(),
  crashReportingEnabled: true,
}));

vi.mock("../../lib/telemetry", () => ({
  reportErrorToTelemetry: mocks.reportErrorToTelemetry,
}));
vi.mock("../../hooks/privacy-queries", () => ({
  privacyConsent: () => ({
    crashReportingEnabled: mocks.crashReportingEnabled,
    analyticsEnabled: true,
  }),
}));

import { reportCaughtError } from "../ErrorBoundary";

const ENDPOINT = "https://ingest.example.dev/k/rbk_x";

describe("reportCaughtError", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mocks.crashReportingEnabled = true;
  });

  it("hands the error and its component stack to telemetry", () => {
    setRuntimeConfig({ OTEL_ENDPOINT: ENDPOINT });
    const err = new Error("boom");

    reportCaughtError(err, "\n    at Foo");

    expect(mocks.reportErrorToTelemetry).toHaveBeenCalledWith(err, "\n    at Foo");
  });

  it("reports nothing without crash-reporting consent", () => {
    setRuntimeConfig({ OTEL_ENDPOINT: ENDPOINT });
    mocks.crashReportingEnabled = false;

    reportCaughtError(new Error("boom"));

    expect(mocks.reportErrorToTelemetry).not.toHaveBeenCalled();
  });

  it("leaves the no-endpoint decision to the telemetry module", () => {
    reportCaughtError(new Error("boom"));

    // The telemetry module no-ops itself with no endpoint; the boundary still
    // hands it the error and lets it decide.
    expect(mocks.reportErrorToTelemetry).toHaveBeenCalledTimes(1);
  });
});
