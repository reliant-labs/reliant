import { beforeEach, describe, expect, it } from "vitest";
import {
  HOLD_MS,
  LANDING_ATTRIBUTION_KEY,
  dropLandingAttribution,
  handoffQuery,
  holdLandingAttribution,
  readLandingAttribution,
} from "../landingAttribution";

const LANDING = "?rid=v_landing_123456&utm_source=reddit&utm_campaign=b2&next=%2Fchats&code=secret";

beforeEach(() => localStorage.clear());

describe("landing attribution handoff", () => {
  it("keeps only what the runtime captures: a well-formed rid and the utm_* values", () => {
    expect(handoffQuery(LANDING)).toBe("?rid=v_landing_123456&utm_source=reddit&utm_campaign=b2");
    expect(handoffQuery("?rid=bad id!&utm_medium=%20post%20")).toBe("?utm_medium=post");
    expect(handoffQuery("?code=abc&state=xyz")).toBe("");
    expect(handoffQuery(`?utm_content=${"x".repeat(300)}`)).toBe(`?utm_content=${"x".repeat(128)}`);
  });

  it("survives the sign-in replacing the URL, and stays held until dropped", () => {
    holdLandingAttribution(LANDING, 1_000);
    // The OAuth callback's own page load carries no handoff and must not erase it.
    holdLandingAttribution("?code=abc&state=xyz", 2_000);
    expect(localStorage.getItem(LANDING_ATTRIBUTION_KEY)).not.toContain("secret");
    expect(readLandingAttribution(3_000)).toBe("?rid=v_landing_123456&utm_source=reddit&utm_campaign=b2");
    // Reading does not consume it: a telemetry start that fails retries with it.
    expect(readLandingAttribution(3_000)).toBe("?rid=v_landing_123456&utm_source=reddit&utm_campaign=b2");
    dropLandingAttribution();
    expect(readLandingAttribution(3_000)).toBeUndefined();
  });

  it("a later landing visit replaces the held one (last touch, as the runtime does)", () => {
    holdLandingAttribution(LANDING, 1_000);
    holdLandingAttribution("?rid=v_second_visit_99&utm_source=hn", 2_000);
    expect(readLandingAttribution(3_000)).toBe("?rid=v_second_visit_99&utm_source=hn");
  });

  it("expires after HOLD_MS and ignores a corrupt or future record", () => {
    holdLandingAttribution(LANDING, 0);
    expect(readLandingAttribution(HOLD_MS + 1)).toBeUndefined();
    expect(localStorage.getItem(LANDING_ATTRIBUTION_KEY)).toBeNull();

    localStorage.setItem(LANDING_ATTRIBUTION_KEY, "{not json");
    expect(readLandingAttribution(0)).toBeUndefined();
    holdLandingAttribution(LANDING, 10_000);
    expect(readLandingAttribution(5_000)).toBeUndefined();
    expect(localStorage.getItem(LANDING_ATTRIBUTION_KEY)).toBeNull();
  });

  it("drop forgets it", () => {
    holdLandingAttribution(LANDING, 1_000);
    dropLandingAttribution();
    expect(readLandingAttribution(2_000)).toBeUndefined();
  });
});
