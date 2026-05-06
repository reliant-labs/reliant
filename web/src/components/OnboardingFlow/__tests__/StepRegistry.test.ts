import { beforeEach, describe, expect, it, vi } from "vitest";
import type { StepConfig } from "../types";

let stepRegistry: typeof import("../StepRegistry")["stepRegistry"];

function makeStep(overrides: Partial<StepConfig> & { id: string }): StepConfig {
  return {
    id: overrides.id,
    label: overrides.label ?? overrides.id,
    category: overrides.category ?? "test",
    component: (() => null) as StepConfig["component"],
    order: overrides.order ?? 0,
  };
}

beforeEach(async () => {
  vi.resetModules();
  const mod = await import("../StepRegistry");
  stepRegistry = mod.stepRegistry;
  stepRegistry.clear();
});

describe("StepRegistry", () => {
  it("registers and retrieves a step by id", () => {
    stepRegistry.register(makeStep({ id: "goal" }));

    const step = stepRegistry.getStep("goal");

    expect(step).toBeDefined();
    expect(step!.id).toBe("goal");
  });

  it("returns undefined for unknown step id", () => {
    expect(stepRegistry.getStep("nonexistent")).toBeUndefined();
  });

  it("registers many steps", () => {
    stepRegistry.registerMany([
      makeStep({ id: "goal" }),
      makeStep({ id: "compute" }),
      makeStep({ id: "model" }),
    ]);

    expect(stepRegistry.getStep("goal")).toBeDefined();
    expect(stepRegistry.getStep("compute")).toBeDefined();
    expect(stepRegistry.getStep("model")).toBeDefined();
  });

  it("overrides duplicate ids", () => {
    stepRegistry.register(makeStep({ id: "goal", label: "Old", order: 0 }));
    stepRegistry.register(makeStep({ id: "goal", label: "New", order: 10 }));

    const step = stepRegistry.getStep("goal");
    expect(step!.label).toBe("New");
    expect(step!.order).toBe(10);
  });

  it("getStepsForPath returns steps in path order", () => {
    stepRegistry.registerMany([
      makeStep({ id: "model", order: 60 }),
      makeStep({ id: "goal", order: 0 }),
      makeStep({ id: "compute", order: 10 }),
    ]);

    const steps = stepRegistry.getStepsForPath(["goal", "compute", "model"]);

    expect(steps.map((s) => s.id)).toEqual(["goal", "compute", "model"]);
  });

  it("getStepsForPath skips unregistered step ids", () => {
    stepRegistry.registerMany([
      makeStep({ id: "goal" }),
      makeStep({ id: "model" }),
    ]);

    const steps = stepRegistry.getStepsForPath(["goal", "unknown-step", "model"]);

    expect(steps.map((s) => s.id)).toEqual(["goal", "model"]);
  });

  it("getStepsForPath returns empty array for empty path", () => {
    stepRegistry.register(makeStep({ id: "goal" }));

    expect(stepRegistry.getStepsForPath([])).toEqual([]);
  });

  it("clear removes all steps", () => {
    stepRegistry.registerMany([
      makeStep({ id: "goal" }),
      makeStep({ id: "compute" }),
    ]);

    stepRegistry.clear();

    expect(stepRegistry.getStep("goal")).toBeUndefined();
    expect(stepRegistry.getStep("compute")).toBeUndefined();
    expect(stepRegistry.getStepsForPath(["goal", "compute"])).toEqual([]);
  });
});
