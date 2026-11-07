// Copyright (c) 2025 Reliant Labs

/**
 * The demo console's banner: this is sample data, and changes are off.
 *
 *  - In every shell: "Guided tour" (hidden while a step is open), and
 *    "How this app was built" (the recorded forge run, `?section=built`).
 *  - Reliant and the forge shell: "Leave the demo" (forge's Apps / the project
 *    picker).
 *  - The public demo (lib/shell.ts `demo`): Install forge and Sign up instead
 *    (there is nowhere to leave to), and one line saying what it records
 *    (lib/demoTelemetry.ts), and where its data comes from when that is not
 *    the built-in sample (DEMO_DATA_SOURCE, demoData/demoDataStatus.ts).
 */

import { useNavigate, useSearch } from "@tanstack/react-router";
import { Compass, FlaskConical, Hammer } from "lucide-react";

import { parseTourParam } from "@/components/Onboarding/tourParam";
import type { TourShell } from "@/components/Onboarding/tourShell";
import { useTourNavigation } from "@/components/Onboarding/useTourNavigation";
import { PRIVACY_POLICY_URL } from "@/lib/privacyLinks";
import { trackEvent } from "@/lib/analytics";
import { browserOptedOut, demoTelemetry, demoTelemetryNotice } from "@/lib/demoTelemetry";
import { isDemoShell } from "@/lib/shell";

import { DemoCta } from "./DemoCta";
import { useDemoDataStatus } from "./demoData/demoDataStatus";
import { DEMO_SECTION_BUILT, FORGE_DEMO_PATH } from "./demoPath";

/** Where "Leave the demo" goes: forge's Apps in Reliant, the project picker in the forge shell. */
const exitHref = (shell: TourShell) => (shell === "forge" ? "/apps" : "/forge");

const LINK = "no-color inline-flex min-h-[44px] items-center gap-1.5 text-sm font-medium text-ink underline-offset-2 hover:underline sm:min-h-0";

export function DemoBanner({ shell }: { shell: TourShell }) {
  const navigate = useNavigate();
  const { startJourney } = useTourNavigation();
  const search = useSearch({ strict: false }) as { tour?: unknown; section?: unknown };
  const tour = parseTourParam(search.tour);
  const demo = isDemoShell();
  const optedOut = demo && browserOptedOut();
  const notice = demo ? demoTelemetryNotice(demoTelemetry(optedOut), optedOut) : null;
  const data = useDemoDataStatus();

  return (
    <div
      role="note"
      className="-mx-1 mb-4 space-y-1 rounded-lg border border-warning-border bg-warning-surface px-3 py-2 text-sm text-ink sm:sticky sm:top-0 sm:z-20"
      data-testid="forge-demo-banner"
    >
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <span className="inline-flex items-center gap-1.5 font-semibold text-warning-ink">
          <FlaskConical className="h-4 w-4" aria-hidden="true" />
          Demo data
        </span>
        <span className="min-w-0 flex-1 basis-56 text-ink-muted" data-testid="forge-demo-data" data-source={demo ? data : "fixtures"}>
          {!demo
            ? "Sample data from a made-up app called shop. Nothing here touches your projects."
            : data === "live"
              ? "Live data from shop, a sample forge app. Nothing you click here changes anything."
              : data === "fallback"
                ? "Showing sample data: the live demo did not answer. Nothing you click here changes anything."
                : "Sample data from a made-up app called shop. Nothing you click here changes anything."}
        </span>
        {!tour && (
          <button
            type="button"
            className={LINK}
            onClick={() => {
              trackEvent("demo_tour_restarted", {});
              startJourney("forge", demo ? "demo" : "menu");
            }}
            data-testid="forge-demo-restart"
          >
            <Compass className="h-3.5 w-3.5" aria-hidden="true" />
            Guided tour
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
            How this app was built
          </button>
        )}
        {demo ? (
          <span className="w-full sm:w-auto">
            <DemoCta placement="banner" size="lg" />
          </span>
        ) : (
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
        )}
      </div>
      {notice && (
        <p className="text-xs text-ink-muted" data-testid="forge-demo-telemetry-notice">
          {notice}{" "}
          <a href={PRIVACY_POLICY_URL} target="_blank" rel="noopener noreferrer" className="underline underline-offset-2">
            Privacy
          </a>
        </p>
      )}
    </div>
  );
}
