/**
 * Onboarding Wizard (Reliant shell)
 *
 * Reliant's onboarding layer: the journeys (JourneyTour, the shell-neutral
 * engine UI) behind Reliant's gates, the version-bump re-offer, and after
 * them the checklist. The forge shell mounts JourneyTour directly instead.
 *
 * The tour is URL-driven: `?tour=` (tourParam.ts) says what is open. The
 * user owns the URL; the wizard reacts. The one effect that navigates is the
 * version-bump re-offer below, which runs at most once per page load.
 *
 * Phases:
 * 1. `?tour=…` open: JourneyTour (the picker, or a journey step).
 * 2. No tour, checklist not dismissed: the floating OnboardingChecklist.
 * 3. Otherwise nothing.
 */

import { useEffect } from "react";
import { useLocation } from "@tanstack/react-router";
import { isClaimPath } from "../../lib/claimLink";
import { isAcceptInvitePath } from "../../lib/inviteLink";
import { trackEvent } from "../../lib/analytics";
import { useAuthStore } from "../../store/authStore";
import { useOnboardingChecklistStore } from "../../store/onboardingChecklistStore";
import { useTourStore } from "../../store/tourStore";
import { useSurface } from "../../lib/surfaceContext";
import { isPhoneViewport } from "../../lib/mobileRedirect";
import { isWorkflowSurfacePath } from "../../lib/workflowsArea";

import { OnboardingChecklist } from "./OnboardingChecklist";
import { JourneyTour } from "./JourneyTour";
import { JOURNEYS, firstStepId, isJourneyAvailable } from "./journeys/registry";
import "./journeys/catalog";
import { journeyToReshow } from "./journeys/reshow";
import { useTourNavigation } from "./useTourNavigation";

// ─── Version-bump re-offer ───────────────────────────────────────────────────
// Once per page load at most: a journey whose version moved past one the user
// completed is opened again, and "shown" is recorded at the new version
// first, so it is never offered twice (never loops), even if they close the
// tab mid-tour.
let reshowChecked = false;

/** Test-only. */
export function __resetJourneyReshow(): void {
  reshowChecked = false;
}

export function OnboardingWizard() {
  const surface = useSurface();
  const nav = useTourNavigation();

  const tourInitialized = useTourStore((state) => state.isInitialized);
  const loadTourState = useTourStore((state) => state.loadState);

  const {
    isInitialized: checklistInitialized,
    loadState: loadChecklistState,
    panelState,
    allRequiredComplete,
    detectCompletedItems,
    subscribeToStoreChanges,
  } = useOnboardingChecklistStore();

  const isInitialized = tourInitialized && checklistInitialized;

  const location = useLocation();
  const isSettingsMode = location.pathname.startsWith("/settings");
  const isWorkflowMode = isWorkflowSurfacePath(location.pathname);
  // The dedicated /onboarding route IS the onboarding experience — showing
  // the post-onboarding "Setup Guide" floating panel on top of it is a
  // bad-UX dupe of the same information. /claim owns the screen for the same
  // reason: someone is mid-way through taking over a site; /accept-invite,
  // mid-way through joining an org.
  const isOnboardingRoute =
    location.pathname.startsWith("/onboarding") ||
    isClaimPath(location.pathname) ||
    isAcceptInvitePath(location.pathname);

  // Onboarding is per-USER state, so none of it may render to a signed-out
  // visitor (a forced sign-out drops the user on the sign-in screen without
  // unmounting this wizard).
  const user = useAuthStore((state) => state.user);

  // The tour spotlights desktop chrome by DOM selector; the mobile surface
  // has none of it (mobile users still get the setup flow at /onboarding).
  // A phone on a desktop-surface route gets JourneyTour's "open this on a
  // computer" card instead.
  const isDesktopSurface = surface === "desktop";

  // Load state on mount. Pure data fetch — no navigation.
  useEffect(() => {
    if (!tourInitialized) {
      void loadTourState();
    }
    if (!checklistInitialized) {
      void loadChecklistState();
    }
  }, [tourInitialized, checklistInitialized, loadTourState, loadChecklistState]);

  // Set up checklist detection after checklist state is ready
  useEffect(() => {
    if (!checklistInitialized) return;
    void detectCompletedItems();
    const unsub = subscribeToStoreChanges();
    return unsub;
  }, [checklistInitialized, detectCompletedItems, subscribeToStoreChanges]);

  const tourOpen = nav.tour != null;
  const { goToStep } = nav;
  useEffect(() => {
    if (reshowChecked || !tourInitialized || !user || !isDesktopSurface || isPhoneViewport()) return;
    if (isOnboardingRoute || tourOpen) return;
    reshowChecked = true;
    const journey = journeyToReshow(
      useTourStore.getState(),
      (id) => (JOURNEYS[id].reshowAfterVersionBump ?? true) && isJourneyAvailable(id),
    );
    const first = journey ? firstStepId(journey) : null;
    if (!journey || !first) return;
    void useTourStore.getState().markJourneyShown(journey);
    trackEvent("journey_offered", { journey, version: JOURNEYS[journey].version, reason: "version_bump" });
    goToStep(journey, first);
  }, [tourInitialized, user, isDesktopSurface, isOnboardingRoute, tourOpen, goToStep]);

  // The mobile surface shows a tour only as its journey's own phone presentation (Journey.phone).
  // It needs the tour state alone: the checklist never shows there, so its
  // store loading (or failing to) must not hide an open phone tour.
  if (!isDesktopSurface) {
    const phoneTour = nav.tour?.kind === "step" && !!JOURNEYS[nav.tour.journey].phone;
    return phoneTour && user && tourInitialized ? <JourneyTour /> : null;
  }

  // Signed out — neither the tour nor the checklist belongs on screen.
  if (!user) return null;

  // Not ready yet
  if (!isInitialized) return null;

  // Phase 1: a tour is open
  if (tourOpen) return <JourneyTour />;

  // The floating checklist would sit over a phone-sized page.
  if (isPhoneViewport()) return null;

  // Phase 2: Show checklist after setup if not dismissed (only on main chat page).
  // allRequiredComplete gate: a guide with nothing left to do is clutter, so it
  // retires itself rather than sitting at full progress forever.
  if (
    panelState !== "dismissed" &&
    !allRequiredComplete() &&
    !isWorkflowMode &&
    !isSettingsMode &&
    !isOnboardingRoute
  ) {
    return <OnboardingChecklist />;
  }

  // Phase 3: Everything done
  return null;
}
