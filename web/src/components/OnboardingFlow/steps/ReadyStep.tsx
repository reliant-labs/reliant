import { useState } from "react";
import { Check, Pencil } from "lucide-react";
import { cn } from "../../../lib/utils";
import type { StepProps, LaunchPlan } from "../types";
import { useOnboardingFlowStore } from "../onboardingStore";
import { useOnboardingComplete } from "../useOnboardingComplete";

const INTENT_LABELS: Record<string, string> = {
  build_app: "Build an app",
  existing_codebase: "Work on existing code",
  landing_page: "Create landing page",
  pitch_deck: "Create pitch deck",
  blog_post: "Write blog / doc",
  explore: "Just explore",
};

const SOURCE_LABELS: Record<string, string> = {
  new_project: "Start from scratch",
  github_repo: "Import from GitHub",
  local_folder: "Open local folder",
  sample_project: "Sample project",
};

export function ReadyStep({ plan }: StepProps) {
  const [starting, setStarting] = useState(false);
  const { completeOnboarding } = useOnboardingComplete();
  const store = useOnboardingFlowStore;

  const goToStep = (stepIndex: number) => {
    store.setState({ currentStepIndex: stepIndex });
  };

  const summaryItems = [
    {
      label: "Goal",
      value: plan.intent ? INTENT_LABELS[plan.intent] : undefined,
      changeStep: 0,
    },
    {
      label: "Source",
      value: plan.codeSource ? SOURCE_LABELS[plan.codeSource] : undefined,
      changeStep: 1,
    },
    {
      label: "Workflow",
      value: plan.workflowId,
      changeStep: undefined,
    },
    ...(plan.useForge !== undefined
      ? [
          {
            label: "Forge",
            value: plan.useForge ? "Enabled" : "Disabled",
            changeStep: undefined as number | undefined,
          },
        ]
      : []),
  ].filter((item) => item.value);

  return (
    <div className="space-y-6">
      <div className="text-center">
        <h2 className="text-2xl font-semibold text-foreground tracking-tight">
          Ready to go!
        </h2>
        <p className="text-sm text-muted-foreground mt-1">
          Here&apos;s what we&apos;ll set up for you
        </p>
      </div>

      {/* Summary */}
      <div className="space-y-2">
        {summaryItems.map((item) => (
          <div
            key={item.label}
            className="flex items-center justify-between p-3 rounded-lg bg-muted/50 border border-border/40"
          >
            <div className="flex items-center gap-2">
              <Check className="w-4 h-4 text-primary flex-shrink-0" />
              <span className="text-xs text-muted-foreground">
                {item.label}
              </span>
              <span className="text-sm text-foreground font-medium">
                {item.value}
              </span>
            </div>
            {item.changeStep !== undefined && (
              <button
                type="button"
                onClick={() => goToStep(item.changeStep!)}
                className="flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground transition-colors"
              >
                <Pencil className="w-3 h-3" />
                Change
              </button>
            )}
          </div>
        ))}
      </div>

      {/* Start button */}
      <div className="text-center pt-2">
        <button
          type="button"
          disabled={starting}
          onClick={async () => {
            setStarting(true);
            try {
              await completeOnboarding(plan as LaunchPlan);
            } finally {
              useOnboardingFlowStore.getState().completeOnboarding();
              setStarting(false);
            }
          }}
          className={cn(
            "inline-flex items-center gap-2 px-8 py-3 text-sm font-medium rounded-lg transition-colors",
            starting
              ? "bg-muted text-muted-foreground cursor-not-allowed"
              : "bg-primary text-primary-foreground hover:bg-primary/90"
          )}
        >
          {starting ? "Starting..." : "Start building"}
        </button>
      </div>
    </div>
  );
}