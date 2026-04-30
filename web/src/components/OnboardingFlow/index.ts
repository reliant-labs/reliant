// Import steps so registration happens on module load
import "./steps";
// Import cloud completion handler (registers only when VITE_CONTROL_PLANE_API_URL is set)
import "./completeHandler";
// Initialize cloud hydration when API is available
import { registerHydrationFn } from "./onboardingStore";
import { getCurrentUser, initOnboardingApi } from "./api";
import { supabase } from "@/lib/supabase";

// Set up cloud hydration if control-plane API is configured
if (import.meta.env.VITE_CONTROL_PLANE_API_URL) {
  const apiUrl = import.meta.env.VITE_CONTROL_PLANE_API_URL;
  initOnboardingApi(apiUrl, async () => {
    try {
      const { data: { session } } = await supabase.auth.getSession();
      return session?.access_token ?? null;
    } catch {
      return null;
    }
  });

  registerHydrationFn(async () => {
    const res = await getCurrentUser();
    return res.user?.onboardingCompleted ?? false;
  });
}

export { OnboardingOverlay } from "./OnboardingOverlay";
export { ProgressBar } from "./ProgressBar";
export { stepRegistry } from "./StepRegistry";
export { useOnboardingFlowStore } from "./onboardingStore";
export { registerOnboardingCompleteHandler } from "./useOnboardingComplete";
export type {
  OnboardingIntent,
  ComputeChoice,
  DaemonLocation,
  CodeSource,
  ModelProvider,
  LaunchPlan,
  StepConfig,
  StepProps,
  OnboardingState,
} from "./types";