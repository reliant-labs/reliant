import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { resetShellForTests } from "./shell";
import { setRuntimeConfig } from "@/test/runtimeConfig";

// trackEvent reaches ClickStack as a `product.<event>` span when the build
// ships browser telemetry. These tests pin that, and that the user's consent
// and dev mode gate it.

vi.mock("../hooks/privacy-queries", () => ({
  privacyConsent: () => ({ analyticsEnabled: true }),
}));

// isDev must be false or the whole pipe is intentionally inert.
vi.mock("./constants", () => ({ isDev: false }));

vi.mock("@reliantlabs/forge-web-runtime", () => ({
  track: vi.fn(),
}));

import { track } from "@reliantlabs/forge-web-runtime";
import { toSnakeCaseKey, trackEvent } from "./analytics";

// OTEL_ENDPOINT decides whether an event goes anywhere (telemetryEndpoint.ts).
describe("OTEL_ENDPOINT", () => {
  const ENDPOINT = "https://ingest.example.dev/k/rbk_x";

  beforeEach(() => {
    vi.clearAllMocks();
    vi.unstubAllEnvs();
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("sends the event to ClickStack when an endpoint is configured", () => {
    setRuntimeConfig({ OTEL_ENDPOINT: ENDPOINT });

    trackEvent("onboarding_completed", { provider: "github" });

    expect(track).toHaveBeenCalledWith("product.onboarding_completed", { surface: "reliant", provider: "github" });
  });

  it("sends nothing for a build that configures no endpoint", () => {
    setRuntimeConfig({ OTEL_ENDPOINT: "" });

    trackEvent("onboarding_completed");

    expect(track).not.toHaveBeenCalled();
  });

  it("sends nothing when the endpoint is `off`", () => {
    setRuntimeConfig({ OTEL_ENDPOINT: "off" });

    trackEvent("onboarding_completed");

    expect(track).not.toHaveBeenCalled();
  });

  it("never throws when the runtime does", () => {
    setRuntimeConfig({ OTEL_ENDPOINT: ENDPOINT });
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
    setRuntimeConfig({ OTEL_ENDPOINT: "https://ingest.example.dev/k/rbk_x" });
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("snake_cases camelCase keys and keeps native value types", () => {
    trackEvent("tour_step_viewed", { stepId: "welcome", stepsCompleted: 2, hasAttachments: false });

    expect(track).toHaveBeenCalledWith("product.tour_step_viewed", {
      surface: "reliant",
      step_id: "welcome",
      steps_completed: 2,
      has_attachments: false,
    });
  });

  it("tags every event with the shell it happened in, as a default a caller can override", () => {
    resetShellForTests("forge");
    try {
      trackEvent("claim_completed", { claimPath: "upgrade" });
      expect(track).toHaveBeenLastCalledWith("product.claim_completed", { surface: "forge", claim_path: "upgrade" });
      trackEvent("tip_shown", { surface: "mobile" });
      expect(track).toHaveBeenLastCalledWith("product.tip_shown", { surface: "mobile" });
    } finally {
      resetShellForTests();
    }
  });

  it("tags the public demo's events surface=demo", () => {
    resetShellForTests("demo");
    try {
      trackEvent("demo_tour_autostarted", { deepLink: false });
      expect(track).toHaveBeenLastCalledWith("product.demo_tour_autostarted", { surface: "demo", deep_link: false });
    } finally {
      resetShellForTests();
    }
  });

  it("leaves already-conforming keys alone", () => {
    trackEvent("onboarding_flow_step_completed", { step: "compute", duration_ms: 1234 });

    expect(track).toHaveBeenCalledWith("product.onboarding_flow_step_completed", {
      surface: "reliant",
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
    vi.doMock("../hooks/privacy-queries", () => ({
      privacyConsent: () => ({ analyticsEnabled: false }),
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
    vi.doMock("../hooks/privacy-queries", () => ({
      privacyConsent: () => ({ analyticsEnabled: true }),
    }));
    vi.doMock("./constants", () => ({ isDev: true }));
    vi.doMock("@reliantlabs/forge-web-runtime", () => ({ track: vi.fn() }));
    const runtime = await import("@reliantlabs/forge-web-runtime");

    const { trackEvent: gated } = await import("./analytics");
    gated("onboarding_completed");

    expect(runtime.track).not.toHaveBeenCalled();
  });
});
