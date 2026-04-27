// Import steps so registration happens on module load
import "./steps";
import './cloud-steps/register';

export { OnboardingOverlay } from "./OnboardingOverlay";
export { ProgressBar } from "./ProgressBar";
export { stepRegistry } from "./StepRegistry";
export { useOnboardingFlowStore } from "./onboardingStore";
export { registerOnboardingCompleteHandler } from "./useOnboardingComplete";
export type {
  OnboardingIntent,
  ComputeChoice,
  CodeSource,
  ModelProvider,
  LaunchPlan,
  StepConfig,
  StepProps,
  OnboardingState,
} from "./types";