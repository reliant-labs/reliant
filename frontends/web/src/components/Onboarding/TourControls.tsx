/**
 * A step's controls inside its own card — the engine's default (Journey
 * `controls`, JOURNEYS.md "Controls"), in place of the retired bottom nav bar:
 *
 *   Step X of Y · Back · Next · Exit
 *
 * Next is disabled while the step waits on a required action (its
 * `actionHint` says what to do, and is the button's title), and shows a check
 * once the action is done. Exit ends the tour. Every control is at least
 * 44px on a touch screen (`touch-target`, index.css).
 */

import { Check, ChevronLeft, MousePointerClick } from "lucide-react";

/** One line in the card saying what to do (announced to screen readers). */
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
      <MousePointerClick className="h-3.5 w-3.5 text-primary-ink" aria-hidden="true" />
      {hint}
    </p>
  );
}

export interface TourControlsProps {
  stepNumber: number;
  totalSteps: number;
  /** Ends the tour (recorded as skipped at this step). Hidden on the last step, where Finish ends it. */
  onExit?: () => void;
  /** Exit's label: "Exit tour" (journeys), "Dismiss" (a one-card tour). */
  exitLabel?: string;
  /** The previous step; omitted on the first. */
  onBack?: () => void;
  /** Next / Finish. */
  onNext?: () => void;
  /** A required action is pending: Next shows, disabled. */
  nextDisabled?: boolean;
  /** Why Next is disabled (the step's actionHint). */
  nextDisabledReason?: string;
  /** The required action is done: a check beside Next. */
  actionDone?: boolean;
  nextLabel?: string;
  /** The end card's second way out ("Try AI workflows in Reliant"). */
  secondary?: { label: string; onClick: () => void };
  /** A step's own way past its action (JourneyStep.skip, e.g. "Create it for me"); there is no generic Skip step. */
  skip?: { label: string; onClick: () => void };
}

const TAP = "touch-target";

export function TourControls({
  stepNumber,
  totalSteps,
  onExit,
  exitLabel = "Exit tour",
  onBack,
  onNext,
  nextDisabled = false,
  nextDisabledReason,
  actionDone = false,
  nextLabel = "Next",
  secondary,
  skip,
}: TourControlsProps) {
  return (
    <div className="mt-4 flex flex-wrap items-center gap-x-3 gap-y-2 border-t border-border pt-3" data-testid="tour-controls">
      <span className="text-xs tabular-nums text-muted-foreground" data-testid="tour-step-count">
        Step {stepNumber} of {totalSteps}
      </span>
      <span className="flex-1" />
      {onExit && (
        <button
          type="button"
          onClick={onExit}
          className={`${TAP} px-1 text-xs font-medium text-muted-foreground underline-offset-2 hover:text-foreground hover:underline`}
          data-testid="tour-exit"
        >
          {exitLabel}
        </button>
      )}
      {secondary && (
        <button
          type="button"
          onClick={secondary.onClick}
          className={`${TAP} px-1 text-xs font-medium text-foreground underline-offset-2 hover:underline`}
          data-testid="tour-secondary"
        >
          {secondary.label}
        </button>
      )}
      {skip && (
        <button
          type="button"
          onClick={skip.onClick}
          className={`${TAP} rounded-md border border-border px-2.5 py-1.5 text-xs font-medium text-foreground hover:bg-muted`}
          data-testid="tour-skip"
        >
          {skip.label}
        </button>
      )}
      {onBack && (
        <button
          type="button"
          onClick={onBack}
          className={`${TAP} inline-flex items-center gap-0.5 rounded-md border border-border px-2.5 py-1.5 text-xs font-medium text-foreground hover:bg-muted`}
          data-testid="tour-back"
        >
          <ChevronLeft className="h-3.5 w-3.5" aria-hidden="true" />
          Back
        </button>
      )}
      {onNext && (
        <button
          type="button"
          onClick={onNext}
          disabled={nextDisabled}
          title={nextDisabled ? nextDisabledReason : undefined}
          aria-describedby={nextDisabled && nextDisabledReason ? "tour-next-reason" : undefined}
          className={`${TAP} inline-flex items-center gap-1 rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-50`}
          data-testid="tour-next"
          data-done={actionDone || undefined}
        >
          {actionDone && <Check className="h-3.5 w-3.5" aria-label="Done" data-testid="tour-next-done" />}
          {nextLabel}
        </button>
      )}
      {nextDisabled && nextDisabledReason && (
        <span id="tour-next-reason" className="sr-only">
          {nextDisabledReason}
        </span>
      )}
    </div>
  );
}
