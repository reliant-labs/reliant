/**
 * Tour Navigation Hook
 *
 * Single source of truth for "which journey and step is the tour on" and "how
 * do I move between steps". The active step lives in the URL as
 * `?tour=<journey>:<step>` (grammar: tourParam.ts); this hook reads/writes
 * that param via tanstack-router. Every transition is the direct result of a
 * user action (a click, or a step's `advanceOn`) — no effect here pushes a
 * route.
 *
 * `?tour=` survives navigations that don't mention it (the root route
 * retains it, routes.tsx), so a journey can span pages the user moves
 * between; every exit below clears it explicitly with `tour: undefined`.
 *
 * Journey analytics fire here, because only here is it known how a step was
 * completed (journeyAnalytics.ts). `useTourStore` is the persistence layer.
 *
 * Shell-neutral: nothing here needs Reliant's daemon or chat unless a
 * journey ends without its own `finish` target in the Reliant shell.
 */

import { useCallback } from "react";
import { useNavigate, useRouter, useSearch } from "@tanstack/react-router";
import { useTourStore } from "../../store/tourStore";
import { useChatStore } from "../../store/chatStore";
import { useProjectStore } from "../../store/projectStore";
import { dropLandingJourney } from "../../lib/landingAttribution";
import { trackEvent } from "../../lib/analytics";
import { chatRouteAfterTour } from "../OnboardingFlow/useOnboardingComplete";
import {
  activeJourneySteps,
  firstStepId,
  getJourney,
  getJourneyStep,
  nextStepId,
  previousStepId,
  resolveTour,
  type ResolvedTour,
} from "./journeys/registry";
import {
  formatTourParam,
  parseTourParam,
  pickerParam,
  type JourneyId,
  type JourneySource,
} from "./tourParam";
import { getTourShell } from "./tourShell";
import {
  trackJourneyCompleted,
  trackJourneySelected,
  trackJourneySkipped,
  trackJourneyStepCompleted,
} from "./journeyAnalytics";
import type { JourneyRoute, JourneyStep, RouteTarget, StepCompletionVia } from "./types";

async function promptApiKeyIfNeededAfterTour(): Promise<void> {
  if (getTourShell() !== "reliant") return;
  const { useApiKeySetupStore } = await import("../../store/apiKeySetupStore");
  if (useApiKeySetupStore.getState().hasApiKey === true) return;
  useApiKeySetupStore.setState({ hasChecked: false });
  await useApiKeySetupStore.getState().ensureApiKeyOrShowModal();
}

/** Whether `pathname` is on a step's page. */
export function isOnRoute(route: JourneyRoute | undefined, pathname: string): boolean {
  return !route || pathname.startsWith(route.path);
}

/** Where a route sends the user, given the journey's context; null when it can't be built yet. */
export function routeTarget(route: JourneyRoute, journey: JourneyId): RouteTarget | null {
  if (!route.open) return { to: route.path };
  if (typeof route.open !== "function") return route.open;
  try {
    return route.open(useTourStore.getState().contexts?.[journey] ?? {});
  } catch {
    return null;
  }
}

function runHook(hook: (() => void) | undefined): void {
  try {
    hook?.();
  } catch {
    // A content hook must never wedge the tour.
  }
}

type Search = Record<string, unknown>;

export interface StepRef {
  journey: JourneyId;
  stepId: string;
}

// ─── Hook ───────────────────────────────────────────────────────────────────

export interface TourNavigation {
  /** What `?tour=` resolves to; null when no tour is open. */
  tour: ResolvedTour | null;
  /** A journey step is open. */
  isWizardActive: boolean;
  isPickerOpen: boolean;
  currentJourney: JourneyId | null;
  currentStepId: string | null;
  /** Open a step, going to its page (route) first when it has one. */
  goToStep: (journey: JourneyId, stepId: string) => void;
  /** Go to the open step's page (the "Open <page>" card's button). */
  openStepPage: (journey: JourneyId, route: JourneyRoute) => void;
  /** Start a journey at its first step (or `atStep`) and record the choice. */
  startJourney: (
    journey: JourneyId,
    source: JourneySource,
    extra?: Record<string, string>,
    atStep?: string,
  ) => void;
  /** Open the journey picker. */
  openPicker: (source?: "menu") => void;
  /** Close the picker without choosing. */
  dismissPicker: () => Promise<void>;
  /** Strip `?tour=`, staying on the page. */
  exitTour: () => void;
  /**
   * Complete the step and move on. `from` pins the step being completed: an
   * `advanceOn` that fires after the user already moved is ignored rather
   * than skipping the step they are now on. On the last step, "action" only
   * completes it; the user still clicks Finish.
   */
  advance: (via: Exclude<StepCompletionVia, "skip">, from?: StepRef) => Promise<void>;
  /** "Skip step": the step's own `skip.run` first (if any), then move on. */
  skipStep: () => Promise<void>;
  goBack: () => void;
  /** "End tour": record it skipped and where it ended (for Resume). */
  skipAll: () => Promise<void>;
  /** Finish the journey; with `then`, start that journey instead of landing. */
  finish: (then?: JourneyId) => Promise<void>;
}

