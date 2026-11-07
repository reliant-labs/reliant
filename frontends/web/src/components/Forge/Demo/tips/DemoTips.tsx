// Copyright (c) 2025 Reliant Labs

/**
 * The demo's page tours: the first time a visitor opens a console page
 * (Overview, Releases, a release, Environments, an environment, Secrets,
 * Domains, Activity, and each Observability view — Dashboards, Errors,
 * Traces, a trace, Logs, Profiles, Alerts, Sessions), a card says what the
 * page is, how an agent uses it (the `forge …` command) and offers:
 *
 *  - **Show me around**: 2–4 callouts on the page's key parts (tipCopy.ts),
 *    drawn by the journey engine's presentation (OnboardingSpotlight with its
 *    in-callout controls, calloutPlacement.ts: beside the target, never over
 *    it; a bottom sheet on a phone). A stop whose target isn't on the page is
 *    left out.
 *  - **Dismiss**, and **Exit tips** (no more cards this session).
 *  - Sign up (primary) and Install forge, in its footer.
 *
 * Once per page per session (demoSession.ts). Afterwards a small "Show me
 * around" button in the corner replays the page's tour, beside Sign up (on
 * every console page, tour or not; the demo console's banner has its own,
 * so not on /forge/demo). Nothing shows while
 * the guided journey is open, or while the tour toast still waits for an
 * answer; nothing is marked seen meanwhile.
 *
 * Page tours are the engine's steps without the URL: they are short, local to
 * one page, and never collide with `?tour=` (JOURNEYS.md "Page tours").
 *
 * Mounted once at the demo shell's root (demoShellRoutes.tsx).
 */

import { useEffect, useMemo, useState } from "react";
import { useLocation } from "@tanstack/react-router";
import { Bot, Compass, Lightbulb, X } from "lucide-react";

import { OnboardingSpotlight } from "@/components/Onboarding/OnboardingSpotlight";
import { TourControls } from "@/components/Onboarding/TourControls";
import { parseTourParam } from "@/components/Onboarding/tourParam";
import { trackEvent } from "@/lib/analytics";

import { DemoCta, SignUpButton } from "../DemoCta";
import { DemoCorner } from "../DemoCorner";
import { demoSession, useDemoSession } from "../demoSession";
import { FORGE_DEMO_PATH } from "../demoPath";
import { DEMO_TIPS, type PageCallout } from "./tipCopy";
import { demoTipKey, type DemoTipKey } from "./tipKeys";

type Mode = { kind: "card" } | { kind: "pill" } | { kind: "touring"; stops: PageCallout[]; at: number };

/** The page's stops whose targets are on the page now. */
function presentStops(key: DemoTipKey): PageCallout[] {
  return DEMO_TIPS[key].callouts.filter((c) => document.querySelector(c.selector));
}

/** How long a page gets to render its parts before the tour looks for them. */
const SETTLE_MS = 400;

