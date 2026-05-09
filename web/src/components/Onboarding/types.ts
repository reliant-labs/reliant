/**
 * Onboarding Types
 *
 * Types for the guided tour + achievement-based checklist onboarding system.
 */

// ─── Tour Step Types ─────────────────────────────────────────────────────────

export type OnboardingStepId =
  | "chat-and-sidebars"
  | "workspaces"
  | "workflow-intro"
  | "workflow-hub"
  | "workflow-builder"
  | "workflow-builder-chat"
  | "presets-and-params"
  | "completion";

export type OnboardingStepType = "modal" | "spotlight" | "multi-spotlight";

export interface SpotlightConfig {
  /** Padding between the element and the spotlight cutout/border (default: 8) */
  padding?: number;
  /** Border radius for the spotlight (default: auto-detect from element) */
  borderRadius?: number | "auto" | "none";
  /** Whether to detect border-radius from the element's computed style (default: true) */
  detectBorderRadius?: boolean;
}

export interface SpotlightTarget {
  /** CSS selector for the target element */
  selector: string;
  /** Label shown in the tooltip for this target */
  label: string;
  /** Short description for this target */
  description?: string;
  /** Icon component to display */
  icon?: React.ReactNode;
  /** Spotlight config for this specific target */
  spotlightConfig?: SpotlightConfig;
}

export interface OnboardingStep {
  id: OnboardingStepId;
  type: OnboardingStepType;
  title: string;
  description: string;
  /** For spotlight steps — CSS selector for the target element */
  targetSelector?: string;
  /** For multi-spotlight steps — multiple targets with labels */
  targets?: SpotlightTarget[];
  /** Whether step can be skipped (all steps are skippable, but this is explicit) */
  skippable: boolean;
  /** Spotlight customization */
  spotlightConfig?: SpotlightConfig;
}

export interface StepProps {
  onComplete: () => void;
  onBack?: () => void;
  onSkipAll: () => void;
  stepNumber: number;
  totalSteps: number;
}

export interface SpotlightProps extends StepProps {
  targetSelector: string;
  title: string;
  description: string;
  tooltipPosition?: "top" | "bottom" | "left" | "right" | "auto";
}

// ─── Checklist Types ─────────────────────────────────────────────────────────

export type ChecklistItemId =
  | "add-api-key"
  | "start-chat"
  | "use-custom-workflow"
  | "create-workflow"
  | "take-product-tour"
  | "create-workspace"
  | "create-preset"
  | "install-mcp"
  | "read-docs";

export type ChecklistCategory = "required" | "bonus";

export type ChecklistAction =
  | "navigate"
  | "external-link"
  | "open-modal"
  | "focus-input";

export interface ChecklistItem {
  id: ChecklistItemId;
  title: string;
  description: string;
  category: ChecklistCategory;
  /** What happens when the user clicks the action button */
  action: ChecklistAction;
  /** URL for external links, or route/target identifier for navigation */
  actionTarget?: string;
  /** Button label for the action */
  actionLabel: string;
}

// ─── Store Types ─────────────────────────────────────────────────────────────

export type ChecklistPanelState = "collapsed" | "expanded" | "dismissed";