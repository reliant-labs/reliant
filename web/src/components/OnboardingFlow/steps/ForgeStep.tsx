import { FolderOpen, Sparkles } from "lucide-react";
import { cn } from "@/lib/utils";
import type { StepProps } from "../types";

export function ForgeStep({ plan, updatePlan, onNext }: StepProps) {
  const handleForge = () => {
    updatePlan({
      workflowId: "builtin://forge-one-shot",
      useForge: true,
      workflowParams: { mode: "auto", ask: true },

    });
    onNext();
  };

  const handleClean = () => {
    updatePlan({
      workflowId: "builtin://agent",
      useForge: false,
      workflowParams: { mode: "auto" },

    });
    onNext();
  };

  return (
    <div className="space-y-6">
      <div className="text-center">
        <h2 className="text-2xl font-semibold text-foreground tracking-tight">
          Choose the starting style
        </h2>
        <p className="text-sm text-muted-foreground mt-1">
          This is not another setup question; it picks the best first workflow for your new project.
        </p>
      </div>

      <div className="space-y-3">
        <button
          type="button"
          onClick={handleForge}
          className={cn(
            "relative flex items-center gap-4 w-full p-5 rounded-lg border text-left transition-all",
            "hover:bg-primary/10 hover:border-primary/50",
            plan.useForge === true
              ? "border-primary bg-primary/10"
              : "border-primary/30 bg-muted/30",
          )}
        >
          <div className="flex-shrink-0 p-2.5 rounded-lg bg-primary/15 text-primary">
            <Sparkles className="w-6 h-6" />
          </div>
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-2">
              <h3 className="text-sm font-semibold text-foreground">Use Forge</h3>
              <span className="px-1.5 py-0.5 text-[10px] font-medium bg-primary/20 text-primary rounded">
                Recommended
              </span>
            </div>
            <p className="text-xs text-muted-foreground mt-0.5">
              Describe the app and Forge creates the project structure, files, and configuration.
            </p>
          </div>
        </button>

        <button
          type="button"
          onClick={handleClean}
          className={cn(
            "flex items-center gap-4 w-full p-4 rounded-lg border text-left transition-all",
            "hover:bg-muted/70 hover:border-border",
            plan.useForge === false
              ? "border-primary bg-primary/10"
              : "border-border/60 bg-muted/30",
          )}
        >
          <div className="flex-shrink-0 p-2 rounded-lg bg-muted text-muted-foreground">
            <FolderOpen className="w-5 h-5" />
          </div>
          <div className="min-w-0">
            <h3 className="text-sm font-medium text-foreground">Clean project</h3>
            <p className="text-xs text-muted-foreground mt-0.5">
              Start with a minimal folder and let the agent scaffold only what you ask for.
            </p>
          </div>
        </button>
      </div>
    </div>
  );
}