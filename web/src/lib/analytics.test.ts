import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// The condition under test is PRODUCTION'S CURRENT ONE: Sentry is live and
// VITE_STATSIG_CLIENT_KEY is unset. Before this change that combination
// discarded the entire onboarding funnel — every trackEvent call returned early
// because there was no Statsig client to log to, so the fully-written
// instrumentation in OnboardingFlow/analytics.ts produced nothing at all.
//
// These tests pin that the two sinks are independent: no Statsig key must still
// mean the event reaches Sentry.

vi.mock("@sentry/react", () => ({
  addBreadcrumb: vi.fn(),
  captureMessage: vi.fn(),
  withScope: vi.fn((fn: (scope: unknown) => void) =>
    fn({
      setLevel: vi.fn(),
      setTag: vi.fn(),
      setContext: vi.fn(),
    }),
  ),
}));

vi.mock("../store/privacyStore", () => ({
  getPrivacySettings: () => ({ analyticsEnabled: true }),
}));

// isDev must be false or the whole pipe is intentionally inert.
vi.mock("./constants", () => ({ isDev: false }));

vi.mock("@statsig/js-client", () => ({
  StatsigClient: vi.fn(),
}));

vi.mock("@reliantlabs/forge-web-runtime", () => ({
  track: vi.fn(),
}));

import * as Sentry from "@sentry/react";
import { track } from "@reliantlabs/forge-web-runtime";
import { toSnakeCaseKey, trackEvent } from "./analytics";

describe("trackEvent with no Statsig key (production's current state)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    // Explicitly unset, which is what every environment has today.
    vi.stubEnv("VITE_STATSIG_CLIENT_KEY", "");
  });

  it("still delivers the event to Sentry as a breadcrumb", () => {
    trackEvent("onboarding_flow_step_viewed", { step: "compute" });

    expect(Sentry.addBreadcrumb).toHaveBeenCalledWith(
      expect.objectContaining({
        category: "funnel",
        message: "onboarding_flow_step_viewed",
        data: { step: "compute" },
      }),
    );
  });

  it("emits an explicit Sentry message for funnel-defining events, so an alert rule can match", () => {
    trackEvent("onboarding_completed", { provider: "github" });

    expect(Sentry.captureMessage).toHaveBeenCalledWith(
      "funnel: onboarding_completed",
      "info",
    );
  });

  it("does NOT emit a message for ordinary events, which would only add noise", () => {
    trackEvent("onboarding_flow_step_viewed", { step: "compute" });

    expect(Sentry.addBreadcrumb).toHaveBeenCalled();
    expect(Sentry.captureMessage).not.toHaveBeenCalled();
  });

  it("coerces metadata values to strings", () => {
    trackEvent("onboarding_flow_step_completed", {
      step: "compute",
      duration_ms: 1234,
      skipped: false,
    });

    expect(Sentry.addBreadcrumb).toHaveBeenCalledWith(
      expect.objectContaining({
        data: { step: "compute", duration_ms: "1234", skipped: "false" },
      }),
    );
  });

  it("never throws when Sentry itself fails — analytics must not break the app", () => {
    vi.mocked(Sentry.addBreadcrumb).mockImplementationOnce(() => {
      throw new Error("sentry exploded");
    });

    expect(() => trackEvent("onboarding_flow_started")).not.toThrow();
  });
});

