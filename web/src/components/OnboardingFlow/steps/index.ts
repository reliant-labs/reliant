import { registerStepComponents } from "../stepConfig";
import type { LaunchPlan } from "../types";
import { ComputeStep } from "./ComputeStep";
import { DaemonConnectStep } from "./DaemonConnectStep";
import { ForgeStep } from "./ForgeStep";
import { GitHubConnectStep } from "./GitHubConnectStep";
import { GoalStep } from "./GoalStep";
import { ModelStep } from "./ModelStep";
import { ProjectLocationStep } from "./ProjectLocationStep";

registerStepComponents({
  'goal': GoalStep,
  'compute': ComputeStep,
  'daemon-connect': DaemonConnectStep,
  'github-connect': GitHubConnectStep,
  'local-project-location': ProjectLocationStep,
  'forge-style': ForgeStep,
  'model': ModelStep,
});

// ── Fixed step paths ────────────────────────────────────────

export const STEP_PATHS: Record<string, string[]> = {
  // Cloud paths
  cloud_new_app: ["goal", "compute", "forge-style", "model"],
  cloud_existing: ["goal", "compute", "github-connect", "model"],
  cloud_explore: ["goal", "compute", "model"],
  cloud_landing_page: ["goal", "compute", "model"],
  cloud_pitch_deck: ["goal", "compute", "model"],
  cloud_blog_post: ["goal", "compute", "model"],

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