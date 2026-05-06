import { stepRegistry } from "../StepRegistry";
import type { LaunchPlan, StepConfig } from "../types";
import { ComputeStep } from "./ComputeStep";
import { DaemonConnectStep } from "./DaemonConnectStep";
import { ForgeStep } from "./ForgeStep";
import { GitHubConnectStep } from "./GitHubConnectStep";
import { GoalStep } from "./GoalStep";
import { ModelStep } from "./ModelStep";
import { ProjectLocationStep } from "./ProjectLocationStep";

const steps: StepConfig[] = [
  {
    id: "goal",
    label: "Goal",
    category: "intent",
    component: GoalStep,
    order: 0,
  },
  {
    id: "compute",
    label: "Daemon",
    category: "connect",
    component: ComputeStep,
    order: 10,
  },
  {
    id: "daemon-connect",
    label: "Connect",
    category: "connect",
    component: DaemonConnectStep,
    order: 20,
  },
  {
    id: "github-connect",
    label: "Repository",
    category: "workspace",
    component: GitHubConnectStep,
    order: 30,
  },
  {
    id: "local-project-location",
    label: "Directory",
    category: "workspace",
    component: ProjectLocationStep,
    order: 40,
  },
  {
    id: "cloud-project-location",
    label: "Project",
    category: "workspace",
    component: ProjectLocationStep,
    order: 45,
  },
  {
    id: "forge-style",
    label: "Style",
    category: "workspace",
    component: ForgeStep,
    order: 50,
  },
  {
    id: "model",
    label: "Model",
    category: "model",
    component: ModelStep,
    order: 60,
  },
];

stepRegistry.registerMany(steps);

// ── Fixed step paths ────────────────────────────────────────

export const STEP_PATHS: Record<string, string[]> = {
  // Cloud paths
  cloud_new_app: ["goal", "compute", "cloud-project-location", "forge-style", "model"],
  cloud_existing: ["goal", "compute", "github-connect", "model"],
  cloud_explore: ["goal", "compute", "cloud-project-location", "model"],
  cloud_landing_page: ["goal", "compute", "cloud-project-location", "model"],
  cloud_pitch_deck: ["goal", "compute", "cloud-project-location", "model"],
  cloud_blog_post: ["goal", "compute", "cloud-project-location", "model"],

  // Local paths
  local_new_app: ["goal", "compute", "daemon-connect", "local-project-location", "forge-style", "model"],
  local_existing: ["goal", "compute", "daemon-connect", "local-project-location", "model"],
  local_explore: ["goal", "compute", "daemon-connect", "local-project-location", "model"],
  local_landing_page: ["goal", "compute", "daemon-connect", "local-project-location", "model"],
  local_pitch_deck: ["goal", "compute", "daemon-connect", "local-project-location", "model"],
  local_blog_post: ["goal", "compute", "daemon-connect", "local-project-location", "model"],

  // Pre-connected daemon paths (daemon already running, skip connect)
  preconnected_new_app: ["goal", "compute", "local-project-location", "forge-style", "model"],
  preconnected_existing: ["goal", "compute", "local-project-location", "model"],
  preconnected_explore: ["goal", "compute", "local-project-location", "model"],
  preconnected_landing_page: ["goal", "compute", "local-project-location", "model"],
  preconnected_pitch_deck: ["goal", "compute", "local-project-location", "model"],
  preconnected_blog_post: ["goal", "compute", "local-project-location", "model"],
};

export const INITIAL_PATH = ["goal"];
export const AFTER_GOAL_PATH = ["goal", "compute"];

export function derivePath(plan: Partial<LaunchPlan>): string[] {
  if (!plan.intent) return INITIAL_PATH;
  if (!plan.compute) return AFTER_GOAL_PATH;

  const computePrefix = plan.daemonPreConnected
    ? "preconnected"
    : plan.compute === "cloud_free_trial"
      ? "cloud"
      : "local";

  const intentSuffix =
    plan.intent === "build_app"
      ? "new_app"
      : plan.intent === "existing_codebase"
        ? "existing"
        : plan.intent; // explore, landing_page, pitch_deck, blog_post

  const pathId = `${computePrefix}_${intentSuffix}`;
  return STEP_PATHS[pathId] ?? AFTER_GOAL_PATH;
}
