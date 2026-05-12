import { describe, expect, it } from "vitest";
import { isCloudEligible, type ReliantEntitlement } from "../api";

describe("isCloudEligible", () => {
  it("returns false for undefined entitlement", () => {
    expect(isCloudEligible(undefined)).toBe(false);
  });

  it("returns false for empty entitlement", () => {
    expect(isCloudEligible({})).toBe(false);
  });

  it("returns true when status is active and reliantEnabled is true", () => {
    const ent: ReliantEntitlement = { status: "active", reliantEnabled: true };
    expect(isCloudEligible(ent)).toBe(true);
  });

  it("returns true when status is active and reliant_enabled is true (snake_case)", () => {
    const ent: ReliantEntitlement = { status: "active", reliant_enabled: true };
    expect(isCloudEligible(ent)).toBe(true);
  });

  it("returns false when status is active but reliantEnabled is false", () => {
    const ent: ReliantEntitlement = { status: "active", reliantEnabled: false };
    expect(isCloudEligible(ent)).toBe(false);
  });

  it("returns false when reliantEnabled is true but status is not active", () => {
    const ent: ReliantEntitlement = { status: "canceled", reliantEnabled: true };
    expect(isCloudEligible(ent)).toBe(false);
  });

  it("returns false when status is trialing", () => {
    const ent: ReliantEntitlement = { status: "trialing", reliantEnabled: true };
    expect(isCloudEligible(ent)).toBe(false);
  });

  it("returns false when reliantEnabled is missing", () => {
    const ent: ReliantEntitlement = { status: "active" };
    expect(isCloudEligible(ent)).toBe(false);
  });
});
