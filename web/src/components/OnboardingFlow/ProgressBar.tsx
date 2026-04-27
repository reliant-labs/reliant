import { Check } from "lucide-react";
import { cn } from "../../lib/utils";
import { stepRegistry } from "./StepRegistry";

interface ProgressBarProps {
  currentCategory: string;
}

const CATEGORY_LABELS: Record<string, string> = {
  goal: "Goal",
  workspace: "Workspace",
  compute: "Compute",
  start: "Start",
};

export function ProgressBar({ currentCategory }: ProgressBarProps) {
  const categories = stepRegistry.getCategories();
  const currentIdx = categories.indexOf(currentCategory);

  return (
    <div className="flex items-center gap-1 px-2">
      {categories.map((cat, i) => {
        const isCompleted = i < currentIdx;
        const isCurrent = i === currentIdx;

        return (
          <div key={cat} className="flex items-center gap-1 flex-1">
            {/* Step indicator */}
            <div className="flex items-center gap-2 flex-1">
              <div
                className={cn(
                  "w-6 h-6 rounded-full flex items-center justify-center text-xs font-medium transition-colors flex-shrink-0",
                  isCompleted &&
                    "bg-primary text-primary-foreground",
                  isCurrent &&
                    "ring-2 ring-primary bg-primary/15 text-primary",
                  !isCompleted &&
                    !isCurrent &&
                    "bg-muted text-muted-foreground"
                )}
              >
                {isCompleted ? (
                  <Check className="w-3.5 h-3.5" />
                ) : (
                  i + 1
                )}
              </div>
              <span
                className={cn(
                  "text-xs whitespace-nowrap hidden sm:inline",
                  isCurrent
                    ? "text-foreground font-medium"
                    : "text-muted-foreground"
                )}
              >
                {CATEGORY_LABELS[cat] ?? cat}
              </span>
            </div>

            {/* Connector line */}
            {i < categories.length - 1 && (
              <div
                className={cn(
                  "h-px flex-1 min-w-4 mx-1",
                  i < currentIdx ? "bg-primary" : "bg-border"
                )}
              />
            )}
          </div>
        );
      })}
    </div>
  );
}
