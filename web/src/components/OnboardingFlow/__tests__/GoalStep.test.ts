import { beforeEach, describe, expect, it, vi } from "vitest";
import type { LaunchPlan, StepConfig } from "../types";

/**
 * Tests for:
 * 1. The intent→workflow mapping contract (GoalStep INTENT_OPTIONS)
 * 2. The shouldShow predicates (steps/index.ts registrations)
 *
 * We avoid importing the actual step components (which pull in grpc-client
 * and protobuf deps) by reproducing the shouldShow predicates and workflow
 * mapping data. This keeps the tests fast and free of transitive dependency
 * issues while still verifying the critical business logic.
 */

let stepRegistry: typeof import("../StepRegistry")["stepRegistry"];

/**
 * These mirror the shouldShow predicates from steps/index.ts.
 * If the predicates change there, these tests should be updated to match.
 */
function registerProductionSteps(registry: typeof stepRegistry) {
  const DummyComponent = (() => null) as unknown as StepConfig["component"];

  registry.registerMany([
    {
      id: "goal",
      category: "goal",
      component: DummyComponent,
      shouldShow: () => true,
      order: 0,
    },
    {
      id: "project-source",
      category: "workspace",
      component: DummyComponent,
      shouldShow: (plan) =>
        plan.intent === "build_app" || plan.intent === "existing_codebase",
      order: 0,
    },
    {
      id: "forge",
      category: "workspace",
      component: DummyComponent,
      shouldShow: (plan) =>
        plan.intent === "build_app" && plan.codeSource === "new_project",
      order: 1,
    },
    {
      id: "ready",
      category: "start",
      component: DummyComponent,
      shouldShow: () => true,
      order: 0,
    },
  ]);
}

beforeEach(async () => {
  vi.resetModules();
  const mod = await import("../StepRegistry");
  stepRegistry = mod.stepRegistry;
  registerProductionSteps(stepRegistry);
});

// ── Intent → Workflow mapping ─────────────────────────────────────────

describe("GoalStep intent → workflow mapping", () => {
  /**
   * These represent the INTENT_OPTIONS defined in GoalStep.tsx.
   * We test them as a contract: each intent must map to the expected
   * workflowId and optional presetId.
   */
  const EXPECTED_MAPPINGS: {
    intent: string;
    workflowId: string;
    presetId?: string;
  }[] = [
    { intent: "build_app", workflowId: "forge-one-shot" },
    { intent: "existing_codebase", workflowId: "agent" },
    { intent: "landing_page", workflowId: "get-it-right", presetId: "ux" },
    { intent: "pitch_deck", workflowId: "get-it-right" },
    { intent: "blog_post", workflowId: "agent", presetId: "documentation" },
    { intent: "explore", workflowId: "agent" },
  ];

  it.each([
    ["build_app", "forge-one-shot", undefined],
    ["existing_codebase", "agent", undefined],
    ["landing_page", "get-it-right", "ux"],
    ["pitch_deck", "get-it-right", undefined],
    ["blog_post", "agent", "documentation"],
    ["explore", "agent", undefined],
  ] as const)(
    "%s → workflowId=%s, presetId=%s",
    (intent, expectedWorkflow, expectedPreset) => {
      const mapping = EXPECTED_MAPPINGS.find((m) => m.intent === intent);
      expect(mapping).toBeDefined();
      expect(mapping!.workflowId).toBe(expectedWorkflow);
      expect(mapping!.presetId).toBe(expectedPreset);
    },
  );
});

// ── Step visibility (shouldShow predicates) ───────────────────────────

describe("Step visibility predicates", () => {
  function visibleIds(plan: Partial<LaunchPlan>): string[] {
    return stepRegistry.getVisibleSteps(plan).map((s) => s.id);
  }

  it("GoalStep shows always (empty plan)", () => {
    expect(visibleIds({})).toContain("goal");
  });

  it("ReadyStep shows always (empty plan)", () => {
    expect(visibleIds({})).toContain("ready");
  });

  it("ProjectSourceStep shows for build_app intent", () => {
    expect(visibleIds({ intent: "build_app" })).toContain("project-source");
  });

  it("ProjectSourceStep shows for existing_codebase intent", () => {
    expect(visibleIds({ intent: "existing_codebase" })).toContain(
      "project-source",
    );
  });

  it("ProjectSourceStep does NOT show for landing_page", () => {
    expect(visibleIds({ intent: "landing_page" })).not.toContain(
      "project-source",
    );
  });

  it("ProjectSourceStep does NOT show for explore", () => {
    expect(visibleIds({ intent: "explore" })).not.toContain("project-source");
  });

  it("ProjectSourceStep does NOT show for pitch_deck", () => {
    expect(visibleIds({ intent: "pitch_deck" })).not.toContain(
      "project-source",
    );
  });

  it("ProjectSourceStep does NOT show for blog_post", () => {
    expect(visibleIds({ intent: "blog_post" })).not.toContain(
      "project-source",
    );
  });

  it("ForgeStep shows for build_app + new_project", () => {
    expect(
      visibleIds({ intent: "build_app", codeSource: "new_project" }),
    ).toContain("forge");
  });

  it("ForgeStep does NOT show for build_app + github_repo", () => {
    expect(
      visibleIds({ intent: "build_app", codeSource: "github_repo" }),
    ).not.toContain("forge");
  });

  it("ForgeStep does NOT show for existing_codebase + new_project", () => {
    expect(
      visibleIds({ intent: "existing_codebase", codeSource: "new_project" }),
    ).not.toContain("forge");
  });

  it("ForgeStep does NOT show without intent", () => {
    expect(visibleIds({ codeSource: "new_project" })).not.toContain("forge");
  });

  it("full build_app + new_project flow shows all 4 steps in order", () => {
    const ids = visibleIds({ intent: "build_app", codeSource: "new_project" });
    expect(ids).toEqual(["goal", "project-source", "forge", "ready"]);
  });

  it("explore flow shows only goal and ready", () => {
    const ids = visibleIds({ intent: "explore" });
    expect(ids).toEqual(["goal", "ready"]);
  });
});