export function DemoTips() {
  const location = useLocation();
  const search = (location.search ?? {}) as Record<string, unknown>;
  const key = demoTipKey(location.pathname, search);
  const tourOpen = !!parseTourParam(search.tour);
  const { tipsDisabled, toastDismissed } = useDemoSession();
  // The toast (DemoTourToast) asks first.
  const toastWaiting = !toastDismissed;
  const [mode, setMode] = useState<Mode | null>(null);
  const [shownKey, setShownKey] = useState<DemoTipKey | null>(null);

  useEffect(() => {
    if (!key || tourOpen || toastWaiting) {
      setMode(null);
      setShownKey(null);
      return;
    }
    setShownKey(key);
    if (!tipsDisabled && !demoSession.hasSeenTip(key)) {
      demoSession.markTipSeen(key);
      setMode({ kind: "card" });
      trackEvent("demo_tip_shown", { tip: key });
    } else {
      setMode({ kind: "pill" });
    }
  }, [key, tourOpen, tipsDisabled, toastWaiting]);

  const start = () => {
    if (!shownKey) return;
    trackEvent("demo_page_tour_started", { tip: shownKey });
    // Give the page a moment: its parts may still be loading.
    window.setTimeout(() => {
      const stops = presentStops(shownKey);
      setMode(stops.length ? { kind: "touring", stops, at: 0 } : { kind: "pill" });
    }, SETTLE_MS);
  };
  const dismiss = () => {
    if (shownKey) trackEvent("demo_tip_dismissed", { tip: shownKey });
    setMode({ kind: "pill" });
  };

  if (tourOpen || toastWaiting) return null;
  // A page with no tour (or before the effect has run) still keeps Sign up in reach.
  if (!shownKey || !mode) return <CornerBar />;
  const tip = DEMO_TIPS[shownKey];

  if (mode.kind === "touring") {
    const stop = mode.stops[mode.at];
    const last = mode.at === mode.stops.length - 1;
    return (
      <PageTourStop
        key={`${shownKey}-${mode.at}`}
        stop={stop}
        number={mode.at + 1}
        total={mode.stops.length}
        onBack={mode.at > 0 ? () => setMode({ ...mode, at: mode.at - 1 }) : undefined}
        onNext={() => {
          if (last) {
            trackEvent("demo_page_tour_completed", { tip: shownKey });
            setMode({ kind: "pill" });
          } else setMode({ ...mode, at: mode.at + 1 });
        }}
        nextLabel={last ? "Done" : "Next"}
        onExit={() => {
          trackEvent("demo_page_tour_exited", { tip: shownKey, at: mode.at + 1 });
          setMode({ kind: "pill" });
        }}
      />
    );
  }

  if (mode.kind === "pill") {
    return (
      <CornerBar>
        <button
          type="button"
          onClick={start}
          className="inline-flex min-h-[44px] items-center gap-1.5 rounded-full border border-border bg-popover px-3.5 text-sm font-medium text-foreground shadow-lg hover:bg-muted sm:min-h-9"
          data-testid="demo-page-tour-replay"
          data-tip={shownKey}
        >
          <Compass className="h-4 w-4 text-primary" aria-hidden="true" />
          Show me around
        </button>
      </CornerBar>
    );
  }

  return (
    <DemoCorner order={1}>
      <section
        aria-label={`About this page: ${tip.title}`}
        aria-live="polite"
        className="max-h-[70vh] overflow-y-auto rounded-xl border border-border bg-popover p-4 text-popover-foreground shadow-2xl"
        data-testid="demo-tip"
        data-tip={shownKey}
      >
        <div className="flex items-start gap-2">
          <Lightbulb className="mt-0.5 h-4 w-4 shrink-0 text-primary" aria-hidden="true" />
          <h3 className="flex-1 text-sm font-semibold text-foreground">{tip.title}</h3>
          <button
            type="button"
            onClick={dismiss}
            aria-label="Dismiss"
            className="-m-2 inline-flex h-[44px] w-[44px] items-center justify-center rounded text-muted-foreground hover:text-foreground sm:h-7 sm:w-7"
          >
            <X className="h-4 w-4" aria-hidden="true" />
          </button>
        </div>
        <p className="mt-1.5 text-sm leading-relaxed text-muted-foreground">{tip.body}</p>
        <p className="mt-2 flex gap-1.5 text-xs leading-relaxed text-muted-foreground" data-testid="demo-tip-agent">
          <Bot className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          <span>
            {tip.agent.before}{" "}
            <code className="break-all rounded bg-surface-sunken px-1 py-0.5 font-mono text-2xs text-ink">{tip.agent.command}</code>
            {tip.agent.after}
          </span>
        </p>
        <div className="mt-3 flex flex-wrap items-center justify-end gap-x-3 gap-y-2">
          <button
            type="button"
            onClick={() => {
              trackEvent("demo_tips_exited", { tip: shownKey });
              demoSession.disableTips();
            }}
            className="mr-auto min-h-[44px] text-xs font-medium text-muted-foreground underline-offset-2 hover:text-foreground hover:underline sm:min-h-0"
            data-testid="demo-tips-exit"
          >
            Exit tips
          </button>
          <button
            type="button"
            onClick={dismiss}
            className="min-h-[44px] rounded-md border border-border px-3 text-xs font-medium text-foreground hover:bg-muted sm:min-h-8"
            data-testid="demo-tip-dismiss"
          >
            Dismiss
          </button>
          <button
            type="button"
            onClick={start}
            className="inline-flex min-h-[44px] items-center gap-1.5 rounded-md border border-primary/40 bg-primary/10 px-3 text-xs font-semibold text-primary-ink hover:bg-primary/15 sm:min-h-8"
            data-testid="demo-page-tour-start"
          >
            <Compass className="h-3.5 w-3.5" aria-hidden="true" />
            Show me around
          </button>
        </div>
        <div className="mt-3 border-t border-border pt-3" data-testid="demo-tip-cta">
          <DemoCta placement="page_card" size="md" />
        </div>
      </section>
    </DemoCorner>
  );
}

/** The corner between cards: Sign up, and the page's replay button when it has a tour. */
function CornerBar({ children }: { children?: React.ReactNode }) {
  const pathname = useLocation({ select: (l) => l.pathname });
  // The demo console's banner has its own.
  const signup = pathname !== FORGE_DEMO_PATH && !pathname.startsWith(`${FORGE_DEMO_PATH}/`);
  if (!signup && !children) return null;
  return (
    <DemoCorner order={1}>
      <div className="flex flex-wrap items-center justify-end gap-2" data-testid="demo-corner-bar">
        {signup && <SignUpButton placement="corner" size="md" className="rounded-full shadow-lg" />}
        {children}
      </div>
    </DemoCorner>
  );
}

/** One stop of a page tour: the engine's spotlight and callout, with its own controls. */
function PageTourStop({
  stop,
  number,
  total,
  onBack,
  onNext,
  nextLabel,
  onExit,
}: {
  stop: PageCallout;
  number: number;
  total: number;
  onBack?: () => void;
  onNext: () => void;
  nextLabel: string;
  onExit: () => void;
}) {
  const footer = useMemo(
    () => <TourControls stepNumber={number} totalSteps={total} onBack={onBack} onNext={onNext} nextLabel={nextLabel} onExit={onExit} />,
    [number, total, onBack, onNext, nextLabel, onExit],
  );
  return (
    <div data-testid="demo-page-tour">
      <OnboardingSpotlight
        targetSelector={stop.selector}
        title={stop.title}
        description={stop.body}
        stepNumber={number}
        totalSteps={total}
        onNext={onNext}
        onBack={onBack}
        onSkipAll={onExit}
        callout={{ footer }}
      />
      <ExitOnEscape onExit={onExit} />
    </div>
  );
}

function ExitOnEscape({ onExit }: { onExit: () => void }) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => e.key === "Escape" && onExit();
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onExit]);
  return null;
}
