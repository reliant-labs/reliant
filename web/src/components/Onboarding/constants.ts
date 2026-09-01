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
    title: "Everything in one view",
    description:
      "Chat in the middle, your history on the left, and your project's files and changes on the right.",
    targets: [
      {
        selector: "[data-onboarding='left-sidebar']",
        label: "Conversations",
        description: "Every chat you've had, organized by workspace",
      },
      {
        selector: "[data-onboarding='chat-input']",
        label: "Ask anything",
        description: "Ask it to fix a bug, write a test, or explain a file",
      },
      {
        selector: "[data-onboarding='right-sidebar']",
        label: "Project context",
        description: "The files it's touched, the diffs it's made, and what's running",
      },
    ],
    skippable: true,
  },
  {
    id: "workspaces",
    type: "spotlight",
    title: "Workspaces",
    description:
      "Work on more than one thing at once — each workspace is its own git branch, so nothing collides with your main code.",
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
      "Automate a whole task — like fixing a bug and opening a PR — as one multi-step run instead of message by message.",
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
    title: "Ready-made workflows",
    description:
      "Pick a template built for your task — Agent for a quick one-off, or Checklist for a full build-test-review pipeline.",
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
    title: "Build your own",
    description:
      "Drag steps onto the canvas and connect them to design your own multi-step process.",
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
    title: "Describe, don't draw",
    description:
      "Describe what you want in plain English and it builds the workflow for you.",
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
    description: "Connect your AI provider so Reliant can start working",
    category: "required",
    action: "open-modal",
    actionTarget: "api-key-setup",
    actionLabel: "Add key",
  },
  {
    id: "start-chat",
    title: "Start a chat",
    description: "Ask it to fix a bug, write a test, or explain some code",
    category: "required",
    action: "focus-input",
    actionLabel: "Start chatting",
  },
  {
    id: "use-custom-workflow",
    title: "Try a workflow",
    description: "Run a chat using a ready-made workflow instead of the default",
    category: "required",
    action: "navigate",
    actionTarget: "workflow-hub",
    actionLabel: "Browse workflows",
  },
  {
    id: "create-workflow",
    title: "Design your own workflow",
    description: "Put together a multi-step process in the visual builder",
    category: "required",
    action: "navigate",
    actionTarget: "workflow-builder",
    actionLabel: "Open builder",
  },
  {
    id: "take-product-tour",
    title: "Take the tour",
    description: "A two-minute walkthrough of chat, workspaces, and workflows",
    category: "required",
    action: "navigate",
    actionTarget: "product-tour",
    actionLabel: "Start tour",
  },

  // Bonus items (deeper engagement)
  {
    id: "create-workspace",
    title: "Create a workspace",
    description: "Start a feature on its own branch, without touching main",
    category: "bonus",
    action: "navigate",
    actionTarget: "create-workspace",
    actionLabel: "Create",
  },
  {
    id: "create-preset",
    title: "Save a preset",
    description: "Save your favorite agent settings so you can reuse them later",
    category: "bonus",
    action: "navigate",
    actionTarget: "workflow-hub-presets",
    actionLabel: "Create",
  },
  {
    id: "install-mcp",
    title: "Connect a tool",
    description: "Give the AI access to other tools and services via MCP",
    category: "bonus",
    action: "navigate",
    actionTarget: "settings-mcp",
    actionLabel: "Browse",
  },
  {
    id: "read-docs",
    title: "Read the docs",
    description: "Learn more about workflows, presets, and everything else",
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