import { beforeEach, describe, expect, it } from "vitest";
import type { StepConfig, LaunchPlan } from "../types";

// We need a fresh registry for each test. The module exports a singleton,
// so we re-import it fresh via vi.resetModules().
let stepRegistry: typeof import("../StepRegistry")["stepRegistry"];

function makeStep(overrides: Partial<StepConfig> & { id: string }): StepConfig {
  return {
    category: "goal",
    component: (() => null) as unknown as StepConfig["component"],
    shouldShow: () => true,
    order: 0,
    ...overrides,
  };
}

beforeEach(async () => {
  // Get a fresh module (and therefore a fresh Map inside the class)
  vi.resetModules();
  const mod = await import("../StepRegistry");
  stepRegistry = mod.stepRegistry;
});

describe("StepRegistry", () => {
  it("register() adds a step", () => {
    stepRegistry.register(makeStep({ id: "a" }));
    const visible = stepRegistry.getVisibleSteps({});
    expect(visible).toHaveLength(1);
    expect(visible[0].id).toBe("a");
  });

  it("registerMany() adds multiple steps", () => {
    stepRegistry.registerMany([
      makeStep({ id: "a" }),
      makeStep({ id: "b" }),
      makeStep({ id: "c" }),
    ]);
    expect(stepRegistry.getVisibleSteps({})).toHaveLength(3);
  });

  it("register() with same id overrides existing step", () => {
    stepRegistry.register(makeStep({ id: "a", order: 0 }));
    stepRegistry.register(makeStep({ id: "a", order: 5 }));

    const visible = stepRegistry.getVisibleSteps({});
    expect(visible).toHaveLength(1);
    expect(visible[0].order).toBe(5);
  });

  it("getVisibleSteps() filters by shouldShow", () => {
    stepRegistry.registerMany([
      makeStep({ id: "shown", shouldShow: () => true }),
      makeStep({ id: "hidden", shouldShow: () => false }),
    ]);
    const visible = stepRegistry.getVisibleSteps({});
    expect(visible).toHaveLength(1);
    expect(visible[0].id).toBe("shown");
  });

  it("getVisibleSteps() passes the plan to shouldShow", () => {
    stepRegistry.register(
      makeStep({
        id: "conditional",
        shouldShow: (plan: Partial<LaunchPlan>) => plan.intent === "build_app",
      }),
    );

    expect(stepRegistry.getVisibleSteps({})).toHaveLength(0);
    expect(
      stepRegistry.getVisibleSteps({ intent: "build_app" }),
    ).toHaveLength(1);
  });

  it("getVisibleSteps() sorts by category order (goal→workspace→compute→start) then by order", () => {
    stepRegistry.registerMany([
      makeStep({ id: "start-1", category: "start", order: 0 }),
      makeStep({ id: "goal-1", category: "goal", order: 1 }),
      makeStep({ id: "workspace-2", category: "workspace", order: 2 }),
      makeStep({ id: "workspace-1", category: "workspace", order: 0 }),
      makeStep({ id: "goal-0", category: "goal", order: 0 }),
      makeStep({ id: "compute-0", category: "compute", order: 0 }),
    ]);

    const ids = stepRegistry.getVisibleSteps({}).map((s) => s.id);
    expect(ids).toEqual([
      "goal-0",
      "goal-1",
      "workspace-1",
      "workspace-2",
      "compute-0",
      "start-1",
    ]);
  });

  it("empty registry returns empty array", () => {
    expect(stepRegistry.getVisibleSteps({})).toEqual([]);
  });
});
