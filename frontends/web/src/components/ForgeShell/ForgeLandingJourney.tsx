// Copyright (c) 2025 Reliant Labs

/**
 * Acts on the journey the landing page carried to the forge host
 * (landingJourney.ts): starts the forge journey, or points at the Workflows
 * area for the workflows one. Mounted once, beside <JourneyTour />, at the
 * root of routes.tsx on the forge brand.
 */

import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useNavigate, useRouterState } from "@tanstack/react-router";

import { FORGE_DEMO_PATH } from "@/components/Forge/Demo/demoPath";
import { isJourneyAvailable } from "@/components/Onboarding/journeys/registry";
import { useTourNavigation } from "@/components/Onboarding/useTourNavigation";
import { trackEvent } from "@/lib/analytics";
import { dropLandingJourney, readLandingJourney } from "@/lib/landingAttribution";
import { workflowLibraryPath } from "@/lib/paths";
import { orgFreeRouteId } from "@/lib/paths/org";
import { useAuthStore } from "@/store/authStore";

import { GUARDED_ROUTE_IDS, landingAuthDecision, landingJourneyAction } from "./landingJourney";

export function ForgeLandingJourney() {
  const nav = useTourNavigation();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const tourOpen = useRouterState({ select: (s) => (s.location.search as Record<string, unknown>).tour !== undefined });
  const guarded = useRouterState({ select: (s) => s.matches.some((m) => GUARDED_ROUTE_IDS.includes(orgFreeRouteId(m.routeId))) });
  const user = useAuthStore((state) => state.user);
  const loading = useAuthStore((state) => state.loading);
  const initialized = useAuthStore((state) => state.initialized);
  const [held, setHeld] = useState(() => readLandingJourney());

  const action = landingJourneyAction({
    held,
    tourOpen,
    forgeAvailable: isJourneyAvailable("forge"),
    onDemo: pathname.startsWith(FORGE_DEMO_PATH),
    guarded,
    auth: landingAuthDecision({ initialized, loading, user }),
  });

  const startJourney = useRef(nav.startJourney);
  startJourney.current = nav.startJourney;
  useEffect(() => {
    if (action === "drop") {
      dropLandingJourney();
      setHeld(undefined);
    } else if (action === "start-forge") {
      // startJourney drops the held choice and opens the first step's page (the demo).
      startJourney.current("forge", "landing");
      setHeld(undefined);
    }
  }, [action]);

  if (action !== "offer-workflows") return null;
  return (
    <WorkflowsOffer
      onClose={() => {
        dropLandingJourney();
        setHeld(undefined);
      }}
    />
  );
}

/** The workflows journey's landing choice: the Workflows area, in this app. */
function WorkflowsOffer({ onClose }: { onClose: () => void }) {
  const navigate = useNavigate();
  const href = workflowLibraryPath();
  return createPortal(
    <section
      aria-labelledby="forge-workflows-offer-title"
      data-testid="forge-workflows-offer"
      className="fixed bottom-6 right-6 z-[90] w-full max-w-sm rounded-xl border border-border bg-popover p-4 text-popover-foreground shadow-2xl"
    >
      <h2 id="forge-workflows-offer-title" className="text-sm font-semibold text-foreground">
        AI workflows
      </h2>
      <p className="mt-1 text-xs text-muted-foreground">
        Workflows run your agents on a machine you connect. See every workflow, and the examples to start from, under Workflows.
      </p>
      <div className="mt-3 flex items-center gap-2">
        <a
          href={href}
          onClick={(event) => {
            trackEvent("tour_link_clicked", { journey: "workflows", target: "workflows" });
            onClose();
            if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0) return;
            event.preventDefault();
            void navigate({ href });
          }}
          className="no-color inline-flex items-center rounded-lg bg-primary px-3 py-1.5 text-sm font-medium text-primary-foreground hover:bg-primary/90"
        >
          Open Workflows
        </a>
        <button type="button" onClick={onClose} className="rounded-lg border border-border px-3 py-1.5 text-sm text-foreground hover:bg-muted">
          Stay here
        </button>
      </div>
    </section>,
    document.body,
  );
}
