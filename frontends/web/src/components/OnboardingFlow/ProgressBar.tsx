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
                  // One hue, the action color: green is for success states
                  // and a stepper is progress, not success. Done steps are a
                  // tint with a check, the current step the filled pill.
                  isCompleted && "bg-primary/15 text-primary-ink",
                  isCurrent && "bg-primary text-primary-foreground ring-2 ring-ring ring-offset-2 ring-offset-background",
                  !isCompleted && !isCurrent && "border border-border bg-background text-muted-foreground",
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
              <div className="mx-1 h-px min-w-4 flex-1 overflow-hidden rounded-full bg-border">
                <div
                  className={cn(
                    "h-full rounded-full transition-all",
                    isCompleted ? "w-full bg-primary" : "w-0",
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