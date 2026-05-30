import { useEffect, useRef } from "react";
import { trackEvent } from "@/lib/analytics";
import type { LaunchPlan } from "./types";

// Bridges the gap between the terminal-step finalize handlers and the
// OnboardingPage unmount cleanup. Without it, the cleanup can't tell
// "user finished" from "user bailed" — both look like unmount — so every
// completion would also fire onboarding_abandoned.
let completedFlag = false;

export function markOnboardingFinalized(
  plan: Partial<LaunchPlan>,
  source: "new" | "github" | "existing",
): void {
  completedFlag = true;
  trackEvent("onboarding_completed", {
    provider: plan.modelProvider ?? "unknown",
    compute: plan.compute ?? "unknown",
    code_source: plan.codeSource ?? "unknown",
    intent: plan.intent ?? "unknown",
    use_forge: plan.useForge ?? false,
    project_source: source,
  });
}

export function useOnboardingTracking(currentStep: string): void {
  const flowStartRef = useRef<number | null>(null);
  const stepStartRef = useRef<number | null>(null);
  const lastStepRef = useRef<string | null>(null);

  useEffect(() => {
    flowStartRef.current = Date.now();
    stepStartRef.current = Date.now();
    lastStepRef.current = currentStep;
    completedFlag = false;
    trackEvent("onboarding_flow_started", { initial_step: currentStep });
    trackEvent("onboarding_flow_step_viewed", { step: currentStep });

    return () => {
      if (completedFlag) {
        completedFlag = false;
        return;
      }
      if (flowStartRef.current && lastStepRef.current) {
        trackEvent("onboarding_flow_abandoned", {
          last_step: lastStepRef.current,
          duration_ms: Date.now() - flowStartRef.current,
        });
      }
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    if (lastStepRef.current === null || lastStepRef.current === currentStep) {
      return;
    }
    if (stepStartRef.current) {
      trackEvent("onboarding_flow_step_completed", {
        step: lastStepRef.current,
        duration_ms: Date.now() - stepStartRef.current,
      });
    }
    trackEvent("onboarding_flow_step_viewed", { step: currentStep });
    lastStepRef.current = currentStep;
    stepStartRef.current = Date.now();
  }, [currentStep]);
}
