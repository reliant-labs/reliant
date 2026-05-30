import { describe, expect, it } from "vitest";
import {
  isCloudEligible,
  type ReliantEntitlement,
} from "@/services/controlPlane/billing";

// The proto-generated `ReliantEntitlement` has every field as a non-optional
// primitive; tests construct partial entitlements by casting through
// `unknown` so we only have to spell out the fields under test.
function ent(partial: Partial<ReliantEntitlement>): ReliantEntitlement {
  return partial as unknown as ReliantEntitlement;
}

describe("isCloudEligible", () => {
  it("returns false for undefined entitlement", () => {
    expect(isCloudEligible(undefined)).toBe(false);
  });

  it("returns false for empty entitlement", () => {
    expect(isCloudEligible(ent({}))).toBe(false);
  });

  it("returns true when status is active and reliantEnabled is true", () => {
    expect(
      isCloudEligible(ent({ status: "active", reliantEnabled: true })),
    ).toBe(true);
  });

  it("returns false when status is active but reliantEnabled is false", () => {
    expect(
      isCloudEligible(ent({ status: "active", reliantEnabled: false })),
    ).toBe(false);
  });

  it("returns false when reliantEnabled is true but status is not active", () => {
    expect(
      isCloudEligible(ent({ status: "canceled", reliantEnabled: true })),
    ).toBe(false);
  });

  it("returns false when status is trialing", () => {
    expect(
      isCloudEligible(ent({ status: "trialing", reliantEnabled: true })),
    ).toBe(false);
  });

  it("returns false when reliantEnabled is missing", () => {
    expect(isCloudEligible(ent({ status: "active" }))).toBe(false);
  });
});
