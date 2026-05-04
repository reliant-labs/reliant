import { ChevronLeft, FolderOpen, Github, Sparkles } from "lucide-react";
import { useEffect } from "react";
import { cn } from "../../../lib/utils";
import type { StepProps, CodeSource } from "../types";

const SOURCE_OPTIONS: {
  source: CodeSource;
  icon: React.ComponentType<{ className?: string }>;
  label: string;
  description: string;
}[] = [
  {
    source: "new_project",
    icon: Sparkles,
    label: "Start from scratch",
    description: "Create a new project with AI assistance",
  },
  {
    source: "github_repo",
    icon: Github,
    label: "Import from GitHub",
    description: "Clone an existing repository",
  },
  {
    source: "local_folder",
    icon: FolderOpen,
    label: "Open local folder",
    description: "Use a project already on your machine",
  },
];

export function ProjectSourceStep({
  plan,
  updatePlan,
  onNext,
  onBack,
}: StepProps) {
  // If working on existing code, auto-set and skip
  useEffect(() => {
    if (plan.intent === "existing_codebase") {
      updatePlan({ codeSource: "github_repo" });
      onNext();
    }
  }, [plan.intent, updatePlan, onNext]);

  // Don't render if auto-skipping
  if (plan.intent === "existing_codebase") {
    return null;
  }

  const handleSelect = (source: CodeSource) => {
    updatePlan({ codeSource: source });
    onNext();
  };

  return (
    <div className="space-y-6">
      <div className="text-center">
        <h2 className="text-2xl font-semibold text-foreground tracking-tight">
          Starting fresh or existing code?
        </h2>
        <p className="text-sm text-muted-foreground mt-1">
          Choose where your project lives
        </p>
      </div>

      <div className="space-y-3">
        {SOURCE_OPTIONS.map((option) => {
          const Icon = option.icon;
          return (
            <button
              key={option.source}
              type="button"
              onClick={() => handleSelect(option.source)}
              className={cn(
                "flex items-center gap-4 w-full p-4 rounded-lg border text-left transition-all",
                "hover:bg-muted/70 hover:border-primary/40",
                plan.codeSource === option.source
                  ? "border-primary bg-primary/10"
                  : "border-border/60 bg-muted/30"
              )}
            >
              <div className="flex-shrink-0 p-2 rounded-lg bg-primary/15 text-primary">
                <Icon className="w-5 h-5" />
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

      <button
        type="button"
        onClick={onBack}
        className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground transition-colors"
      >
        <ChevronLeft className="w-3.5 h-3.5" aria-hidden="true" />
        Change goal
      </button>
    </div>
  );
}