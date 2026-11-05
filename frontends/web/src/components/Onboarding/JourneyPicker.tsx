/**
 * The journey picker: "Explore a forge app" or "Use AI workflows".
 *
 * Opened by `?tour=pick` — onboarding's exit puts it there, so every new user
 * chooses (local and cloud alike) — and by `?tour=pick:menu` from the Tours
 * entries. A journey the landing page carried (`?journey=`, held through
 * sign-in by lib/landingAttribution.ts) is PRESELECTED, not skipped: the user
 * still confirms, and can switch.
 */

import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Boxes, Workflow } from "lucide-react";
import { cn } from "../../lib/utils";
import { readLandingJourney } from "../../lib/landingAttribution";
import { availableJourneys, resolveTour } from "./journeys/registry";
import { parseTourParam, type JourneyId, type JourneySource } from "./tourParam";

const ICONS: Record<JourneyId, typeof Boxes> = { forge: Boxes, workflows: Workflow };

interface JourneyPickerProps {
  source: "onboarding" | "menu";
  /** `<journey>:<step>` the user last ended a tour on; offered as "Resume". */
  resumeStep?: string | null;
  /** `atStep` is set when resuming. */
  onStart: (journey: JourneyId, source: JourneySource, atStep?: string) => void;
  onDismiss: () => void;
}

/** Where a choice came from: a menu, the landing page's preselection kept, or the picker itself. */
export function selectionSource(
  pickerSource: "onboarding" | "menu",
  chosen: JourneyId,
  landing: JourneyId | undefined,
): JourneySource {
  if (pickerSource === "menu") return "menu";
  return landing === chosen ? "landing" : "picker";
}

export function JourneyPicker({ source, resumeStep, onStart, onDismiss }: JourneyPickerProps) {
  const journeys = availableJourneys();
  const resume = resolveTour(parseTourParam(resumeStep));
  const [landing] = useState(() => {
    const held = readLandingJourney();
    return journeys.some((j) => j.id === held) ? held : undefined;
  });
  const [selected, setSelected] = useState<JourneyId | undefined>(
    landing ?? (journeys.length === 1 ? journeys[0].id : undefined),
  );
  const startRef = useRef<HTMLButtonElement>(null);
  const firstCardRef = useRef<HTMLButtonElement>(null);

  // Focus the confirm button when a choice is preselected, else the first card.
  useEffect(() => {
    (selected ? startRef.current : firstCardRef.current)?.focus();
    // Mount only.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const start = () => {
    if (selected) onStart(selected, selectionSource(source, selected, landing));
  };

  return createPortal(
    <div
      className="fixed inset-0 z-[100] flex items-center justify-center p-4"
      role="dialog"
      aria-modal="true"
      aria-labelledby="journey-picker-title"
      data-testid="journey-picker"
      onKeyDown={(e) => {
        if (e.key === "Escape") onDismiss();
      }}
    >
      <div className="absolute inset-0 bg-black/70 backdrop-blur-sm" aria-hidden="true" />
      <div className="relative w-full max-w-2xl rounded-xl border border-border bg-popover p-6 text-popover-foreground shadow-2xl sm:p-8">
        <div className="space-y-1.5 text-center">
          <h2 id="journey-picker-title" className="text-2xl font-semibold tracking-tight text-foreground">
            Take a quick tour
          </h2>
          <p className="text-sm text-muted-foreground">
            Pick where to start. You can take the other one any time from the command palette.
          </p>
        </div>

        <div className="mt-6 grid gap-3 sm:grid-cols-2" role="group" aria-label="Tours">
          {journeys.map((journey, i) => {
            const Icon = ICONS[journey.id];
            const isSelected = selected === journey.id;
            return (
              <button
                key={journey.id}
                ref={i === 0 ? firstCardRef : undefined}
                type="button"
                aria-pressed={isSelected}
                data-testid={`journey-card-${journey.id}`}
                onClick={() => setSelected(journey.id)}
                onDoubleClick={() => onStart(journey.id, selectionSource(source, journey.id, landing))}
                className={cn(
                  "flex min-h-[148px] flex-col items-start gap-3 rounded-xl border-2 p-5 text-left transition-all",
                  "focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                  isSelected
                    ? "border-primary bg-primary/10"
                    : "border-border/50 bg-background hover:border-primary/50 hover:bg-muted/40",
                )}
              >
                <span
                  className={cn(
                    "flex h-10 w-10 items-center justify-center rounded-lg",
                    isSelected ? "bg-primary/20 text-primary-ink" : "bg-muted text-foreground",
                  )}
                >
                  <Icon className="h-5 w-5" aria-hidden />
                </span>
                <span className="space-y-1">
                  <span className="block text-base font-semibold text-foreground">{journey.title}</span>
                  <span className="block text-xs leading-relaxed text-muted-foreground">
                    {journey.description}
                  </span>
                </span>
              </button>
            );
          })}
        </div>

        <div className="mt-6 flex items-center justify-end gap-2">
          {resume?.kind === "step" && (
            <button
              type="button"
              data-testid="journey-resume"
              onClick={() => onStart(resume.journey, source === "menu" ? "menu" : "picker", resume.stepId)}
              className="mr-auto px-3 py-1.5 text-sm text-primary-ink transition-colors hover:underline"
            >
              Resume where you left off
            </button>
          )}
          <button
            type="button"
            onClick={onDismiss}
            className="px-3 py-1.5 text-sm text-muted-foreground transition-colors hover:text-foreground"
          >
            Not now
          </button>
          <button
            ref={startRef}
            type="button"
            onClick={start}
            disabled={!selected}
            className="rounded-lg bg-primary px-4 py-1.5 text-sm font-medium text-primary-foreground transition-colors hover:bg-primary/90 disabled:cursor-not-allowed disabled:opacity-50"
          >
            Start tour
          </button>
        </div>
      </div>
    </div>,
    document.body,
  );
}
