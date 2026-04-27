import { createPortal } from "react-dom";
import { ChevronLeft, ChevronRight } from "lucide-react";
import { cn } from "../../lib/utils";
import { useOnboardingFlowStore } from "./onboardingStore";
import { ProgressBar } from "./ProgressBar";
import { stepRegistry } from "./StepRegistry";

export function OnboardingOverlay() {
  const state = useOnboardingFlowStore((s) => s.state);
  const plan = useOnboardingFlowStore((s) => s.plan);
  const currentStepIndex = useOnboardingFlowStore((s) => s.currentStepIndex);
  const updatePlan = useOnboardingFlowStore((s) => s.updatePlan);
  const nextStep = useOnboardingFlowStore((s) => s.nextStep);
  const prevStep = useOnboardingFlowStore((s) => s.prevStep);
  const skipOnboarding = useOnboardingFlowStore((s) => s.skipOnboarding);
  const currentCategory = useOnboardingFlowStore((s) => s.currentCategory);

  // Don't render on admin or auth pages
  if (typeof window !== "undefined") {
    const path = window.location.pathname;
    if (path.includes("/admin") || path.includes("/auth")) {
      return null;
    }
  }

  // Only show when not_started or in_progress
  if (state !== "not_started" && state !== "in_progress") {
    return null;
  }

  // Need at least one registered step
  const visibleSteps = stepRegistry.getVisibleSteps(plan);
  if (visibleSteps.length === 0) {
    return null;
  }

  const currentStep = visibleSteps[currentStepIndex] ?? null;
  if (!currentStep) {
    return null;
  }

  const StepComponent = currentStep.component;
  const isFirst = currentStepIndex === 0;
  const isLast = currentStepIndex === visibleSteps.length - 1;

  const overlay = (
    <div
      className="fixed inset-0 z-40 flex items-center justify-center p-4"
      role="dialog"
      aria-modal="true"
      aria-label="Onboarding setup"
    >
      {/* Backdrop */}
      <div
        className="absolute inset-0 bg-black/70 backdrop-blur-sm"
        aria-hidden="true"
      />

      {/* Card */}
      <div
        className={cn(
          "relative w-full max-w-[680px] border border-border/50 rounded-xl font-sans overflow-hidden flex flex-col",
          "max-h-[calc(100vh-80px)]",
          "elevation-5",
          "animate-in fade-in-0 zoom-in-98 duration-300"
        )}
      >
        {/* Top bar: progress + sign in */}
        <div className="flex items-center justify-between px-6 py-4 border-b border-border/60 elevation-1">
          <div className="flex-1">
            <ProgressBar currentCategory={currentCategory()} />
          </div>
          <a
            href="/auth"
            className="ml-4 text-xs text-muted-foreground hover:text-foreground transition-colors whitespace-nowrap"
          >
            Sign in
          </a>
        </div>

        {/* Step content */}
        <div className="flex-1 overflow-y-auto bg-[hsl(var(--surface-modal))]">
          <div className="px-8 py-8">
            <StepComponent
              plan={plan}
              updatePlan={updatePlan}
              onNext={nextStep}
              onBack={prevStep}
              onSkip={skipOnboarding}
            />
          </div>
        </div>

        {/* Footer navigation */}
        <div className="flex items-center justify-between px-6 py-4 border-t border-border/60 bg-[hsl(var(--surface-modal))]">
          <button
            type="button"
            onClick={skipOnboarding}
            className="text-xs text-muted-foreground hover:text-foreground transition-colors"
          >
            Skip setup
          </button>

          <div className="flex items-center gap-2">
            {!isFirst && (
              <button
                type="button"
                onClick={prevStep}
                className="flex items-center gap-1 px-4 py-2 text-sm border border-border rounded-lg hover:bg-muted transition-colors"
              >
                <ChevronLeft className="w-4 h-4" />
                Back
              </button>
            )}
            {!isLast && (
              <button
                type="button"
                onClick={nextStep}
                className="flex items-center gap-1 px-4 py-2 text-sm bg-primary text-primary-foreground rounded-lg hover:bg-primary/90 transition-colors"
              >
                Next
                <ChevronRight className="w-4 h-4" />
              </button>
            )}
          </div>
        </div>
      </div>
    </div>
  );

  return createPortal(overlay, document.body);
}