// The sinks flag (telemetrySinks.ts) decides which of Sentry and ClickStack an
// event reaches. Both run in parallel until web Sentry is removed.
describe("VITE_TELEMETRY_SINKS", () => {
  const ENDPOINT = "https://ingest.example.dev/k/rbk_x";

  beforeEach(() => {
    vi.clearAllMocks();
    vi.unstubAllEnvs();
    vi.stubEnv("VITE_STATSIG_CLIENT_KEY", "");
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("sends to both Sentry and ClickStack for `sentry,clickstack`", () => {
    vi.stubEnv("VITE_OTEL_ENDPOINT", ENDPOINT);
    vi.stubEnv("VITE_TELEMETRY_SINKS", "sentry,clickstack");

    trackEvent("onboarding_completed", { provider: "github" });

    expect(track).toHaveBeenCalledWith("onboarding_completed", { provider: "github" });
    expect(Sentry.addBreadcrumb).toHaveBeenCalled();
    expect(Sentry.captureMessage).toHaveBeenCalledWith("funnel: onboarding_completed", "info");
  });

  it("sends only to ClickStack for `clickstack`", () => {
    vi.stubEnv("VITE_OTEL_ENDPOINT", ENDPOINT);
    vi.stubEnv("VITE_TELEMETRY_SINKS", "clickstack");

    trackEvent("onboarding_completed");

    expect(track).toHaveBeenCalledWith("onboarding_completed", {});
    expect(Sentry.addBreadcrumb).not.toHaveBeenCalled();
    expect(Sentry.captureMessage).not.toHaveBeenCalled();
  });

  it("sends only to Sentry for `sentry`", () => {
    vi.stubEnv("VITE_OTEL_ENDPOINT", ENDPOINT);
    vi.stubEnv("VITE_TELEMETRY_SINKS", "sentry");

    trackEvent("onboarding_completed");

    expect(track).not.toHaveBeenCalled();
    expect(Sentry.addBreadcrumb).toHaveBeenCalled();
  });

  it("changes nothing for a build that configures no endpoint: Sentry only", () => {
    trackEvent("onboarding_completed");

    expect(track).not.toHaveBeenCalled();
    expect(Sentry.addBreadcrumb).toHaveBeenCalled();
  });

  it("still reaches ClickStack when Sentry fails", () => {
    vi.stubEnv("VITE_OTEL_ENDPOINT", ENDPOINT);
    vi.stubEnv("VITE_TELEMETRY_SINKS", "sentry,clickstack");
    vi.mocked(Sentry.addBreadcrumb).mockImplementationOnce(() => {
      throw new Error("sentry exploded");
    });

    expect(() => trackEvent("onboarding_flow_started")).not.toThrow();
    expect(track).toHaveBeenCalledWith("onboarding_flow_started", {});
  });

  it("never throws when the runtime does", () => {
    vi.stubEnv("VITE_OTEL_ENDPOINT", ENDPOINT);
    vi.stubEnv("VITE_TELEMETRY_SINKS", "clickstack");
    vi.mocked(track).mockImplementationOnce(() => {
      throw new Error("runtime exploded");
    });

    expect(() => trackEvent("onboarding_flow_started")).not.toThrow();
  });
});

// The runtime keeps keys matching [a-z0-9_.]{1,64} and silently drops the rest;
// reliant's callers use camelCase (stepId, contentLength), which would vanish.
// Value and key-count bounding (20 keys, 256 chars, credential redaction) is the
// runtime's and is covered by its own tests.
describe("ClickStack prop keys", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.stubEnv("VITE_OTEL_ENDPOINT", "https://ingest.example.dev/k/rbk_x");
    vi.stubEnv("VITE_TELEMETRY_SINKS", "clickstack");
    vi.stubEnv("VITE_STATSIG_CLIENT_KEY", "");
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("snake_cases camelCase keys and keeps native value types", () => {
    trackEvent("tour_step_viewed", { stepId: "welcome", stepsCompleted: 2, hasAttachments: false });

    expect(track).toHaveBeenCalledWith("tour_step_viewed", {
      step_id: "welcome",
      steps_completed: 2,
      has_attachments: false,
    });
  });

  it("leaves already-conforming keys alone", () => {
    trackEvent("onboarding_flow_step_completed", { step: "compute", duration_ms: 1234 });

    expect(track).toHaveBeenCalledWith("onboarding_flow_step_completed", {
      step: "compute",
      duration_ms: 1234,
    });
  });

  it.each([
    ["stepId", "step_id"],
    ["isGitRepo", "is_git_repo"],
    ["contentLength", "content_length"],
    ["code_source", "code_source"],
    ["a-b c", "a_b_c"],
  ])("toSnakeCaseKey(%s) = %s", (input, expected) => {
    expect(toSnakeCaseKey(input)).toBe(expected);
  });

  it("sends nothing when analytics consent is off", async () => {
    vi.resetModules();
    vi.doMock("../store/privacyStore", () => ({
      getPrivacySettings: () => ({ analyticsEnabled: false }),
    }));
    vi.doMock("./constants", () => ({ isDev: false }));
    vi.doMock("@reliantlabs/forge-web-runtime", () => ({ track: vi.fn() }));
    const runtime = await import("@reliantlabs/forge-web-runtime");

    const { trackEvent: gated } = await import("./analytics");
    gated("onboarding_completed", { provider: "github" });

    expect(runtime.track).not.toHaveBeenCalled();
  });

  it("sends nothing in dev", async () => {
    vi.resetModules();
    vi.doMock("../store/privacyStore", () => ({
      getPrivacySettings: () => ({ analyticsEnabled: true }),
    }));
    vi.doMock("./constants", () => ({ isDev: true }));
    vi.doMock("@reliantlabs/forge-web-runtime", () => ({ track: vi.fn() }));
    const runtime = await import("@reliantlabs/forge-web-runtime");

    const { trackEvent: gated } = await import("./analytics");
    gated("onboarding_completed");

    expect(runtime.track).not.toHaveBeenCalled();
  });
});

describe("privacy and dev gating", () => {
  beforeEach(() => vi.clearAllMocks());

  it("sends nothing when the user has disabled analytics", async () => {
    vi.resetModules();
    vi.doMock("../store/privacyStore", () => ({
      getPrivacySettings: () => ({ analyticsEnabled: false }),
    }));
    vi.doMock("./constants", () => ({ isDev: false }));

    const { trackEvent: gated } = await import("./analytics");
    gated("onboarding_completed");

    expect(Sentry.addBreadcrumb).not.toHaveBeenCalled();
    expect(Sentry.captureMessage).not.toHaveBeenCalled();
  });

  it("sends nothing in dev", async () => {
    vi.resetModules();
    vi.doMock("../store/privacyStore", () => ({
      getPrivacySettings: () => ({ analyticsEnabled: true }),
    }));
    vi.doMock("./constants", () => ({ isDev: true }));

    const { trackEvent: gated } = await import("./analytics");
    gated("onboarding_completed");

    expect(Sentry.addBreadcrumb).not.toHaveBeenCalled();
  });
});
