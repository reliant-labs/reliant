/**
 * Completion Step
 *
 * Final step — the critical handoff moment.
 * Shows one clear CTA, secondary links, and a subtle tip.
 */

import { useEffect } from "react";
import { ArrowRight } from "lucide-react";
import { useNavigate } from "@tanstack/react-router";
import { OnboardingModal } from "../OnboardingModal";
import type { StepProps } from "../types";
import { trackEvent } from "../../../lib/analytics";
import { useProjectStore } from "../../../store/projectStore";

export function CompletionStep({
  onComplete,
  stepNumber,
  totalSteps,
}: StepProps) {
  const navigate = useNavigate();
  const currentProject = useProjectStore((s) => s.currentProject);
  useEffect(() => {
    trackEvent("onboarding_completed", { totalSteps });
  }, [totalSteps]);

  // "Let's get started" lands the user on the chat page for their current
  // project. Without this they'd stay wherever they last were in the tour
  // (typically the workflow builder), which is not where you want to start.
  const handleQuickStart = () => {
    onComplete();
    if (currentProject?.id) {
      setTimeout(() => {
        void navigate({
          to: "/project/$projectId",
          params: { projectId: currentProject.id },
          search: {},
        });
      }, 300);
    }
  };

  return (
    <OnboardingModal
      isOpen={true}
      title="Ready to go"
      stepNumber={stepNumber}
      totalSteps={totalSteps}
      hideNavigation
      hideProgressBar
    >
      <div className="space-y-5">
        {/* Primary CTA */}
        <div className="text-center">
          <p className="text-sm text-muted-foreground mb-4">
            You're all set up. Jump in and start building.
          </p>
          <button
            type="button"
            onClick={handleQuickStart}
            className="inline-flex items-center gap-2 px-6 py-3 text-sm font-medium bg-primary text-primary-foreground rounded-lg hover:bg-primary/90 transition-colors"
          >
            Let's get started
            <ArrowRight className="w-4 h-4" />
          </button>
        </div>

        {/* Divider */}
        <div className="border-t border-border/40" />

        {/* Secondary actions as text links */}
        <div className="flex flex-col items-center gap-2 text-sm">
          <button
            type="button"
            onClick={() => {
              onComplete();
              // Defer the navigation so the tour completion flow finishes
              // first (clears active chat, persists state) before the user
              // lands on the workflow hub.
              setTimeout(() => {
                void navigate({ to: "/workflow", search: {} });
              }, 300);
            }}
            className="text-muted-foreground hover:text-primary transition-colors"
          >
            Browse workflow templates
          </button>
          <a
            href="https://docs.reliantlabs.io/"
            target="_blank"
            rel="noopener noreferrer"
            className="text-muted-foreground hover:text-primary transition-colors"
          >
            Read the docs
          </a>
          <a
            href="https://cal.com/team/reliant/onboarding"
            target="_blank"
            rel="noopener noreferrer"
            className="text-muted-foreground hover:text-primary transition-colors"
          >
            Book a walkthrough
          </a>
        </div>

        {/* Preset tip */}
        <p className="text-xs text-muted-foreground/60 text-center">
          Tip: Use presets to choose agent roles like researcher, debugger, or
          planner
        </p>

        {/* Restart hint */}
        <p className="text-xs text-muted-foreground/60 text-center">
          Restart this tour anytime from Settings → About
        </p>
      </div>
    </OnboardingModal>
  );
}