/**
 * The workflows journey on a phone (Journey.phone): the builder canvas isn't
 * there, so the same lessons come as a deck of cards, each with a still
 * diagram. Swipe or use Back / Next; Done completes the journey.
 */

import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { ChevronLeft } from "lucide-react";

import type { JourneyPhoneProps } from "../types";
import { LESSONS } from "./workflowsLessons";

const TAP = "min-h-11 px-3 text-sm font-medium";

export function WorkflowsPhoneDeck({ onFinish, onExit }: JourneyPhoneProps) {
  const [index, setIndex] = useState(0);
  const track = useRef<HTMLDivElement | null>(null);
  // Set while Back / Next scrolls the deck: its scroll events mustn't move the count.
  const steering = useRef<ReturnType<typeof setTimeout> | null>(null);
  const last = LESSONS.length - 1;

  // The visible card follows the swipe.
  useEffect(() => {
    const el = track.current;
    if (!el) return;
    const onScroll = () => {
      if (steering.current) return;
      const width = el.clientWidth || 1;
      setIndex(Math.max(0, Math.min(last, Math.round(el.scrollLeft / width))));
    };
    el.addEventListener("scroll", onScroll, { passive: true });
    return () => {
      el.removeEventListener("scroll", onScroll);
      if (steering.current) clearTimeout(steering.current);
    };
  }, [last]);

  const go = (to: number) => {
    const next = Math.max(0, Math.min(last, to));
    setIndex(next);
    const el = track.current;
    if (!el || typeof el.scrollTo !== "function") return;
    if (steering.current) clearTimeout(steering.current);
    steering.current = setTimeout(() => (steering.current = null), 700);
    const reduce = typeof window.matchMedia === "function" && window.matchMedia("(prefers-reduced-motion: reduce)").matches;
    el.scrollTo({ left: next * el.clientWidth, behavior: reduce ? "auto" : "smooth" });
  };

  return createPortal(
    <div className="fixed inset-0 z-[100] flex flex-col justify-end" data-testid="wf-phone-deck">
      <div className="absolute inset-0 bg-black/60" aria-hidden="true" />
      <section
        role="dialog"
        aria-modal="true"
        aria-roledescription="carousel"
        aria-label="Use AI workflows"
        className="relative max-h-[92dvh] rounded-t-2xl border border-border bg-popover pb-[env(safe-area-inset-bottom)] text-popover-foreground shadow-2xl"
      >
        <div
          ref={track}
          className="flex snap-x snap-mandatory overflow-x-auto overscroll-x-contain [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
          data-testid="wf-phone-track"
        >
          {LESSONS.map((lesson, i) => (
            <article
              key={lesson.id}
              aria-roledescription="slide"
              aria-label={`${i + 1} of ${LESSONS.length}: ${lesson.title}`}
              aria-hidden={i !== index}
              className="w-full shrink-0 snap-center px-5 pt-5"
              data-testid={`wf-phone-card-${lesson.id}`}
            >
              <div className="rounded-xl border border-border bg-muted/30 p-3">{lesson.diagram()}</div>
              <h3 className="mt-4 text-lg font-semibold tracking-tight text-foreground">{lesson.title}</h3>
              <p className="mt-2 text-sm leading-relaxed text-muted-foreground">{lesson.phoneBody ?? lesson.body}</p>
              {i === last && (
                <p className="mt-2 text-sm leading-relaxed text-muted-foreground">
                  On a computer, the tour opens Fix my PR on the builder canvas, and you can make your own from there.
                </p>
              )}
            </article>
          ))}
        </div>

        <div className="flex justify-center gap-1.5 pt-4" aria-hidden="true">
          {LESSONS.map((lesson, i) => (
            <span key={lesson.id} className={`h-1.5 rounded-full transition-all ${i === index ? "w-5 bg-primary" : "w-1.5 bg-border"}`} />
          ))}
        </div>

        <div className="mt-2 flex items-center gap-2 border-t border-border px-4 py-3">
          <span className="text-xs tabular-nums text-muted-foreground" data-testid="wf-phone-count" aria-live="polite">
            Step {index + 1} of {LESSONS.length}
          </span>
          <span className="flex-1" />
          {index < last && (
            <button type="button" onClick={onExit} className={`${TAP} text-muted-foreground`} data-testid="wf-phone-exit">
              Exit tour
            </button>
          )}
          {index > 0 && (
            <button
              type="button"
              onClick={() => go(index - 1)}
              className={`${TAP} inline-flex items-center rounded-md border border-border`}
              data-testid="wf-phone-back"
            >
              <ChevronLeft className="h-4 w-4" aria-hidden="true" />
              Back
            </button>
          )}
          <button
            type="button"
            onClick={() => (index === last ? onFinish() : go(index + 1))}
            className={`${TAP} rounded-md bg-primary text-primary-foreground`}
            data-testid="wf-phone-next"
          >
            {index === last ? "Done" : "Next"}
          </button>
        </div>
      </section>
    </div>,
    document.body,
  );
}
