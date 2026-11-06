/**
 * A step's controls inside its own card (Journey.controls "callout"), in
 * place of the bottom nav bar: the step count, one small Exit tour, and Next
 * only where there is no required action to do. A required action's
 * instruction is the card's copy; `TourActionHint` repeats it as one line
 * (announced to screen readers), and the tour moves on when it's done.
 */

import { Check, MousePointerClick } from "lucide-react";

export function TourActionHint({ hint, done }: { hint?: string; done: boolean }) {
  if (done) {
    return (
      <p aria-live="polite" className="mt-3 flex items-center gap-1.5 text-xs font-medium text-success-ink" data-testid="tour-action-done">
        <Check className="h-3.5 w-3.5" aria-hidden="true" />
        Done
      </p>
    );
  }
  if (!hint) return null;
  return (
    <p aria-live="polite" className="mt-3 flex items-center gap-1.5 text-xs font-medium text-foreground" data-testid="tour-action-hint">
      <MousePointerClick className="h-3.5 w-3.5 text-primary" aria-hidden="true" />
      {hint}
    </p>
  );
}

export interface TourControlsProps {
  stepNumber: number;
  totalSteps: number;
  /** Ends the tour (recorded as skipped at this step). Hidden on the last step, where Finish ends it. */
  onExit?: () => void;
  /** Next / Finish; omitted while a required action is pending. */
  onNext?: () => void;
  nextLabel?: string;
  /** The end card's second way out ("Try AI workflows in Reliant"). */
  secondary?: { label: string; onClick: () => void };
}

export function TourControls({ stepNumber, totalSteps, onExit, onNext, nextLabel = "Next", secondary }: TourControlsProps) {
  return (
    <div className="mt-4 flex items-center gap-3 border-t border-border pt-3" data-testid="tour-controls">
      <span className="text-xs tabular-nums text-muted-foreground">
        {stepNumber} of {totalSteps}
      </span>
      <span className="flex-1" />
      {onExit && (
        <button
          type="button"
          onClick={onExit}
          className="text-xs font-medium text-muted-foreground underline-offset-2 hover:text-foreground hover:underline"
          data-testid="tour-exit"
        >
          Exit tour
        </button>
      )}
      {secondary && (
        <button
          type="button"
          onClick={secondary.onClick}
          className="text-xs font-medium text-foreground underline-offset-2 hover:underline"
          data-testid="tour-secondary"
        >
          {secondary.label}
        </button>
      )}
      {onNext && (
        <button
          type="button"
          onClick={onNext}
          className="rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground hover:bg-primary/90"
          data-testid="tour-next"
        >
          {nextLabel}
        </button>
      )}
    </div>
  );
}
