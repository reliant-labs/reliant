import type { StepConfig, LaunchPlan } from "./types";

const CATEGORY_ORDER: Record<string, number> = {
  goal: 0,
  workspace: 1,
  compute: 2,
  start: 3,
};

class OnboardingStepRegistry {
  private steps: Map<string, StepConfig> = new Map();

  register(step: StepConfig): void {
    this.steps.set(step.id, step);
  }

  registerMany(steps: StepConfig[]): void {
    for (const step of steps) {
      this.register(step);
    }
  }

  getVisibleSteps(plan: Partial<LaunchPlan>): StepConfig[] {
    return Array.from(this.steps.values())
      .filter((step) => step.shouldShow(plan))
      .sort((a, b) => {
        const catDiff =
          (CATEGORY_ORDER[a.category] ?? 99) -
          (CATEGORY_ORDER[b.category] ?? 99);
        if (catDiff !== 0) return catDiff;
        return a.order - b.order;
      });
  }

  getCategories(): string[] {
    return ["goal", "workspace", "compute", "start"];
  }
}

export const stepRegistry = new OnboardingStepRegistry();
