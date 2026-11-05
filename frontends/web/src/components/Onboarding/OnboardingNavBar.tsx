/**
 * Onboarding Navigation Bar
 *
 * A floating navigation bar that stays fixed at the bottom of the screen
 * during spotlight steps. Provides consistent navigation controls without
 * requiring users to track moving tooltips.
 */

import { createPortal } from "react-dom";
import { Check, ChevronLeft, ChevronRight } from "lucide-react";
import { cn } from "../../lib/utils";

interface OnboardingNavBarProps {
  stepNumber: number;
  totalSteps: number;
  onNext: () => void;
  onBack?: () => void;
  onSkipAll: () => void;
  nextLabel?: string;
  showSkipAll?: boolean;
  isVisible?: boolean;
  stepTitle?: string;
  /** Hide Next: the step waits on a required action (advanceOn). */
  nextHidden?: boolean;
  /** Shown while Next is hidden, e.g. "Click New workflow to continue". */
  actionHint?: string;
  /** "Skip step": move on without doing the step's action. */
  onSkipStep?: () => void;
  /** An extra button before Next, e.g. "Try the other journey" on the last step. */
  secondaryAction?: { label: string; onClick: () => void };
  /** The step's required action is done: show a check beside Next. */
  actionDone?: boolean;
  /** Label of the button that ends the tour. */
  skipAllLabel?: string;
  /** Label of "Skip step" (a step can rename it, e.g. "Create it for me"). */
  skipStepLabel?: string;
}

export function OnboardingNavBar({
  stepNumber,
  totalSteps,
  onNext,
  onBack,
  onSkipAll,
  nextLabel = "Next",
  showSkipAll = true,
  isVisible = true,
  stepTitle,
  nextHidden = false,
  actionHint,
  onSkipStep,
  secondaryAction,
  actionDone = false,
  skipAllLabel = "End tour",
  skipStepLabel = "Skip step",
}: OnboardingNavBarProps) {
  const progress = (stepNumber / totalSteps) * 100;

  const navContent = (
    <div
      role="navigation"
      aria-label="Tour"
      data-testid="onboarding-nav-bar"
      className={cn(
        "fixed bottom-24 left-1/2 -translate-x-1/2 z-[101]",
        "flex items-center gap-4 px-6 py-3.5",
        "rounded-xl border border-border bg-popover text-popover-foreground shadow-2xl",
        "transition-all duration-300",
        isVisible ? "opacity-100 translate-y-0" : "opacity-0 translate-y-4 pointer-events-none"
      )}
    >
      {/* Progress indicator */}
      <div className="flex items-center gap-3 pr-4 border-r border-border">
        <div className="w-28 h-1.5 bg-muted rounded-full overflow-hidden">
          <div
            className="h-full bg-primary transition-all duration-300 ease-out rounded-full"
            style={{ width: `${progress}%` }}
          />
        </div>
        <span className="text-xs text-muted-foreground whitespace-nowrap font-medium">
          {stepNumber} / {totalSteps}
        </span>
      </div>

      {/* Step title */}
      {stepTitle && (
        <span className="text-sm text-foreground font-medium px-2 max-w-48 truncate">
          {stepTitle}
        </span>
      )}

      {/* What the step is waiting for. Polite live region, so a screen
          reader hears the required action when the step opens. */}
      {nextHidden && actionHint && (
        <span aria-live="polite" className="text-sm text-muted-foreground max-w-56 truncate">
          {actionHint}
        </span>
      )}

      {actionDone && (
        <span aria-live="polite" className="flex items-center gap-1 text-sm text-success-ink">
          <Check className="w-4 h-4" aria-hidden />
          Done
        </span>
      )}

      {/* Navigation buttons */}
      <div className="flex items-center gap-2">
        {showSkipAll && (
          <button
            type="button"
            onClick={onSkipAll}
            className="px-3 py-1.5 text-sm text-muted-foreground hover:text-foreground transition-colors"
          >
            {skipAllLabel}
          </button>
        )}

        {onBack && (
          <button
            type="button"
            onClick={onBack}
            className="flex items-center gap-1 px-3 py-1.5 text-sm text-foreground border border-border rounded-lg hover:bg-muted transition-colors"
          >
            <ChevronLeft className="w-4 h-4" />
            Back
          </button>
        )}

        {onSkipStep && (
          <button
            type="button"
            onClick={onSkipStep}
            className="px-3 py-1.5 text-sm text-foreground border border-border rounded-lg hover:bg-muted transition-colors"
          >
            {skipStepLabel}
          </button>
        )}

        {secondaryAction && (
          <button
            type="button"
            onClick={secondaryAction.onClick}
            className="px-3 py-1.5 text-sm text-foreground border border-border rounded-lg hover:bg-muted transition-colors"
          >
            {secondaryAction.label}
          </button>
        )}

        {!nextHidden && (
          <button
            type="button"
            onClick={onNext}
            className="flex items-center gap-1 px-4 py-1.5 text-sm bg-primary text-primary-foreground font-medium rounded-lg hover:bg-primary/90 transition-colors"
          >
            {nextLabel}
            <ChevronRight className="w-4 h-4" />
          </button>
        )}
      </div>
    </div>
  );

  return createPortal(navContent, document.body);
}