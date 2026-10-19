import { describe, expect, it } from "vitest";
import { endpointConfigured, parseTelemetrySinks } from "../telemetrySinks";

const ENDPOINT = "https://ingest.example.dev/k/rbk_x";

describe("parseTelemetrySinks", () => {
  it("is today's behaviour when nothing is configured: Sentry on, ClickStack off", () => {
    expect(parseTelemetrySinks(undefined, undefined)).toEqual({
      sentry: true,
      clickstack: false,
    });
  });

  it("turns ClickStack on for an unset flag only once an endpoint exists", () => {
    expect(parseTelemetrySinks(undefined, ENDPOINT)).toEqual({
      sentry: true,
      clickstack: true,
    });
  });

  it("runs both in parallel for `sentry,clickstack`", () => {
    expect(parseTelemetrySinks("sentry,clickstack", ENDPOINT)).toEqual({
      sentry: true,
      clickstack: true,
    });
  });

  it("drops Sentry for `clickstack`", () => {
    expect(parseTelemetrySinks("clickstack", ENDPOINT)).toEqual({
      sentry: false,
      clickstack: true,
    });
  });

  it("keeps ClickStack off without an endpoint even when the flag names it", () => {
    expect(parseTelemetrySinks("sentry,clickstack", undefined)).toEqual({
      sentry: true,
      clickstack: false,
    });
    expect(parseTelemetrySinks("clickstack", "off").clickstack).toBe(false);
  });

  it("tolerates case and whitespace", () => {
    expect(parseTelemetrySinks(" Sentry , ClickStack ", ENDPOINT)).toEqual({
      sentry: true,
      clickstack: true,
    });
  });

  it("does not let a typo switch off the sink that works today", () => {
    expect(parseTelemetrySinks("clickhouse", ENDPOINT).sentry).toBe(true);
    expect(parseTelemetrySinks("", undefined).sentry).toBe(true);
  });
});

describe("endpointConfigured", () => {
  it.each([undefined, "", "  ", "off", "OFF"])("treats %j as unset", (v) => {
    expect(endpointConfigured(v)).toBe(false);
  });

  it("accepts a URL", () => {
    expect(endpointConfigured(ENDPOINT)).toBe(true);
  });
});
