/**
 * JourneyTour: the shell-neutral journey engine UI — the picker and the open
 * step — driven by `?tour=`.
 *
 * It depends on nothing Reliant-shell-only: no signed-in user, daemon,
 * checklist or onboarding route. Reliant mounts it inside OnboardingWizard
 * (which adds those gates and the checklist); the forge shell mounts it
 * directly, after `setTourShell("forge")` / `setTourStorage(...)`
 * (tourShell.ts). Journey CONTENT is data (journeys/<id>.tsx, JOURNEYS.md).
 */

import { useEffect, useRef } from "react";
import { createPortal } from "react-dom";
import { useLocation } from "@tanstack/react-router";
import { trackEvent } from "../../lib/analytics";
import { isPhoneViewport } from "../../lib/mobileRedirect";
import { useTourStore } from "../../store/tourStore";
import { OnboardingSpotlight } from "./OnboardingSpotlight";
import { OnboardingMultiSpotlight } from "./OnboardingMultiSpotlight";
import { OnboardingModal } from "./OnboardingModal";
import { OnboardingNavBar } from "./OnboardingNavBar";
import { JourneyPicker } from "./JourneyPicker";
import { conditionsOf, useAdvanceOn, useStepPhase } from "./advanceOn";
import { trackJourneyStepViewed } from "./journeyAnalytics";
import {
  firstStepId,
  getJourney,
  isJourneyAvailable,
  nextStepId,
  presentStep,
  previousStepId,
  stepPosition,
  stepType,
} from "./journeys/registry";
import { formatTourParam, otherJourney, type JourneyId } from "./tourParam";
import type { JourneyStep } from "./types";
import { isOnRoute, useTourNavigation, type TourNavigation } from "./useTourNavigation";

// ─── Page-redirect card ──────────────────────────────────────────────────────
// Rendered when a step's page is not the current page. Keeps the user in
// control of navigation — one button takes them there; Skip/Back stay on the
// nav bar.

function OpenPageModal({
  title,
  message,
  actionLabel,
  onOpen,
  stepNumber,
  totalSteps,
}: {
  title: string;
  message: string;
  actionLabel: string;
  onOpen: () => void;
  stepNumber: number;
  totalSteps: number;
}) {
  return (
    <OnboardingModal
      isOpen={true}
      title={title}
      stepNumber={stepNumber}
      totalSteps={totalSteps}
      hideNavigation
      hideProgressBar
    >
      <div className="space-y-5">
        <p className="text-sm text-muted-foreground text-center">{message}</p>
        <div className="flex justify-center">
          <button
            type="button"
            onClick={onOpen}
            className="inline-flex items-center gap-2 px-5 py-2.5 text-sm font-medium bg-primary text-primary-foreground rounded-lg hover:bg-primary/90 transition-colors"
          >
            {actionLabel}
          </button>
        </div>
      </div>
    </OnboardingModal>
  );
}

/**
 * A step with no target: a card, centred above the nav bar. Non-modal by
 * default; `modal` (JourneyStep.card) locks the page's scroll and stops clicks
 * on the backdrop, and `wide` gives it room. Either way, no spotlight cutout.
 */
export function TourCard({
  title,
  description,
  wide = false,
  modal = false,
}: {
  title: string;
  description: React.ReactNode;
  wide?: boolean;
  modal?: boolean;
}) {
  useEffect(() => {
    if (!modal) return;
    const els = [document.documentElement, document.body];
    const before = els.map((el) => el.style.overflow);
    for (const el of els) el.style.overflow = "hidden";
    return () => els.forEach((el, i) => (el.style.overflow = before[i]));
  }, [modal]);
  return createPortal(
    <div
      className={`fixed inset-0 z-[100] flex items-center justify-center p-4 pointer-events-none ${modal ? "pt-6 pb-52" : ""}`}
      data-testid="tour-card-layer"
    >
      {/* Modal: room below for the nav bar (z-[101], bottom-24). The backdrop takes the clicks and the wheel, so the page behind stays put. */}
      <div className={`absolute inset-0 bg-black/60 ${modal ? "pointer-events-auto" : ""}`} aria-hidden="true" data-testid="tour-card-backdrop" />
      <section
        role={modal ? "dialog" : "status"}
        aria-modal={modal || undefined}
        aria-live={modal ? undefined : "polite"}
        aria-labelledby="tour-card-title"
        data-testid="tour-card"
        className={`relative w-full rounded-xl border border-border bg-popover p-6 text-popover-foreground shadow-2xl pointer-events-auto ${
          wide ? "max-w-4xl max-h-full overflow-y-auto overscroll-contain" : "max-w-md"
        }`}
      >
        <h3 id="tour-card-title" className="mb-2.5 text-lg font-semibold tracking-tight text-foreground">
          {title}
        </h3>
        <div className="text-sm leading-relaxed text-muted-foreground">{description}</div>
      </section>
    </div>,
    document.body,
  );
}

/** Below desktop width the spotlights have nothing to point at. */
function DeferredOnPhone({ onClose }: { onClose: () => void }) {
  const tracked = useRef(false);
  useEffect(() => {
    if (tracked.current) return;
    tracked.current = true;
    trackEvent("tour_deferred_mobile");
  }, []);
  return createPortal(
    <div className="fixed inset-x-4 bottom-6 z-[100] rounded-xl border border-border bg-popover p-4 text-popover-foreground shadow-2xl" role="status">
      <p className="text-sm text-foreground">Open this on a computer</p>
      <p className="mt-1 text-xs text-muted-foreground">The tour points at parts of the screen a phone doesn't have room for.</p>
      <button type="button" onClick={onClose} className="mt-3 rounded-lg border border-border px-3 py-1.5 text-sm">
        OK
      </button>
    </div>,
    document.body,
  );
}

