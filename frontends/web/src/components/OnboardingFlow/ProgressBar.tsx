import { Check } from "lucide-react";
import { cn } from "@/lib/utils";

interface ProgressBarStep {
  id: string;
  label: string;
}

interface ProgressBarProps {
  steps: ProgressBarStep[];
  currentStepIndex: number;
}

export function ProgressBar({ steps, currentStepIndex }: ProgressBarProps) {
  return (
    <div className="flex items-center gap-1 px-2">
      {steps.map((step, index) => {
        const isCompleted = index < currentStepIndex;
        const isCurrent = index === currentStepIndex;

        return (
          <div key={step.id} className="flex flex-1 items-center gap-1">
            <div className="flex flex-1 items-center gap-2">
              <div
                className={cn(
                  "flex h-6 w-6 flex-shrink-0 items-center justify-center rounded-full text-xs font-medium transition-all",
                  isCompleted && "bg-gradient-to-br from-emerald-400 to-cyan-500 text-white shadow-sm shadow-cyan-500/30",
                  isCurrent && "bg-gradient-to-br from-sky-500 to-violet-500 text-white ring-2 ring-white/40 shadow-md shadow-violet-500/25",
                  !isCompleted && !isCurrent && "border border-white/10 bg-white/[0.06] text-muted-foreground",
                )}
              >
                {isCompleted ? <Check className="w-3.5 h-3.5" /> : index + 1}
              </div>
              <span
                className={cn(
                  "hidden whitespace-nowrap text-xs sm:inline",
                  isCurrent ? "font-medium text-foreground" : "text-muted-foreground",
                )}
              >
                {step.label}
              </span>
            </div>

            {index < steps.length - 1 && (
              <div className="mx-1 h-px min-w-4 flex-1 overflow-hidden rounded-full bg-white/10">
                <div
                  className={cn(
                    "h-full rounded-full transition-all",
                    isCompleted ? "w-full bg-gradient-to-r from-emerald-400 via-cyan-400 to-sky-500" : "w-0",
                  )}
                />
              </div>
            )}
          </div>
        );
      })}
    </div>
  );
}