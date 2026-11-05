/**
 * The journey registry: validation of content, walking steps (shell,
 * availability, outro), and resolving `?tour=` — including the retired single
 * tour's ids.
 */
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  JOURNEYS,
  activeJourneySteps,
  availableJourneys,
  nextStepId,
  presentStep,
  previousStepId,
  resolveTour,
  stepPosition,
  validateJourneys,
} from "../journeys/registry";
import { parseTourParam } from "../tourParam";
import { setTourShell } from "../tourShell";
import { clearForgeUIPreference, setForgeUIEnabled } from "../../../lib/forgeFeature";
import type { Journey } from "../types";

const resolve = (raw: string) => resolveTour(parseTourParam(raw));

function journeys(over: Partial<Record<"workflows" | "forge", Partial<Journey>>>) {
  return {
    workflows: { ...JOURNEYS.workflows, ...over.workflows },
    forge: { ...JOURNEYS.forge, ...over.forge },
  } as Record<"workflows" | "forge", Journey>;
}

describe("journey registry", () => {
  beforeEach(() => {
    clearForgeUIPreference();
    setTourShell("reliant");
  });
  afterEach(() => {
    clearForgeUIPreference();
    setTourShell(null);
  });

  describe("validateJourneys", () => {
    it("accepts the shipped journeys", () => {
      expect(() => validateJourneys(JOURNEYS)).not.toThrow();
    });

    it("rejects a step id used by both journeys (bare ?tour= must be unambiguous)", () => {
      const dup = JOURNEYS.workflows.steps[0];
      expect(() => validateJourneys(journeys({ forge: { steps: [dup] } }))).toThrow(/already used/);
    });

    it("rejects a non-kebab id, an empty journey and a target-less click", () => {
      expect(() =>
        validateJourneys(journeys({ forge: { steps: [{ id: "Bad_Id", title: "t", description: "d" }] } })),
      ).toThrow(/kebab/);
      expect(() => validateJourneys(journeys({ forge: { steps: [] } }))).toThrow(/no steps/);
      expect(() =>
        validateJourneys(
          journeys({ forge: { steps: [{ id: "fg-x", title: "t", description: "d", advanceOn: { kind: "click" } }] } }),
        ),
      ).toThrow(/click/);
    });

    it("requires a `when` on every phase after the first", () => {
      expect(() =>
        validateJourneys(
          journeys({
            forge: { steps: [{ id: "fg-x", title: "t", description: "d", phases: [{ target: "a" }, { target: "b" }] }] },
          }),
        ),
      ).toThrow(/phase/);
    });
  });

  describe("resolveTour", () => {
    it("opens journey:step, and a journey alone at its first step", () => {
      expect(resolve("workflows:wf-library")).toMatchObject({ kind: "step", journey: "workflows", stepId: "wf-library" });
      expect(resolve("workflows")).toMatchObject({ stepId: "wf-open" });
    });

    it("finds the journey of a bare step id", () => {
      expect(resolve("fg-instrumented")).toMatchObject({ journey: "forge", stepId: "fg-instrumented" });
    });

    it("opens an unknown step of a known journey at its first step", () => {
      expect(resolve("workflows:not-a-step")).toMatchObject({ journey: "workflows", stepId: "wf-open" });
    });

    it("redirects the retired single tour's ids to their successor journey", () => {
      expect(resolve("chat-and-sidebars")).toMatchObject({ journey: "workflows", stepId: "wf-open" });
      expect(resolve("workflow-builder")).toMatchObject({ journey: "workflows", stepId: "wf-open" });
      expect(resolve("deployments")).toMatchObject({ journey: "forge", stepId: "fg-console" });
    });

    it("is null for an unknown id or a journey that doesn't run here", () => {
      expect(resolve("zz-nothing")).toBeNull();
      setTourShell("forge");
      expect(resolve("workflows:wf-open")).toBeNull();
      setTourShell("reliant");
      setForgeUIEnabled(false);
      expect(resolve("forge")).toBeNull();
    });
  });

  describe("walking steps", () => {
    it("walks steps then the outro, in Reliant", () => {
      expect(activeJourneySteps("workflows").map((s) => s.id)).toEqual(["wf-open", "wf-library", "wf-outro-layout"]);
      expect(nextStepId("workflows", "wf-library")).toBe("wf-outro-layout");
      expect(nextStepId("workflows", "wf-outro-layout")).toBeNull();
      expect(previousStepId("workflows", "wf-outro-layout")).toBe("wf-library");
      expect(stepPosition("workflows", "wf-outro-layout")).toEqual({ index: 2, total: 3 });
    });

    it("drops a reliant-only outro step in the forge shell, so the journey ends on its last step", () => {
      setTourShell("forge");
      expect(activeJourneySteps("workflows").map((s) => s.id)).toEqual(["wf-open", "wf-library"]);
      expect(nextStepId("workflows", "wf-library")).toBeNull();
    });

    it("lists only the journeys that run in this shell", () => {
      expect(availableJourneys().map((j) => j.id)).toEqual(["forge", "workflows"]);
      setTourShell("forge");
      expect(availableJourneys().map((j) => j.id)).toEqual(["forge"]);
    });
  });

  describe("presentStep", () => {
    it("applies per-shell copy, then the active phase", () => {
      const step = {
        id: "fg-p",
        title: "T",
        description: "base",
        copyByShell: { forge: { description: "forge copy" } },
        phases: [{}, { when: { kind: "route" as const, path: "/x" }, target: "#b", description: "phase b" }],
      };
      setTourShell("forge");
      expect(presentStep(step).description).toBe("forge copy");
      expect(presentStep(step, 1)).toMatchObject({ description: "phase b", target: "#b", title: "T" });
    });
  });
});
