// Copyright (c) 2025 Reliant Labs

/**
 * Acts on the journey the landing page carried to the forge shell
 * (landingJourney.ts): starts the forge journey, or offers the workflows
 * journey where it runs, in Reliant. Mounted once, beside <JourneyTour />.
 */

import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useRouterState } from "@tanstack/react-router";

import { FORGE_DEMO_PATH } from "@/components/Forge/Demo/demoPath";
import { getJourney, isJourneyAvailable } from "@/components/Onboarding/journeys/registry";
import { useTourNavigation } from "@/components/Onboarding/useTourNavigation";
import { trackEvent } from "@/lib/analytics";
import { dropLandingJourney, readLandingJourney } from "@/lib/landingAttribution";
import { useAuthStore } from "@/store/authStore";

import { forgeAuthDecision } from "./ForgeAuthGuard";
import { GUARDED_ROUTE_ID, landingJourneyAction } from "./landingJourney";

export function ForgeLandingJourney() {
  const nav = useTourNavigation();
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const tourOpen = useRouterState({ select: (s) => (s.location.search as Record<string, unknown>).tour !== undefined });
  const guarded = useRouterState({ select: (s) => s.matches.some((m) => m.routeId === GUARDED_ROUTE_ID) });
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
    auth: forgeAuthDecision({ initialized, loading, user }),
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

  if (action !== "offer-reliant") return null;
  return (
    <WorkflowsInReliant
      onClose={() => {
        dropLandingJourney();
        setHeld(undefined);
      }}
    />
  );
}

/** The workflows journey, offered where it runs. */
function WorkflowsInReliant({ onClose }: { onClose: () => void }) {
  const href = getJourney("workflows").elsewhere?.href();
  if (!href) return null;
  return createPortal(
    <section
      aria-labelledby="forge-workflows-offer-title"
      data-testid="forge-workflows-offer"
      className="fixed bottom-6 right-6 z-[90] w-full max-w-sm rounded-xl border border-border bg-popover p-4 text-popover-foreground shadow-2xl"
    >
      <h2 id="forge-workflows-offer-title" className="text-sm font-semibold text-foreground">
        AI workflows run in Reliant
      </h2>
      <p className="mt-1 text-xs text-muted-foreground">
        This is the forge console. The workflows tour needs Reliant&apos;s agents and machines, so it runs there.
      </p>
      <div className="mt-3 flex items-center gap-2">
        <a
          href={href}
          onClick={() => {
            trackEvent("tour_link_clicked", { journey: "workflows", target: "reliant" });
            onClose();
          }}
          className="inline-flex items-center rounded-lg bg-primary px-3 py-1.5 text-sm font-medium text-primary-foreground hover:bg-primary/90"
        >
          Open Reliant
        </a>
        <button type="button" onClick={onClose} className="rounded-lg border border-border px-3 py-1.5 text-sm text-foreground hover:bg-muted">
          Stay here
        </button>
      </div>
    </section>,
    document.body,
  );
}
