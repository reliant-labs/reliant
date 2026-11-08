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
export { JourneyPicker } from "./JourneyPicker";
export { CHECKLIST_ITEMS, CHECKLIST_SETTINGS_KEYS } from "./constants";
export { JOURNEYS } from "./journeys/registry";
import "./journeys/catalog";
export { tourEvents } from "./tourEvents";
export type { TourEventMap, TourEventName } from "./tourEvents";
export { whenStore } from "./advanceOn";
export { useTourNavigation } from "./useTourNavigation";
export type { TourNavigation } from "./useTourNavigation";
export type { ChecklistItemId, ChecklistItem, Journey, JourneyStep, AdvanceOn } from "./types";
export type { JourneyId } from "./tourParam";