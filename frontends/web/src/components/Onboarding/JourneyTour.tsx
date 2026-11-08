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
import { isDemoShell } from "../../lib/shell";
import { isPhoneViewport } from "../../lib/mobileRedirect";
import { useTourStore } from "../../store/tourStore";
import { OnboardingSpotlight } from "./OnboardingSpotlight";
import { OnboardingMultiSpotlight } from "./OnboardingMultiSpotlight";
import { OnboardingNavBar } from "./OnboardingNavBar";
import { JourneyPicker } from "./JourneyPicker";
import { TourActionHint, TourControls } from "./TourControls";
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
import { getTourShell } from "./tourShell";
import type { JourneyStep } from "./types";
import { isOnRoute, useTourNavigation, type TourNavigation } from "./useTourNavigation";

// ─── Page-redirect card ──────────────────────────────────────────────────────
// Rendered when a step's page is not the current page. Keeps the user in
// control of navigation: one button takes them there; the step's controls
// (the nav bar, or the card's own footer) stay available.

function OpenPageCard({
  title,
  message,
  actionLabel,
  onOpen,
  footer,
}: {
  title: string;
  message: string;
  actionLabel: string;
  onOpen: () => void;
  footer?: React.ReactNode;
}) {
  return (
    <TourCard
      title={title}
      description={
        <div className="space-y-4" data-testid="tour-open-page">
          <p>{message}</p>
          <button
            type="button"
            onClick={onOpen}
            className="inline-flex items-center gap-2 px-4 py-2 text-sm font-medium bg-primary text-primary-foreground rounded-lg hover:bg-primary/90 transition-colors"
          >
            {actionLabel}
          </button>
        </div>
      }
      footer={footer}
    />
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
  footer,
  reserveBar = true,
}: {
  title: string;
  description: React.ReactNode;
  wide?: boolean;
  modal?: boolean;
  /** The step's own controls (Journey.controls "callout"). */
  footer?: React.ReactNode;
  /** Keep room below a modal card for the bottom nav bar. False when there is none ("callout"). */
  reserveBar?: boolean;
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
      className={`fixed inset-0 z-[100] flex items-center justify-center p-4 pointer-events-none ${modal ? (reserveBar ? "pt-6 pb-52" : "py-6") : ""}`}
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
        {footer}
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

  // The card's own controls (the default); "bar" keeps the retired bottom nav bar.
  const compact = getJourney(journey).controls !== "bar";
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

  // The card's own controls: Step X of Y, Back, Next (disabled until a required action is
  // done, then checked), and Exit. An auto-advancing step moves on by itself once done.
  const footer = compact ? (
    <TourControls
      stepNumber={stepNumber}
      totalSteps={total}
      onExit={isLast ? undefined : () => void nav.skipAll()}
      onBack={onBack}
      onNext={onNext}
      nextDisabled={nextHidden}
      nextDisabledReason={step.actionHint}
      actionDone={hasAction && satisfied}
      nextLabel={isLast ? "Finish" : "Next"}
      secondary={secondaryAction}
      skip={step.skip?.label && !isLast && !(hasAction && satisfied) ? { label: step.skip.label, onClick: () => void nav.skipStep() } : undefined}
    />
  ) : undefined;
  const hint = compact && hasAction && !satisfied ? <TourActionHint hint={step.actionHint} done={false} /> : undefined;
  const callout = compact ? { footer, hint } : undefined;

  let body: React.ReactNode;
  if (step.route && !isOnRoute(step.route, pathname)) {
    body = (
      <OpenPageCard
        title={shown.title}
        message={step.route.message ?? `${step.route.label} to continue this step.`}
        actionLabel={step.route.label}
        onOpen={() => nav.openStepPage(journey, step.route!)}
        footer={compact ? <TourControls stepNumber={stepNumber} totalSteps={total} onExit={() => void nav.skipAll()} /> : undefined}
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
            callout={callout}
          />
        );
        break;
      case "multi-spotlight":
        body = (
          <>
            <OnboardingMultiSpotlight
              targets={shown.targets ?? []}
              stepNumber={stepNumber}
              totalSteps={total}
              onNext={nextHidden ? noop : onNext}
              onBack={onBack}
              onSkipAll={() => void nav.skipAll()}
              keyboardNav={!hasAction}
            />
            {/* The labels name each target; with no bar, the step's copy and controls need a card of their own. */}
            {compact && <TourCard title={shown.title} description={<>{description}{hint}</>} footer={footer} reserveBar={false} />}
          </>
        );
        break;
      case "card":
        body = (
          <TourCard
            title={shown.title}
            description={<>{description}{hint}</>}
            wide={step.card?.wide}
            modal={step.card?.modal}
            footer={footer}
            reserveBar={!compact}
          />
        );
        break;
    }
  }

  return (
    <>
      {body}
      {!compact && (
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
      )}
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

  // Phones: Reliant's mobile surface has none of the desktop chrome a journey points at, so it
  // defers there. The forge shell and the demo draw each step as a bottom sheet instead.
  // A journey with its own phone presentation (Journey.phone) shows that instead.
  if (isPhoneViewport() && getTourShell() === "reliant") {
    const Phone = nav.tour.kind === "step" ? getJourney(nav.tour.journey).phone : undefined;
    if (Phone) return <Phone onFinish={() => void nav.finish()} onExit={() => void nav.skipAll()} />;
    return <DeferredOnPhone onClose={nav.exitTour} />;
  }

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
  // The public demo explains the actual page, including legacy ?view= deep links.
  // It never demands a prior action or sends exploration through the old single-page sequence.
  if (journey === "forge" && isDemoShell()) return null; // DemoTips owns the page-local entry callout, including legacy links.
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
