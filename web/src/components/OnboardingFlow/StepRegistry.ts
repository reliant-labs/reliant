import type { StepConfig } from "./types";

class OnboardingStepRegistry {
  private steps: Map<string, StepConfig> = new Map();

  clear(): void {
    this.steps.clear();
  }

  register(step: StepConfig): void {
    this.steps.set(step.id, step);
  }

  registerMany(steps: StepConfig[]): void {
    for (const step of steps) {
      this.register(step);
    }
  }

  getStep(id: string): StepConfig | undefined {
    return this.steps.get(id);
  }

  getStepsForPath(stepIds: string[]): StepConfig[] {
    const result: StepConfig[] = [];
    for (const id of stepIds) {
      const step = this.steps.get(id);
      if (step) result.push(step);
    }
    return result;
  }
}

export const stepRegistry = new OnboardingStepRegistry();