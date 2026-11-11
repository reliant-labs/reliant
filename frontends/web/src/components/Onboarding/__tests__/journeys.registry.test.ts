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
  WORKFLOWS_ELSEWHERE,
} from "../journeys/registry";
import { workflowsJourney } from "../journeys/workflows";
// The workflows journey's content is registered by the Reliant shells (journeys/catalog.ts).
import "../journeys/catalog";
import { parseTourParam } from "../tourParam";
import { setTourShell } from "../tourShell";
import { clearForgeUIPreference, setForgeUIEnabled } from "../../../lib/forgeFeature";
import type { Journey } from "../types";

const resolve = (raw: string) => resolveTour(parseTourParam(raw));

/** The workflows journey proper, before its outro. */
const WORKFLOWS_STEPS = ["wf-example", "wf-edge", "wf-loop", "wf-branch", "wf-trigger", "wf-machine", "wf-inputs"];

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
      expect(resolve("workflows:wf-loop")).toMatchObject({ kind: "step", journey: "workflows", stepId: "wf-loop" });
      expect(resolve("workflows")).toMatchObject({ stepId: "wf-example" });
    });

    it("finds the journey of a bare step id", () => {
      expect(resolve("fg-instrumented")).toMatchObject({ journey: "forge", stepId: "fg-instrumented" });
    });

    it("opens an unknown step of a known journey at its first step", () => {
      expect(resolve("workflows:not-a-step")).toMatchObject({ journey: "workflows", stepId: "wf-example" });
    });

    it("redirects the retired single tour's ids to their successor journey", () => {
      expect(resolve("chat-and-sidebars")).toMatchObject({ journey: "workflows", stepId: "wf-example" });
      expect(resolve("workflow-builder")).toMatchObject({ journey: "workflows", stepId: "wf-example" });
      expect(resolve("deployments")).toMatchObject({ journey: "forge", stepId: "fg-intro" });
    });

    it("is null for an unknown id or a journey that doesn't run here", () => {
      expect(resolve("zz-nothing")).toBeNull();
      setTourShell("forge");
      expect(resolve("workflows:wf-new")).toBeNull();
      setTourShell("reliant");
      setForgeUIEnabled(false);
      expect(resolve("forge")).toBeNull();
    });
  });

  describe("walking steps", () => {
    it("walks steps then the outro, in Reliant", () => {
      expect(activeJourneySteps("workflows").map((s) => s.id)).toEqual([...WORKFLOWS_STEPS, "wf-outro-layout"]);
      expect(nextStepId("workflows", "wf-inputs")).toBe("wf-outro-layout");
      expect(nextStepId("workflows", "wf-outro-layout")).toBeNull();
      expect(previousStepId("workflows", "wf-outro-layout")).toBe("wf-inputs");
      expect(stepPosition("workflows", "wf-outro-layout")).toEqual({ index: 7, total: 8 });
    });

    it("drops a reliant-only outro step in the forge shell, so the journey ends on its last step", () => {
      setTourShell("forge");
      expect(activeJourneySteps("workflows").map((s) => s.id)).toEqual(WORKFLOWS_STEPS);
      expect(nextStepId("workflows", "wf-inputs")).toBeNull();
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

describe("the workflows placeholder (the forge demo bundle, where the content isn't loaded)", () => {
  it("says what the real journey says: version, title, shells and where it runs", () => {
    expect(WORKFLOWS_ELSEWHERE).toMatchObject({
      id: workflowsJourney.id,
      version: workflowsJourney.version,
      title: workflowsJourney.title,
      description: workflowsJourney.description,
      shells: workflowsJourney.shells,
    });
    expect(WORKFLOWS_ELSEWHERE.elsewhere?.label).toBe(workflowsJourney.elsewhere?.label);
    expect(WORKFLOWS_ELSEWHERE.elsewhere?.href()).toBe(workflowsJourney.elsewhere?.href());
  });

  it("is replaced by the content once the catalog registers it", () => {
    expect(JOURNEYS.workflows).toBe(workflowsJourney);
  });
});
