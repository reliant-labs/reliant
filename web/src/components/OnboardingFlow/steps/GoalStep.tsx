import {
  ChartColumn,
  Compass,
  FolderCode,
  Palette,
  PenLine,
  Rocket,
  type LucideIcon,
} from "lucide-react";
import { cn } from "../../../lib/utils";
import type { StepProps, OnboardingIntent, CodeSource } from "../types";

const INTENT_OPTIONS: {
  intent: OnboardingIntent;
  icon: LucideIcon;
  label: string;
  description: string;
  workflowId: string;
  presetId?: string;
  codeSource?: CodeSource;
}[] = [
  {
    intent: "build_app",
    icon: Rocket,
    label: "Build an app",
    description: "Start a new project from scratch",
    workflowId: "forge-one-shot",
  },
  {
    intent: "existing_codebase",
    icon: FolderCode,
    label: "Work on existing code",
    description: "Navigate and improve your codebase",
    workflowId: "agent",
  },
  {
    intent: "landing_page",
    icon: Palette,
    label: "Create landing page",
    description: "Design and build a web page",
    workflowId: "get-it-right",
    presetId: "ux",
  },
  {
    intent: "pitch_deck",
    icon: ChartColumn,
    label: "Create pitch deck",
    description: "Build a presentation or deck",
    workflowId: "get-it-right",
  },
  {
    intent: "blog_post",
    icon: PenLine,
    label: "Write blog / doc",
    description: "Draft documentation or articles",
    workflowId: "agent",
    presetId: "documentation",
  },
  {
    intent: "explore",
    icon: Compass,
    label: "Just explore",
    description: "Browse features and templates",
    workflowId: "agent",
    codeSource: "sample_project",
  },
];

export function GoalStep({ plan, updatePlan, onNext }: StepProps) {
  const handleSelect = (option: (typeof INTENT_OPTIONS)[number]) => {
    updatePlan({
      intent: option.intent,
      workflowId: option.workflowId,
      presetId: option.presetId,
      codeSource: option.codeSource,
    });
    onNext();
  };

  return (
    <div className="space-y-6">
      <div className="text-center">
        <h2 className="text-2xl font-semibold text-foreground tracking-tight">
          What do you want to do first?
        </h2>
        <p className="text-sm text-muted-foreground mt-1">
          Pick one — you can always change later
        </p>
      </div>

      <div className="grid grid-cols-2 gap-3">
        {INTENT_OPTIONS.map((option) => {
          const Icon = option.icon;
          return (
            <button
              key={option.intent}
              type="button"
              onClick={() => handleSelect(option)}
              className={cn(
                "flex items-start gap-3 p-4 rounded-lg border text-left transition-all",
                "hover:bg-muted/70 hover:border-primary/40",
                plan.intent === option.intent
                  ? "border-primary bg-primary/10"
                  : "border-border/60 bg-muted/30"
              )}
            >
              <div className="flex-shrink-0 p-2 rounded-lg bg-primary/15 text-primary mt-0.5">
                <Icon className="w-5 h-5" aria-hidden="true" />
              </div>
              <div className="min-w-0">
                <h3 className="text-sm font-medium text-foreground">
                  {option.label}
                </h3>
                <p className="text-xs text-muted-foreground mt-0.5">
                  {option.description}
                </p>
              </div>
            </button>
          );
        })}
      </div>
    </div>
  );
}