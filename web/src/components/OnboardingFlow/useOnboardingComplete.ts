/**
 * Shared helpers for onboarding completion.
 *
 * The actual completion logic lives in ModelStep.tsx — this module
 * exports reusable helpers that ModelStep (or other consumers) call directly.
 */

import { logger } from "@/lib/logger";
import type { LaunchPlan } from "./types";

/**
 * Ensure a project exists and is selected. Some onboarding paths (e.g. cloud
 * with daemonProvisioning, or github-connect when daemon isn't ready) skip
 * ProjectLocationStep's project creation. Without a project the main UI never
 * renders because hasProjects stays false.
 */
export async function ensureProject(plan: Partial<LaunchPlan>): Promise<string> {
  const { useProjectStore } = await import("@/store/projectStore");
  const store = useProjectStore.getState();

  // If a project is already selected, we're good
  if (store.currentProject) {
    return store.currentProject.id;
  }

  // Try to find an existing project matching the plan path
  if (plan.localPath) {
    await store.loadProjects();
    const existing = useProjectStore.getState().projects.find(
      (p) => p.path === plan.localPath,
    );
    if (existing) {
      await useProjectStore.getState().selectProject(existing);
      return existing.id;
    }
  }

  // Create a new project with plan data or sensible defaults
  const projectPath = plan.localPath || "/home/workspace/projects/reliant-project";
  const projectName = plan.projectName || "Reliant Project";

  logger.info("[OnboardingComplete] Creating project since none exists", {
    projectName,
    projectPath,
  });

  const created = await store.createProject({
    name: projectName,
    path: projectPath,
    description: "",
    is_git_repo: Boolean(plan.repo),
    default_branch: plan.repo?.branch || "main",
  });

  // createProject already sets currentProject, but ensure selection is complete
  await useProjectStore.getState().selectProject(created);

  // Refresh project list so hasProjects becomes true in ModernApp
  await store.loadProjects();

  return created.id;
}
