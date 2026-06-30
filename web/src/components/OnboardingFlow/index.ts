// Import steps so registration happens on module load
import "./steps";

export { OnboardingPage } from "./OnboardingPage";
export { ProgressBar } from "./ProgressBar";

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
