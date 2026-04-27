import { stepRegistry } from "../StepRegistry";
import type { StepConfig } from "../types";
import { GoalStep } from "./GoalStep";
import { ProjectSourceStep } from "./ProjectSourceStep";
import { ForgeStep } from "./ForgeStep";
import { ReadyStep } from "./ReadyStep";

const steps: StepConfig[] = [
  {
    id: "goal",
    category: "goal",
    component: GoalStep,
    shouldShow: () => true,
    order: 0,
  },
  {
    id: "project-source",
    category: "workspace",
    component: ProjectSourceStep,
    shouldShow: (plan) =>
      plan.intent === "build_app" || plan.intent === "existing_codebase",
    order: 0,
  },
  {
    id: "forge",
    category: "workspace",
    component: ForgeStep,
    shouldShow: (plan) =>
      plan.intent === "build_app" && plan.codeSource === "new_project",
    order: 1,
  },
  {
    id: "ready",
    category: "start",
    component: ReadyStep,
    shouldShow: () => true,
    order: 0,
  },
];

stepRegistry.registerMany(steps);
