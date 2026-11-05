/**
 * The landing page's `?journey=` choice survives the sign-in redirect, held
 * beside rid/utm but with no consent gate (it is not an identifier).
 */
import { afterEach, describe, expect, it } from "vitest";
import {
  HOLD_MS,
  LANDING_JOURNEY_KEY,
  dropLandingAttribution,
  dropLandingJourney,
  handoffQuery,
  holdLandingJourney,
  journeyFromSearch,
  readLandingJourney,
} from "../landingAttribution";

afterEach(() => localStorage.clear());

describe("landing journey handoff", () => {
  it("reads only known journeys", () => {
    expect(journeyFromSearch("?journey=forge&rid=abcdefgh12")).toBe("forge");
    expect(journeyFromSearch("?journey=workflows")).toBe("workflows");
    expect(journeyFromSearch("?journey=build")).toBeUndefined();
    expect(journeyFromSearch("?journey=")).toBeUndefined();
  });

  it("holds it through a sign-in page load that carries none", () => {
    holdLandingJourney("?journey=forge", 1_000);
    holdLandingJourney("?code=oauth-return", 2_000);
    expect(readLandingJourney(3_000)).toBe("forge");
  });

  it("a later choice replaces an earlier one", () => {
    holdLandingJourney("?journey=forge", 1_000);
    holdLandingJourney("?journey=workflows", 2_000);
    expect(readLandingJourney(3_000)).toBe("workflows");
  });

  it("expires after the hold window, and drops a corrupt record", () => {
    holdLandingJourney("?journey=forge", 0);
    expect(readLandingJourney(HOLD_MS + 1)).toBeUndefined();
    expect(localStorage.getItem(LANDING_JOURNEY_KEY)).toBeNull();
    localStorage.setItem(LANDING_JOURNEY_KEY, "{not json");
    expect(readLandingJourney()).toBeUndefined();
    expect(localStorage.getItem(LANDING_JOURNEY_KEY)).toBeNull();
  });

  it("is independent of the consent-gated rid/utm handoff", () => {
    holdLandingJourney("?journey=workflows&rid=abcdefgh12", 1_000);
    // Analytics consent off drops the attribution handoff, never the journey.
    dropLandingAttribution();
    expect(readLandingJourney(2_000)).toBe("workflows");
    // And journey is not part of what the runtime is handed.
    expect(handoffQuery("?journey=workflows&rid=abcdefgh12")).toBe("?rid=abcdefgh12");
    dropLandingJourney();
    expect(readLandingJourney(2_000)).toBeUndefined();
  });
});
