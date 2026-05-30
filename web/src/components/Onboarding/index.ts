/**
 * Onboarding Module
 *
 * Exports for the achievement-based onboarding checklist system.
 */

export { OnboardingWizard } from "./OnboardingWizard";
export { ContextualTipsLayer } from "./ContextualTipsLayer";
export { OnboardingModal } from "./OnboardingModal";
export { OnboardingChecklist } from "./OnboardingChecklist";
export { OnboardingSpotlight } from "./OnboardingSpotlight";
export { OnboardingMultiSpotlight } from "./OnboardingMultiSpotlight";
export { OnboardingNavBar } from "./OnboardingNavBar";
export { CHECKLIST_ITEMS, CHECKLIST_SETTINGS_KEYS, ONBOARDING_STEPS, ONBOARDING_STEP_IDS } from "./constants";
export { useTourNavigation, STEP_EXPECTED_PATH } from "./useTourNavigation";
export type { TourNavigation } from "./useTourNavigation";
export type { ChecklistItemId, ChecklistItem, StepProps, OnboardingStepId } from "./types";