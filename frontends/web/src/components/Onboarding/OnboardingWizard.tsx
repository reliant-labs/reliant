/**
 * Onboarding Wizard (Reliant shell)
 *
 * Reliant's onboarding layer: the journeys (JourneyTour, the shell-neutral
 * engine UI) behind Reliant's gates, the version-bump re-offer, and the
 * checklist's state (loaded and kept current here). The checklist itself is
 * drawn by the shell frame (OnboardingChecklist, floating on every shell
 * page; Layout/shell/AppShell.tsx). The forge shell mounts JourneyTour
 * directly instead.
 *
 * The tour is URL-driven: `?tour=` (tourParam.ts) says what is open. The
 * user owns the URL; the wizard reacts. The one effect that navigates is the
 * version-bump re-offer below, which runs at most once per page load.
 *
 * It renders JourneyTour while `?tour=…` is open (the picker, or a journey
 * step), and nothing otherwise.
 */

import { useEffect } from "react";
import { useLocation } from "@tanstack/react-router";
import { isClaimPath } from "../../lib/claimLink";
import { isAcceptInvitePath } from "../../lib/inviteLink";
import { trackEvent } from "../../lib/analytics";
import { useAuthIdentity } from "@/lib/auth/use-auth";
import { useOnboardingChecklistStore } from "../../store/onboardingChecklistStore";
import { useTourStore } from "../../store/tourStore";
import { useSurface } from "../../lib/surfaceContext";
import { isPhoneViewport } from "../../lib/mobileRedirect";

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
  const journeys = useTourStore((state) => state.journeys);
  const loadTourState = useTourStore((state) => state.loadState);

  const {
    isInitialized: checklistInitialized,
    loadState: loadChecklistState,
    detectCompletedItems,
    subscribeToStoreChanges,
  } = useOnboardingChecklistStore();

  const isInitialized = tourInitialized && checklistInitialized;

  const location = useLocation();
  // The dedicated /onboarding route IS the onboarding experience: a re-offered
  // journey must not open over it. /claim owns the screen for the same reason
  // (someone is mid-way through taking over a site); /accept-invite, mid-way
  // through joining an org.
  const isOnboardingRoute =
    location.pathname.startsWith("/onboarding") ||
    isClaimPath(location.pathname) ||
    isAcceptInvitePath(location.pathname);

  // Onboarding is per-USER state, so none of it may render to a signed-out
  // visitor (a forced sign-out drops the user on the sign-in screen without
  // unmounting this wizard).
  const user = useAuthIdentity();

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

  // Detect persisted achievements on load and when a journey finishes in this
  // session, without waiting for the wizard to remount.
  useEffect(() => {
    if (!isInitialized) return;
    void detectCompletedItems();
  }, [isInitialized, journeys, detectCompletedItems]);

  // Keep the other completion signals subscribed while the checklist is ready.
  useEffect(() => {
    if (!checklistInitialized) return;
    const unsub = subscribeToStoreChanges();
    return unsub;
  }, [checklistInitialized, subscribeToStoreChanges]);

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

  // Signed out: no tour belongs on screen.
  if (!user) return null;

  // Not ready yet
  if (!isInitialized) return null;

  return tourOpen ? <JourneyTour /> : null;
}
