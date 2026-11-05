/**
 * Onboarding Constants
 *
 * Checklist items for the achievement-based system, and shared settings
 * keys. The tour's steps are journeys: see journeys/registry.ts and
 * JOURNEYS.md.
 */

import type { ChecklistItem, ChecklistItemId } from "./types";
import type { JourneyId } from "./tourParam";

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
    title: "Take a tour",
    description: "A short guided tour: build an AI workflow, or explore a forge app",
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
  /** Some journey (or the picker) was finished or skipped; also the retired single tour's flag. */
  COMPLETED: "onboarding.completed",
  SKIPPED_ALL: "onboarding.skipped_all",
  // CURRENT_STEP is intentionally absent: the URL (?tour=<journey>:<step>) is
  // the source of truth for the active step. tourStore.loadState()
  // best-effort deletes the stale row so old installs don't carry it forever.
  CURRENT_STEP: "onboarding.current_step",
  /** `<journey>:<step>` keys (bare step ids from the retired tour may remain). */
  COMPLETED_STEPS: "onboarding.completed_steps",
  SKIPPED_STEPS: "onboarding.skipped_steps",
  /** The journey the user last chose. */
  LAST_JOURNEY: "onboarding.journey",
  /** `<journey>:<step>` the user ended the tour on, offered as "Resume". */
  RESUME: "onboarding.journey_resume",
  /** JSON per-journey context ({ workflows: { workflowSlug, … } }). */
  JOURNEY_CONTEXT: "onboarding.journey_context",
} as const;

/**
 * The settings key recording a journey's outcome at one version
 * ("completed" | "skipped" | "shown"). Per version, so a changed journey is
 * offered again to users who completed an older one, and never loops.
 */
export function journeyCompletionKey(journey: JourneyId, version: number): string {
  return `onboarding.tour.${journey}.v${version}.completed`;
}