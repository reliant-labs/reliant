/**
 * Onboarding Constants
 *
 * Step definitions for the 8-step guided tour, checklist items for the
 * achievement-based system, and shared settings keys.
 *
 * Flow: Tour (7 spotlight steps) → Completion → Checklist
 */

import type {
  OnboardingStep,
  OnboardingStepId,
  ChecklistItem,
  ChecklistItemId,
} from "./types";

// ─── Tour Steps ──────────────────────────────────────────────────────────────

export const ONBOARDING_STEPS: OnboardingStep[] = [
  {
    id: "chat-and-sidebars",
    type: "multi-spotlight",
    title: "Your Development Environment",
    description:
      "Three panels work together — conversations on the left, AI chat in the center, and project context on the right.",
    targets: [
      {
        selector: "[data-onboarding='left-sidebar']",
        label: "Conversations",
        description: "Chat history grouped by workspace",
      },
      {
        selector: "[data-onboarding='chat-input']",
        label: "AI Chat",
        description: "Ask anything — code, debug, refactor",
      },
      {
        selector: "[data-onboarding='right-sidebar']",
        label: "Project Context",
        description: "Files, changes, processes, tasks",
      },
    ],
    skippable: true,
  },
  {
    id: "workspaces",
    type: "spotlight",
    title: "Workspaces",
    description:
      "Isolated git branches for parallel work. Create, switch, and manage them here.",
    targetSelector: "[data-onboarding='workspace-buttons']",
    skippable: true,
    spotlightConfig: {
      padding: 8,
      detectBorderRadius: true,
    },
  },
  {
    id: "workflow-intro",
    type: "spotlight",
    title: "Workflows",
    description:
      "Multi-agent AI pipelines defined in YAML — with loops, branches, parallel execution, and approval gates.",
    targetSelector: "[data-onboarding='workflow-button']",
    skippable: true,
    spotlightConfig: {
      padding: 4,
      detectBorderRadius: true,
    },
  },
  {
    id: "workflow-hub",
    type: "spotlight",
    title: "Workflow Templates",
    description:
      "Browse built-in templates or create your own. Start with Agent for general tasks, or Checklist for a full development pipeline.",
    targetSelector: "[data-onboarding='workflow-hub']",
    skippable: true,
    spotlightConfig: {
      padding: 16,
      detectBorderRadius: true,
    },
  },
  {
    id: "workflow-builder",
    type: "spotlight",
    title: "Visual Workflow Builder",
    description:
      "Build workflows visually with drag-and-drop. Nodes represent steps, edges show the flow. Connect steps to create custom AI pipelines.",
    targetSelector: "[data-onboarding='workflow-canvas']",
    skippable: true,
    spotlightConfig: {
      padding: 0,
      borderRadius: "none",
    },
  },
  {
    id: "workflow-builder-chat",
    type: "spotlight",
    title: "Builder AI Assistant",
    description:
      "Generate an entire workflow from a one-line description, or chat to edit and refine an existing one — it writes the YAML for you.",
    targetSelector: "[data-onboarding='workflow-chat']",
    skippable: true,
    spotlightConfig: {
      padding: 0,
      borderRadius: "none",
    },
  },
  {
    id: "completion",
    type: "modal",
    title: "Ready to go",
    description: "You're all set to start building",
    skippable: false,
  },
];

// ─── Tour Step Helpers ───────────────────────────────────────────────────────

export function getStepById(
  id: OnboardingStepId,
): OnboardingStep | undefined {
  return ONBOARDING_STEPS.find((step) => step.id === id);
}

export function getStepIndex(id: OnboardingStepId): number {
  return ONBOARDING_STEPS.findIndex((step) => step.id === id);
}

export function getNextStepId(
  currentId: OnboardingStepId,
): OnboardingStepId | null {
  const currentIndex = getStepIndex(currentId);
  if (currentIndex === -1 || currentIndex >= ONBOARDING_STEPS.length - 1) {
    return null;
  }
  return ONBOARDING_STEPS[currentIndex + 1].id;
}

export function getPreviousStepId(
  currentId: OnboardingStepId,
): OnboardingStepId | null {
  const currentIndex = getStepIndex(currentId);
  if (currentIndex <= 0) {
    return null;
  }
  return ONBOARDING_STEPS[currentIndex - 1].id;
}

// Step context — which view mode each step requires
export const WORKFLOW_MODE_STEPS: OnboardingStepId[] = [
  "workflow-hub",
  "workflow-builder",
  "workflow-builder-chat",
];

export const WORKFLOW_BUILDER_STEPS: OnboardingStepId[] = [
  "workflow-builder",
  "workflow-builder-chat",
];

export const CHAT_MODE_STEPS: OnboardingStepId[] = [
  "chat-and-sidebars",
  "workspaces",
  "workflow-intro",
];

export const MODAL_STEPS: OnboardingStepId[] = [
  "completion",
];

export const SETTINGS_MODE_STEPS: OnboardingStepId[] = [];

export function stepRequiresWorkflowMode(stepId: OnboardingStepId): boolean {
  return WORKFLOW_MODE_STEPS.includes(stepId);
}

export function stepRequiresWorkflowBuilder(
  stepId: OnboardingStepId,
): boolean {
  return WORKFLOW_BUILDER_STEPS.includes(stepId);
}

