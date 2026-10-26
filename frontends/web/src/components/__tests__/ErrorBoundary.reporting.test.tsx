import { beforeEach, describe, expect, it, vi } from "vitest";
import { setRuntimeConfig } from "@/test/runtimeConfig"

// A boundary-caught error goes to every sink the build ships to. The runtime
// skips an error its SDK already recorded, so dual reporting is one record.

const mocks = vi.hoisted(() => ({
  captureException: vi.fn(),
  reportErrorToTelemetry: vi.fn(),
  crashReportingEnabled: true,
}));

vi.mock("@sentry/react", () => ({
  captureException: mocks.captureException,
  withErrorBoundary: (component: unknown) => component,
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

  it("reports to both sinks while they run in parallel", () => {
    setRuntimeConfig({ OTEL_ENDPOINT: ENDPOINT, TELEMETRY_SINKS: "sentry,clickstack" });
    const err = new Error("boom");

    reportCaughtError(err, "\n    at Foo");

    expect(mocks.captureException).toHaveBeenCalledWith(err, {
      contexts: { react: { componentStack: "\n    at Foo" } },
    });
    expect(mocks.reportErrorToTelemetry).toHaveBeenCalledWith(err, "\n    at Foo");
  });

  it("reports only to ClickStack once Sentry is out of the build", () => {
    setRuntimeConfig({ OTEL_ENDPOINT: ENDPOINT, TELEMETRY_SINKS: "clickstack" });

    reportCaughtError(new Error("boom"));

    expect(mocks.captureException).not.toHaveBeenCalled();
    expect(mocks.reportErrorToTelemetry).toHaveBeenCalledTimes(1);
  });

  it("reports nothing without crash-reporting consent", () => {
    setRuntimeConfig({ OTEL_ENDPOINT: ENDPOINT, TELEMETRY_SINKS: "sentry,clickstack" });
    mocks.crashReportingEnabled = false;

    reportCaughtError(new Error("boom"));

    expect(mocks.captureException).not.toHaveBeenCalled();
    expect(mocks.reportErrorToTelemetry).not.toHaveBeenCalled();
  });

  it("leaves production's current behaviour alone when nothing new is configured", () => {
    reportCaughtError(new Error("boom"));

    expect(mocks.captureException).toHaveBeenCalledTimes(1);
    // The telemetry module no-ops itself with the sink off; the boundary still
    // hands it the error and lets it decide.
    expect(mocks.reportErrorToTelemetry).toHaveBeenCalledTimes(1);
  });
});
