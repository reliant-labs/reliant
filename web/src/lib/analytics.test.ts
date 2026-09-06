import { describe, it, expect, vi, beforeEach } from "vitest";

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

import * as Sentry from "@sentry/react";
import { trackEvent } from "./analytics";

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
