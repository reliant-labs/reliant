// Copyright (c) 2025 Reliant Labs

/**
 * The demo console's banner: this is sample data, and changes are off.
 *
 *  - "Take the tour" (hidden while a step is open), and "How it was built"
 *    (the recorded forge run, `?section=built`).
 *  - "Leave the demo" (forge's Apps / the project picker).
 *
 * Reliant and the forge shell only: the public demo (lib/shell.ts `demo`) says
 * the same once, in its action row (DemoScreenActions), with what it records.
 */

import { useNavigate, useSearch } from "@tanstack/react-router";
import { Compass, FlaskConical, Hammer } from "lucide-react";

import { parseTourParam } from "@/components/Onboarding/tourParam";
import type { TourShell } from "@/components/Onboarding/tourShell";
import { useTourNavigation } from "@/components/Onboarding/useTourNavigation";
import { trackEvent } from "@/lib/analytics";

import { DEMO_SECTION_BUILT, FORGE_DEMO_PATH } from "./demoPath";

/** Where "Leave the demo" goes: forge's Apps in Reliant, the project picker in the forge shell. */
const exitHref = (shell: TourShell) => (shell === "forge" ? "/apps" : "/forge");

const LINK = "no-color inline-flex touch-target items-center gap-1.5 text-sm font-medium text-ink underline-offset-2 hover:underline";

export function DemoBanner({ shell }: { shell: TourShell }) {
  const navigate = useNavigate();
  const { startJourney } = useTourNavigation();
  const search = useSearch({ strict: false }) as { tour?: unknown; section?: unknown };
  const tour = parseTourParam(search.tour);

  return (
    <div
      role="note"
      className="mb-4 space-y-1 rounded-lg border border-warning-border bg-surface px-3 py-2 text-sm text-ink"
      data-testid="forge-demo-banner"
    >
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span className="inline-flex items-center gap-1.5 font-semibold text-warning-ink">
          <FlaskConical className="h-4 w-4" aria-hidden="true" />
          Demo data
        </span>
        <span className="min-w-0 flex-1 basis-56 text-ink-muted" data-testid="forge-demo-data" data-source="fixtures">
          Sample data from a made-up app called shop. Nothing here touches your projects.
        </span>
        {!tour && (
          <button
            type="button"
            className={LINK}
            onClick={() => {
              trackEvent("demo_tour_restarted", {});
              startJourney("forge", "menu");
            }}
            data-testid="forge-demo-restart"
          >
            <Compass className="h-3.5 w-3.5" aria-hidden="true" />
            Take the tour
          </button>
        )}
        {search.section !== DEMO_SECTION_BUILT && (
          <button
            type="button"
            className={LINK}
            onClick={() => {
              trackEvent("demo_how_built_opened", { from: "banner" });
              void navigate({
                to: FORGE_DEMO_PATH,
                search: ((prev: Record<string, unknown>) => ({ ...prev, section: DEMO_SECTION_BUILT, trace: undefined, span: undefined, issue: undefined })) as never,
              } as never);
            }}
            data-testid="forge-demo-how-built"
          >
            <Hammer className="h-3.5 w-3.5" aria-hidden="true" />
            How it was built
          </button>
        )}
        <a
          href={exitHref(shell)}
          onClick={(event) => {
            if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return;
            event.preventDefault();
            // Leaving the demo leaves its tour too: the root keeps `?tour=` unless told otherwise.
            void navigate({ to: exitHref(shell), search: { tour: undefined } } as never);
          }}
          className={LINK}
          data-testid="forge-demo-exit"
        >
          Leave the demo
        </a>
      </div>
    </div>
  );
}
