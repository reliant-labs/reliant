import { beforeEach, describe, expect, it, vi } from "vitest";
import { setRuntimeConfig } from "@/test/runtimeConfig";

// A failure the app catches reaches the error pipeline only through these two
// calls, so what they hand the runtime is pinned here.

const runtime = vi.hoisted(() => ({ reportException: vi.fn(() => true) }));
vi.mock("@reliantlabs/forge-web-runtime", () => ({
  reportException: runtime.reportException,
}));

import { reportError, reportFailure } from "../errorReporting";

const ENDPOINT = "https://ingest.example.dev/k/rbk_x";

beforeEach(() => {
  vi.clearAllMocks();
  setRuntimeConfig({ OTEL_ENDPOINT: ENDPOINT });
});

describe("reportError", () => {
  it("hands the error and its context to the runtime, dropping unset values", () => {
    const err = new Error("boom");
    reportError(err, { component: "chat", operation: "load_messages", chat_id: undefined, attempt: 2 });
    expect(runtime.reportException).toHaveBeenCalledWith(err, {
      component: "chat",
      operation: "load_messages",
      attempt: 2,
    });
  });

  it("reports with no context", () => {
    const err = new Error("boom");
    reportError(err);
    expect(runtime.reportException).toHaveBeenCalledWith(err, undefined);
  });

  it("does nothing when the deployment ships no telemetry", () => {
    setRuntimeConfig({ OTEL_ENDPOINT: "" });
    reportError(new Error("boom"));
    expect(runtime.reportException).not.toHaveBeenCalled();
  });

  it("never throws when the runtime does", () => {
    runtime.reportException.mockImplementationOnce(() => {
      throw new Error("runtime exploded");
    });
    expect(() => reportError(new Error("boom"))).not.toThrow();
  });
});

describe("reportFailure", () => {
  it("reports an Error carrying the message, so it groups by it", () => {
    reportFailure("Codex OAuth state mismatch", { component: "oauth", provider: "codex" });
    const [error, attributes] = runtime.reportException.mock.calls[0] as unknown as [Error, unknown];
    expect(error).toBeInstanceOf(Error);
    expect(error.message).toBe("Codex OAuth state mismatch");
    expect(attributes).toEqual({ component: "oauth", provider: "codex" });
  });
});
