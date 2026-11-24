import { describe, expect, it } from "vitest";

import { isFixtureMode, runtimeFixtures } from "@/lib/fixturesConfig";
import { setRuntimeConfig } from "@/test/runtimeConfig";

describe("runtimeFixtures (forge#2719's keys)", () => {
  it("is off without FIXTURES_SCENARIO, or with it empty", () => {
    expect(runtimeFixtures({})).toBeNull();
    expect(runtimeFixtures(undefined)).toBeNull();
    expect(runtimeFixtures({ FIXTURES_SCENARIO: "  " })).toBeNull();
    expect(runtimeFixtures({ FIXTURES_READ_ONLY: "false" })).toBeNull();
  });

  it("is read-only unless FIXTURES_READ_ONLY is exactly \"false\"", () => {
    expect(runtimeFixtures({ FIXTURES_SCENARIO: "shop" })).toEqual({ scenario: "shop", readOnly: true, overlays: [] });
    expect(runtimeFixtures({ FIXTURES_SCENARIO: "shop", FIXTURES_READ_ONLY: "true" })?.readOnly).toBe(true);
    expect(runtimeFixtures({ FIXTURES_SCENARIO: "shop", FIXTURES_READ_ONLY: "False" })?.readOnly).toBe(true);
    expect(runtimeFixtures({ FIXTURES_SCENARIO: "shop", FIXTURES_READ_ONLY: "" })?.readOnly).toBe(true);
    expect(runtimeFixtures({ FIXTURES_SCENARIO: "shop", FIXTURES_READ_ONLY: "false" })?.readOnly).toBe(false);
  });

  it("reads ?fixtures= overlays in fixture mode, and only then", () => {
    expect(runtimeFixtures({ FIXTURES_SCENARIO: "shop" }, "?fixtures=no-daemons,%20tests-unimplemented&x=1")?.overlays).toEqual(["no-daemons", "tests-unimplemented"]);
    expect(runtimeFixtures({ FIXTURES_SCENARIO: "shop" }, "?fixtures=<script>,ok")?.overlays).toEqual(["ok"]);
    expect(runtimeFixtures({}, "?fixtures=no-daemons")).toBeNull();
  });

  it("reads the page's own config.js by default", () => {
    setRuntimeConfig({});
    expect(isFixtureMode()).toBe(false);
    setRuntimeConfig({ FIXTURES_SCENARIO: "shop" });
    expect(isFixtureMode()).toBe(true);
  });
});
