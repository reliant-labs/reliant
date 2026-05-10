import { describe, expect, it } from "vitest";
import { hasReliantCreditEligibility, type ControlPlaneUser } from "../api";

describe("hasReliantCreditEligibility", () => {
  // ── Undefined / empty user ────────────────────────────────

  it("returns false for undefined user", () => {
    expect(hasReliantCreditEligibility(undefined)).toBe(false);
  });

  it("returns false with empty user (no eligibility fields)", () => {
    const user: ControlPlaneUser = {};
    expect(hasReliantCreditEligibility(user)).toBe(false);
  });

  // ── Denial flags override everything ──────────────────────

  it("returns false when globalBudgetAvailable is false", () => {
    const user: ControlPlaneUser = {
      freeCreditsEligible: true,
      globalBudgetAvailable: false,
    };
    expect(hasReliantCreditEligibility(user)).toBe(false);
  });

  it("returns false when budgetAvailable is false", () => {
    const user: ControlPlaneUser = {
      freeCreditsEligible: true,
      budgetAvailable: false,
    };
    expect(hasReliantCreditEligibility(user)).toBe(false);
  });

  it("returns false when ipRestricted is true", () => {
    const user: ControlPlaneUser = {
      freeCreditsEligible: true,
      ipRestricted: true,
    };
    expect(hasReliantCreditEligibility(user)).toBe(false);
  });

  it("returns false when ipAllowed is false", () => {
    const user: ControlPlaneUser = {
      freeCreditsEligible: true,
      ipAllowed: false,
    };
    expect(hasReliantCreditEligibility(user)).toBe(false);
  });

  it("budget denial overrides credit eligibility", () => {
    const user: ControlPlaneUser = {
      freeCreditsEligible: true,
      creditsAvailable: true,
      creditBalance: 100,
      globalBudgetAvailable: false,
    };
    expect(hasReliantCreditEligibility(user)).toBe(false);
  });

  it("personal budget denial overrides credit eligibility", () => {
    const user: ControlPlaneUser = {
      freeCreditsEligible: true,
      creditsAvailable: true,
      creditBalance: 100,
      budgetAvailable: false,
    };
    expect(hasReliantCreditEligibility(user)).toBe(false);
  });

  it("IP restriction overrides credit eligibility", () => {
    const user: ControlPlaneUser = {
      freeCreditsEligible: true,
      creditsAvailable: true,
      creditBalance: 100,
      ipRestricted: true,
    };
    expect(hasReliantCreditEligibility(user)).toBe(false);
  });

  // ── Boolean eligibility flags ─────────────────────────────

  it("returns true when freeCreditsEligible is true", () => {
    const user: ControlPlaneUser = { freeCreditsEligible: true };
    expect(hasReliantCreditEligibility(user)).toBe(true);
  });

  it("returns true when reliantCreditsEligible is true", () => {
    const user: ControlPlaneUser = { reliantCreditsEligible: true };
    expect(hasReliantCreditEligibility(user)).toBe(true);
  });

  it("returns true when creditsEligible is true", () => {
    const user: ControlPlaneUser = { creditsEligible: true };
    expect(hasReliantCreditEligibility(user)).toBe(true);
  });

  it("returns true when welcomeCreditEligible is true", () => {
    const user: ControlPlaneUser = { welcomeCreditEligible: true };
    expect(hasReliantCreditEligibility(user)).toBe(true);
  });

  it("returns true when hasFreeCredits is true", () => {
    const user: ControlPlaneUser = { hasFreeCredits: true };
    expect(hasReliantCreditEligibility(user)).toBe(true);
  });

  it("returns true when freeCreditsAvailable is true", () => {
    const user: ControlPlaneUser = { freeCreditsAvailable: true };
    expect(hasReliantCreditEligibility(user)).toBe(true);
  });

  it("returns true when creditsAvailable is true", () => {
    const user: ControlPlaneUser = { creditsAvailable: true };
    expect(hasReliantCreditEligibility(user)).toBe(true);
  });

  // ── Numeric / string balance fields ───────────────────────

  it("returns true with positive trialCreditsRemaining (number)", () => {
    const user: ControlPlaneUser = { trialCreditsRemaining: 100 };
    expect(hasReliantCreditEligibility(user)).toBe(true);
  });

  it("returns true with positive trialCreditsRemaining (string)", () => {
    const user: ControlPlaneUser = { trialCreditsRemaining: "50" };
    expect(hasReliantCreditEligibility(user)).toBe(true);
  });

  it("returns false with zero trialCreditsRemaining", () => {
    const user: ControlPlaneUser = { trialCreditsRemaining: 0 };
    expect(hasReliantCreditEligibility(user)).toBe(false);
  });

  it("returns true with positive freeCreditsRemaining", () => {
    const user: ControlPlaneUser = { freeCreditsRemaining: 25 };
    expect(hasReliantCreditEligibility(user)).toBe(true);
  });

  it("returns true with positive creditsRemaining", () => {
    const user: ControlPlaneUser = { creditsRemaining: 10 };
    expect(hasReliantCreditEligibility(user)).toBe(true);
  });

  it("returns true with positive creditBalance", () => {
    const user: ControlPlaneUser = { creditBalance: 10.5 };
    expect(hasReliantCreditEligibility(user)).toBe(true);
  });

  it("returns false with zero creditBalance", () => {
    const user: ControlPlaneUser = { creditBalance: 0 };
    expect(hasReliantCreditEligibility(user)).toBe(false);
  });

  it("returns false with negative creditBalance", () => {
    const user: ControlPlaneUser = { creditBalance: -5 };
    expect(hasReliantCreditEligibility(user)).toBe(false);
  });

  it("returns true with positive creditBalance as string", () => {
    const user: ControlPlaneUser = { creditBalance: "42" };
    expect(hasReliantCreditEligibility(user)).toBe(true);
  });

  it("returns false with zero creditBalance as string", () => {
    const user: ControlPlaneUser = { creditBalance: "0" };
    expect(hasReliantCreditEligibility(user)).toBe(false);
  });
});