export function stepRequiresChatMode(stepId: OnboardingStepId): boolean {
  return CHAT_MODE_STEPS.includes(stepId);
}

export function stepRequiresSettingsMode(stepId: OnboardingStepId): boolean {
  return SETTINGS_MODE_STEPS.includes(stepId);
}

// ─── Checklist Items ─────────────────────────────────────────────────────────

export const CHECKLIST_ITEMS: ChecklistItem[] = [
  // Required items (core onboarding)
  {
    id: "add-api-key",
    title: "Add an API key",
    description: "Connect an AI provider to start using Reliant",
    category: "required",
    action: "open-modal",
    actionTarget: "api-key-setup",
    actionLabel: "Add key",
  },
  {
    id: "start-chat",
    title: "Start a chat",
    description: "Send your first message to an AI agent",
    category: "required",
    action: "focus-input",
    actionLabel: "Start chatting",
  },
  {
    id: "use-custom-workflow",
    title: "Use a custom workflow",
    description: "Run a chat with a non-default workflow selected",
    category: "required",
    action: "navigate",
    actionTarget: "workflow-hub",
    actionLabel: "Browse workflows",
  },
  {
    id: "create-workflow",
    title: "Create a workflow",
    description: "Build a custom AI pipeline in the visual editor",
    category: "required",
    action: "navigate",
    actionTarget: "workflow-builder",
    actionLabel: "Open builder",
  },
  {
    id: "take-product-tour",
    title: "Take the product tour",
    description: "Walk through Reliant's chat, workspace, and workflow basics",
    category: "required",
    action: "navigate",
    actionTarget: "product-tour",
    actionLabel: "Start tour",
  },

  // Bonus items (deeper engagement)
  {
    id: "create-workspace",
    title: "Create a workspace",
    description: "Work on a feature in an isolated git branch",
    category: "bonus",
    action: "navigate",
    actionTarget: "create-workspace",
    actionLabel: "Create",
  },
  {
    id: "create-preset",
    title: "Create a preset",
    description: "Save custom agent settings for reuse",
    category: "bonus",
    action: "navigate",
    actionTarget: "workflow-hub-presets",
    actionLabel: "Create",
  },
  {
    id: "install-mcp",
    title: "Install an MCP server",
    description: "Extend AI capabilities with external tools",
    category: "bonus",
    action: "navigate",
    actionTarget: "settings-mcp",
    actionLabel: "Browse",
  },
  {
    id: "read-docs",
    title: "Read the docs",
    description: "Learn about workflows, presets, and more",
    category: "bonus",
    action: "external-link",
    actionTarget: "https://docs.reliantlabs.io/",
    actionLabel: "Open docs",
  },
];

export const REQUIRED_ITEMS = CHECKLIST_ITEMS.filter(
  (i) => i.category === "required",
);
export const BONUS_ITEMS = CHECKLIST_ITEMS.filter(
  (i) => i.category === "bonus",
);

export function getChecklistItem(
  id: ChecklistItemId,
): ChecklistItem | undefined {
  return CHECKLIST_ITEMS.find((item) => item.id === id);
}

// ─── Settings Keys ───────────────────────────────────────────────────────────

export const CHECKLIST_SETTINGS_KEYS = {
  COMPLETED_ITEMS: "onboarding.checklist.completed_items",
  PANEL_STATE: "onboarding.checklist.panel_state",
  WELCOME_SHOWN: "onboarding.checklist.welcome_shown",
} as const;

/** Tour-specific settings keys */
export const TOUR_SETTINGS_KEYS = {
  COMPLETED: "onboarding.completed",
  SKIPPED_ALL: "onboarding.skipped_all",
  // CURRENT_STEP is intentionally absent: the URL (?tour=<step>) is the
  // source of truth for the active step. tourStore.loadState() best-effort
  // deletes the stale row so old installs don't carry the value forever.
  CURRENT_STEP: "onboarding.current_step",
  COMPLETED_STEPS: "onboarding.completed_steps",
  SKIPPED_STEPS: "onboarding.skipped_steps",
} as const;

// ─── Step ID tuple (for Zod search-param schemas) ───────────────────────────
// Hard-coded here (not derived from ONBOARDING_STEPS) so it can be imported by
// routeSchemas.ts without dragging in this module's other dependencies. The
// build-time consistency check below (top-level throw on mismatch) guarantees
// the tuple stays in sync with ONBOARDING_STEPS.
export const ONBOARDING_STEP_IDS = [
  "chat-and-sidebars",
  "workspaces",
  "workflow-intro",
  "workflow-hub",
  "workflow-builder",
  "workflow-builder-chat",
  "completion",
] as const satisfies readonly OnboardingStepId[];

if (
  ONBOARDING_STEP_IDS.length !== ONBOARDING_STEPS.length ||
  !ONBOARDING_STEP_IDS.every((id, i) => ONBOARDING_STEPS[i].id === id)
) {
  // Module-load failure surfaces immediately in dev and in tests; we never
  // want the wizard's URL schema to silently drift from the step list.
  throw new Error(
    "[Onboarding] ONBOARDING_STEP_IDS does not match ONBOARDING_STEPS — update one to match the other.",
  );
}