// ─── One journey step ────────────────────────────────────────────────────────

function JourneyStepView({
  journey,
  stepId,
  step,
  nav,
}: {
  journey: JourneyId;
  stepId: string;
  step: JourneyStep;
  nav: TourNavigation;
}) {
  const { pathname } = useLocation();
  const stepKey = formatTourParam(journey, stepId);

  useEffect(() => {
    trackJourneyStepViewed(journey, stepId);
    try {
      step.onEnter?.();
    } catch {
      // A content hook must never wedge the tour.
    }
    // Once per step: the parent keys this component by step.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const { pending, satisfied } = useAdvanceOn(journey, step, stepKey, () => {
    void nav.advance("action", { journey, stepId });
  });
  const phase = useStepPhase(journey, step, stepKey);
  const shown = presentStep(step, phase);
  const description =
    satisfied && step.afterAction ? (
      <div className="space-y-2">
        <div>{shown.description}</div>
        <div>{step.afterAction}</div>
      </div>
    ) : (
      shown.description
    );

  const { index, total } = stepPosition(journey, stepId);
  const stepNumber = index + 1;
  const isLast = nextStepId(journey, stepId) === null;
  const onBack = previousStepId(journey, stepId) ? nav.goBack : undefined;
  const hasAction = conditionsOf(step).length > 0;
  const onNext = () => {
    if (isLast) void nav.finish();
    // Next after the action was done still counts as the action completing it.
    else void nav.advance(hasAction && satisfied ? "action" : "next");
  };
  const nextHidden = pending && !step.showNext;
  const noop = () => undefined;

  // The end card's second button: the other journey here, or a link to where it runs.
  const other = otherJourney(journey);
  const elsewhere = getJourney(other).elsewhere;
  const secondaryAction = !isLast
    ? undefined
    : isJourneyAvailable(other) && firstStepId(other) !== null
      ? { label: "Try the other journey", onClick: () => void nav.finish(other) }
      : elsewhere
        ? { label: elsewhere.label, onClick: () => window.open(elsewhere.href(), "_blank", "noopener") }
        : undefined;

  let body: React.ReactNode;
  if (step.route && !isOnRoute(step.route, pathname)) {
    body = (
      <OpenPageModal
        title={shown.title}
        message={step.route.message ?? `${step.route.label} to continue this step.`}
        actionLabel={step.route.label}
        onOpen={() => nav.openStepPage(journey, step.route!)}
        stepNumber={stepNumber}
        totalSteps={total}
      />
    );
  } else {
    switch (stepType(shown)) {
      case "spotlight":
        body = (
          <OnboardingSpotlight
            targetSelector={shown.target!}
            title={shown.title}
            description={description}
            stepNumber={stepNumber}
            totalSteps={total}
            onNext={nextHidden ? noop : onNext}
            onBack={onBack}
            onSkipAll={() => void nav.skipAll()}
            tooltipPosition={shown.tooltipPosition}
            tooltipPadding={step.tooltipPadding}
            spotlightConfig={step.spotlightConfig}
            keyboardNav={!hasAction}
            blockOutside={pending && step.blockOutside === true}
          />
        );
        break;
      case "multi-spotlight":
        body = (
          <OnboardingMultiSpotlight
            targets={shown.targets ?? []}
            stepNumber={stepNumber}
            totalSteps={total}
            onNext={nextHidden ? noop : onNext}
            onBack={onBack}
            onSkipAll={() => void nav.skipAll()}
            keyboardNav={!hasAction}
          />
        );
        break;
      case "card":
        body = <TourCard title={shown.title} description={description} wide={step.card?.wide} modal={step.card?.modal} />;
        break;
    }
  }

  return (
    <>
      {body}
      <OnboardingNavBar
        stepNumber={stepNumber}
        totalSteps={total}
        stepTitle={shown.title}
        onNext={onNext}
        onBack={onBack}
        onSkipAll={() => void nav.skipAll()}
        nextLabel={isLast ? "Finish" : "Next"}
        showSkipAll={!isLast}
        nextHidden={nextHidden}
        actionHint={step.actionHint}
        actionDone={hasAction && satisfied}
        onSkipStep={isLast || (hasAction && satisfied) ? undefined : () => void nav.skipStep()}
        skipStepLabel={step.skip?.label}
        secondaryAction={secondaryAction}
      />
    </>
  );
}

// ─── The engine UI ───────────────────────────────────────────────────────────

export function JourneyTour() {
  const nav = useTourNavigation();
  const initialized = useTourStore((state) => state.isInitialized);
  const resumeStep = useTourStore((state) => state.resumeStep);

  // Progress, versions and context (a slug a step's route needs) load first.
  // Reliant's OnboardingWizard loads it too; loadState de-duplicates.
  useEffect(() => {
    if (!initialized) void useTourStore.getState().loadState();
  }, [initialized]);

  if (!nav.tour || !initialized) return null;

  if (isPhoneViewport()) return <DeferredOnPhone onClose={nav.exitTour} />;

  if (nav.tour.kind === "picker") {
    return (
      <JourneyPicker
        source={nav.tour.source}
        resumeStep={resumeStep}
        onStart={(journey, source, atStep) =>
          nav.startJourney(journey, source, atStep ? { resumed: "true" } : undefined, atStep)
        }
        onDismiss={() => void nav.dismissPicker()}
      />
    );
  }

  const { journey, stepId, step } = nav.tour;
  return (
    <JourneyStepView
      key={formatTourParam(journey, stepId)}
      journey={journey}
      stepId={stepId}
      step={step}
      nav={nav}
    />
  );
}
