import { describe, expect, it } from "vitest";
import { endpointConfigured } from "../telemetryEndpoint";

const ENDPOINT = "https://ingest.example.dev/k/rbk_x";

describe("endpointConfigured", () => {
  it.each([undefined, "", "  ", "off", "OFF"])("treats %j as unset", (v) => {
    expect(endpointConfigured(v)).toBe(false);
  });

  it("accepts a URL", () => {
    expect(endpointConfigured(ENDPOINT)).toBe(true);
  });

  it("accepts the hosted SPA's same-origin ingest", () => {
    expect(endpointConfigured("/_otel")).toBe(true);
  });
});
