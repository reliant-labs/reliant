/**
 * The tour's step list and the rules that walk it.
 *
 * Pins the shape the rest of the tour assumes: no closing modal (the last
 * spotlight IS the end), and a step whose target can't exist — Deployments
 * with the forge UI switched off — drops out of Next, Back and the count
 * instead of becoming a dead stop on an empty spotlight.
 */
import { describe, it, expect, beforeEach, afterEach } from "vitest";
import {
  ONBOARDING_STEPS,
  ONBOARDING_STEP_IDS,
  getActiveTourSteps,
  getNextStepId,
  getPreviousStepId,
  getStepIndex,
} from "../constants";
import {
  FORGE_UI_FLAG_KEY,
  clearForgeUIPreference,
  setForgeUIEnabled,
} from "../../../lib/forgeFeature";

describe("tour steps", () => {
  beforeEach(() => clearForgeUIPreference());
  afterEach(() => clearForgeUIPreference());

  it("ends on a spotlight, with no closing modal step", () => {
    const last = ONBOARDING_STEPS[ONBOARDING_STEPS.length - 1];
    expect(last.id).toBe("workflow-builder");
    expect(ONBOARDING_STEPS.every((step) => step.type !== ("modal" as string))).toBe(true);
    expect(ONBOARDING_STEP_IDS).not.toContain("completion");
  });

  it("has no successor after the last step, so the nav reads Finish there", () => {
    expect(getNextStepId("workflow-builder")).toBeNull();
    expect(getNextStepId("workflow-hub")).toBe("workflow-builder");
  });

  it("teaches workflow params right after the overview", () => {
    expect(getNextStepId("chat-and-sidebars")).toBe("workflow-controls");
    expect(getNextStepId("workflow-controls")).toBe("workspaces");
  });

  describe("with the forge UI on (the default)", () => {
    it("includes Deployments between Workspaces and Workflows", () => {
      expect(window.localStorage.getItem(FORGE_UI_FLAG_KEY)).toBeNull();
      expect(getActiveTourSteps().map((s) => s.id)).toEqual([...ONBOARDING_STEP_IDS]);
      expect(getNextStepId("workspaces")).toBe("deployments");
      expect(getPreviousStepId("workflow-intro")).toBe("deployments");
      expect(getStepIndex("deployments")).toBe(3);
    });
  });

  describe("with the forge UI switched off", () => {
    beforeEach(() => setForgeUIEnabled(false));

    it("leaves Deployments out of the active tour and its count", () => {
      const ids = getActiveTourSteps().map((s) => s.id);
      expect(ids).not.toContain("deployments");
      expect(ids).toHaveLength(ONBOARDING_STEPS.length - 1);
      expect(getStepIndex("deployments")).toBe(-1);
    });

    it("steps over Deployments in both directions", () => {
      expect(getNextStepId("workspaces")).toBe("workflow-intro");
      expect(getPreviousStepId("workflow-intro")).toBe("workspaces");
    });

    it("still moves on from Deployments if it was open when forge went off", () => {
      expect(getNextStepId("deployments")).toBe("workflow-intro");
      expect(getPreviousStepId("deployments")).toBe("workspaces");
    });
  });
});