export function useTourNavigation(): TourNavigation {
  const navigate = useNavigate();
  const router = useRouter();
  // Reading via { strict: false } so the hook works at the root (which has no
  // search schema). The wizard is mounted globally — we can't constrain `from`.
  const search = useSearch({ strict: false }) as { tour?: unknown };
  const tour = resolveTour(parseTourParam(search.tour));
  const step = tour?.kind === "step" ? tour : null;
  const currentJourney = step?.journey ?? null;
  const currentStepId = step?.stepId ?? null;

  // With `to` omitted the router can't resolve which route's search schema
  // applies, so it types the reducer's return as `never`; the runtime
  // contract (merge into the current search) is route-agnostic.
  const setTourParam = useCallback(
    (value: string | undefined) => {
      void navigate({
        search: ((prev: Search) => ({ ...prev, tour: value })) as never,
      });
    },
    [navigate],
  );

  const navigateTo = useCallback(
    (target: RouteTarget, tourValue: string | undefined) => {
      const path = target.to.replace(/\$([A-Za-z0-9_]+)/g, (_, name: string) =>
        encodeURIComponent(target.params?.[name] ?? ""),
      );
      const here = router.history.location.pathname === path;
      void navigate({
        to: target.to,
        params: target.params,
        // Already on the page: merge, so params the page keeps (a project,
        // an open trace) survive. Elsewhere: the target's own search.
        search: ((prev: Search) =>
          here ? { ...prev, ...target.search, tour: tourValue } : { ...target.search, tour: tourValue }) as never,
      } as never);
    },
    [navigate, router],
  );

  const goToStep = useCallback(
    (journey: JourneyId, stepId: string) => {
      const tourValue = formatTourParam(journey, stepId);
      const route = getJourneyStep(journey, stepId)?.route;
      const onPage = isOnRoute(route, router.history.location.pathname);
      // A step whose route names an explicit target goes there (its search
      // too, e.g. `view=traces`); a bare path only when we're off it.
      const target = route && (route.open || !onPage) ? routeTarget(route, journey) : null;
      if (target) navigateTo(target, tourValue);
      else setTourParam(tourValue);
    },
    [router, navigateTo, setTourParam],
  );

  const openStepPage = useCallback(
    (journey: JourneyId, route: JourneyRoute) => {
      const target = routeTarget(route, journey);
      if (target) navigateTo(target, (router.latestLocation.search as Search).tour as string | undefined);
    },
    [navigateTo, router],
  );

  // The step the URL is on right now — read fresh from the router, not from
  // this render, because advanceOn fires from listeners that outlive it.
  const liveStep = useCallback((): (StepRef & { step: JourneyStep }) | null => {
    const live = resolveTour(parseTourParam((router.latestLocation.search as Search).tour));
    return live?.kind === "step" ? { journey: live.journey, stepId: live.stepId, step: live.step } : null;
  }, [router]);

  /**
   * Leave the tour at the end of a journey. A journey's `finish` target for
   * this shell wins; Reliant's default is the chat, in ONE navigation (the
   * destination clears `tour`, so the tour never vanishes while the user
   * still sits on the last step's page); elsewhere, strip `?tour=`.
   */
  const land = useCallback(
    async (journey: JourneyId) => {
      const shell = getTourShell();
      const target = getJourney(journey).finish?.(shell);
      if (target) {
        await navigate({ to: target.to, params: target.params, search: { ...target.search, tour: undefined } } as never);
      } else if (shell === "reliant") {
        useChatStore.getState().clearCurrentChat();
        const projectId = useProjectStore.getState().currentProject?.id;
        await navigate(chatRouteAfterTour(projectId) as never);
      } else {
        setTourParam(undefined);
      }
      void promptApiKeyIfNeededAfterTour();
    },
    [navigate, setTourParam],
  );

  const exitTour = useCallback(() => setTourParam(undefined), [setTourParam]);

  const startJourney = useCallback(
    (journey: JourneyId, source: JourneySource, extra?: Record<string, string>, atStep?: string) => {
      const first = atStep && getJourneyStep(journey, atStep) ? atStep : firstStepId(journey);
      if (!first) return;
      trackJourneySelected(journey, source, extra);
      dropLandingJourney();
      const store = useTourStore.getState();
      void store.setLastJourney(journey);
      void store.setResumeStep(null);
      goToStep(journey, first);
    },
    [goToStep],
  );

  const openPicker = useCallback(
    (source?: "menu") => setTourParam(pickerParam(source)),
    [setTourParam],
  );

  const dismissPicker = useCallback(async () => {
    const fromOnboarding = tour?.kind === "picker" && tour.source === "onboarding";
    trackJourneySkipped("none", "picker");
    dropLandingJourney();
    exitTour();
    await useTourStore.getState().markTourCompleted();
    if (fromOnboarding) void promptApiKeyIfNeededAfterTour();
  }, [tour, exitTour]);

  const finish = useCallback(
    async (then?: JourneyId) => {
      const live = liveStep();
      if (!live) return;
      const { journey, stepId, step: current } = live;
      runHook(current.onComplete);
      trackJourneyStepCompleted(journey, stepId, "next");
      trackJourneyCompleted(journey);
      // The funnel marker the stall alert counts on; `journey` splits it.
      trackEvent("onboarding_completed", {
        journey,
        totalSteps: activeJourneySteps(journey).length,
      });
      const store = useTourStore.getState();
      await store.completeStep(formatTourParam(journey, stepId));
      void store.setResumeStep(null);
      if (then) {
        void store.markTourCompleted(journey, "completed");
        startJourney(then, "picker", { from_journey: journey });
        return;
      }
      await store.markTourCompleted(journey, "completed");
      await land(journey);
    },
    [liveStep, startJourney, land],
  );

  const advance = useCallback(
    async (via: Exclude<StepCompletionVia, "skip">, from?: StepRef) => {
      const live = liveStep();
      if (!live) return;
      if (from && (from.journey !== live.journey || from.stepId !== live.stepId)) return;
      const next = nextStepId(live.journey, live.stepId);
      if (!next) {
        if (via === "next") await finish();
        return;
      }
      runHook(live.step.onComplete);
      trackJourneyStepCompleted(live.journey, live.stepId, via, live.step.signal);
      await useTourStore.getState().completeStep(formatTourParam(live.journey, live.stepId));
      goToStep(live.journey, next);
    },
    [liveStep, finish, goToStep],
  );

  const skipStep = useCallback(async () => {
    const live = liveStep();
    if (!live) return;
    const { journey, stepId, step: current } = live;
    if (current.skip?.run) {
      const store = useTourStore.getState();
      try {
        await current.skip.run({
          context: store.contexts?.[journey] ?? {},
          setContext: (patch) => useTourStore.getState().setJourneyContext(journey, patch),
        });
      } catch {
        // The step reports its own failure (a toast) and the user stays put.
        return;
      }
    }
    const next = nextStepId(journey, stepId);
    if (!next) {
      await finish();
      return;
    }
    runHook(current.onComplete);
    trackJourneyStepCompleted(journey, stepId, "skip");
    await useTourStore.getState().skipStep(formatTourParam(journey, stepId));
    goToStep(journey, next);
  }, [liveStep, finish, goToStep]);

  const goBack = useCallback(() => {
    const live = liveStep();
    if (!live) return;
    const prev = previousStepId(live.journey, live.stepId);
    if (prev) goToStep(live.journey, prev);
    else exitTour();
  }, [liveStep, goToStep, exitTour]);

  const skipAll = useCallback(async () => {
    const live = liveStep();
    if (!live) return;
    trackJourneySkipped(live.journey, live.stepId);
    const store = useTourStore.getState();
    void store.setResumeStep(formatTourParam(live.journey, live.stepId));
    // Mark every remaining step skipped in memory, then persist ONCE via
    // markTourCompleted: a save per step made "Skip tour" take ~10s.
    store.markRemainingSkipped(
      activeJourneySteps(live.journey).map((s) => formatTourParam(live.journey, s.id)),
    );
    await store.markTourCompleted(live.journey, "skipped");
    await land(live.journey);
  }, [liveStep, land]);

  return {
    tour,
    isWizardActive: step !== null,
    isPickerOpen: tour?.kind === "picker",
    currentJourney,
    currentStepId,
    goToStep,
    openStepPage,
    startJourney,
    openPicker,
    dismissPicker,
    exitTour,
    advance,
    skipStep,
    goBack,
    skipAll,
    finish,
  };
}
