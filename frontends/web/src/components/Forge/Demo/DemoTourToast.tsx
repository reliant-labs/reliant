// Copyright (c) 2025 Reliant Labs

/**
 * The public demo opens on the console, free to explore. This card in the
 * bottom-right corner offers the guided tour instead of starting it:
 *
 *  - "Take the guided tour" starts the forge journey (source `demo`);
 *  - the close button and "Explore on my own" dismiss it.
 *
 * Either way it stays gone for the rest of the session (demoSession.ts), and
 * it never shows while a tour is open (a deep link to a step, a restart from
 * the banner), which also counts as dismissing it. On a phone it is a bottom
 * sheet (DemoCorner), and so is every tour step.
 *
 * Mounted once at the demo shell's root (demoShellRoutes.tsx).
 */

import { useEffect, useRef } from "react";
import { useSearch } from "@tanstack/react-router";
import { Compass, X } from "lucide-react";

import { parseTourParam } from "@/components/Onboarding/tourParam";
import { useTourNavigation } from "@/components/Onboarding/useTourNavigation";
import { trackEvent } from "@/lib/analytics";

import { InstallForgeLink } from "./DemoCta";
import { DemoCorner } from "./DemoCorner";
import { FORGE_SHORT } from "./demoCopy";
import { demoSession, useDemoSession } from "./demoSession";

export function DemoTourToast() {
  const { startJourney } = useTourNavigation();
  const search = useSearch({ strict: false }) as { tour?: unknown };
  const tourOpen = !!parseTourParam(search.tour);
  const { toastDismissed } = useDemoSession();
  const offered = useRef(false);
  const visible = !toastDismissed && !tourOpen;

  // A tour opened some other way answers the offer.
  useEffect(() => {
    if (tourOpen) demoSession.dismissToast();
  }, [tourOpen]);

  useEffect(() => {
    if (!visible || offered.current) return;
    offered.current = true;
    trackEvent("demo_tour_offered", {});
  }, [visible]);

  if (!visible) return null;

  const dismiss = (how: "close" | "explore") => {
    trackEvent("demo_tour_toast_dismissed", { how });
    demoSession.dismissToast();
  };

  return (
    <DemoCorner order={2}>
      <section
        role="dialog"
        aria-modal="false"
        aria-labelledby="demo-tour-toast-title"
        className="rounded-xl border border-border bg-popover p-4 text-popover-foreground shadow-2xl"
        data-testid="demo-tour-toast"
      >
        <div className="flex items-start gap-2">
          <Compass className="mt-0.5 h-4 w-4 shrink-0 text-primary" aria-hidden="true" />
          <h2 id="demo-tour-toast-title" className="flex-1 text-sm font-semibold text-foreground">
            Explore a forge app
          </h2>
          <button
            type="button"
            onClick={() => dismiss("close")}
            aria-label="Dismiss"
            className="-m-2 inline-flex h-11 w-11 items-center justify-center rounded text-muted-foreground hover:text-foreground sm:h-7 sm:w-7"
            data-testid="demo-tour-toast-close"
          >
            <X className="h-3.5 w-3.5" aria-hidden="true" />
          </button>
        </div>
        <p className="mt-1.5 text-sm leading-relaxed text-muted-foreground">
          {FORGE_SHORT} This is the console it gives every app, on sample data. Look around, or take a two-minute tour.
        </p>
        <div className="mt-3 flex flex-wrap items-center justify-end gap-x-3 gap-y-2">
          <span className="mr-auto text-xs">
            <InstallForgeLink placement="toast" />
          </span>
          <button
            type="button"
            onClick={() => dismiss("explore")}
            className="min-h-11 text-xs font-medium text-muted-foreground underline-offset-2 hover:text-foreground hover:underline sm:min-h-0"
            data-testid="demo-tour-toast-dismiss"
          >
            Explore on my own
          </button>
          <button
            type="button"
            onClick={() => {
              demoSession.dismissToast();
              startJourney("forge", "demo");
            }}
            className="min-h-11 rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground hover:bg-primary/90 sm:min-h-0"
            data-testid="demo-tour-toast-start"
          >
            Take the guided tour
          </button>
        </div>
      </section>
    </DemoCorner>
  );
}
